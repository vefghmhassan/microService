package controllers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/valyala/fasthttp"

	"microserver/internal/models"
	"microserver/internal/services/destination"
	"microserver/internal/services/orchestrator"
	"microserver/internal/services/providers"
	"microserver/internal/services/settings"
)

// ---- dashboard ----

func (a *App) Dashboard(c *fiber.Ctx) error {
	type row struct {
		Status string
		N      int64
	}
	var rows []row
	a.DB.Model(&models.Server{}).Select("status, count(*) as n").Where("status <> ?", models.ServerDeleted).Group("status").Scan(&rows)
	byStatus := map[string]int64{}
	var total int64
	for _, r := range rows {
		byStatus[r.Status] = r.N
		total += r.N
	}
	var drift, providersN, keptIPs, running int64
	a.DB.Model(&models.Server{}).Where("sync_status IN ? AND status <> ?", []string{models.SyncDrift, models.SyncFailed}, models.ServerDeleted).Count(&drift)
	a.DB.Model(&models.ProviderAccount{}).Count(&providersN)
	a.DB.Model(&models.IPAddress{}).Where("status IN ?", []string{models.IPKept, models.IPFiltered}).Count(&keptIPs)
	a.DB.Model(&models.Job{}).Where("status IN ?", []string{models.JobQueued, models.JobRunning}).Count(&running)
	var traffic struct{ Up, Down, Online int64 }
	a.DB.Model(&models.Server{}).Select("COALESCE(SUM(traffic_up),0) AS up, COALESCE(SUM(traffic_down),0) AS down, COALESCE(SUM(online_count),0) AS online").
		Where("status <> ?", models.ServerDeleted).Scan(&traffic)
	var recent []models.Job
	a.DB.Order("id desc").Limit(8).Find(&recent)
	var checks []models.CheckRun
	a.DB.Order("id desc").Limit(8).Find(&checks)
	snap, _ := orchestrator.LoadSnapshot(a.DB)
	tv := 0
	if snap != nil {
		tv = snap.Xray.Version
	}
	return c.JSON(fiber.Map{
		"servers_total": total, "servers_by_status": byStatus, "servers_drift": drift,
		"providers": providersN, "kept_ips": keptIPs, "jobs_active": running,
		"recent_jobs": recent, "recent_checks": checks, "template_version": tv,
		"traffic_up": traffic.Up, "traffic_down": traffic.Down, "online": traffic.Online,
	})
}

// ---- IP pool ----

func (a *App) ListIPs(c *fiber.Ctx) error {
	q := a.DB.Order("id desc")
	if st := c.Query("status"); st != "" {
		q = q.Where("status = ?", st)
	}
	var ips []models.IPAddress
	if err := q.Limit(500).Find(&ips).Error; err != nil {
		return err
	}
	return c.JSON(ips)
}

// ReleaseIP gives a kept IP back to the provider so it stops costing money.
func (a *App) ReleaseIP(c *fiber.Ctx) error {
	id, err := idParam(c)
	if err != nil {
		return err
	}
	var ip models.IPAddress
	if err := a.DB.First(&ip, id).Error; err != nil {
		return errJSON(c, fiber.StatusNotFound, "ip not found")
	}
	if ip.Status == models.IPInUse {
		return errJSON(c, fiber.StatusConflict, "IP در حال استفاده است")
	}
	if ip.Status != models.IPReleased && ip.Ref != "" {
		var acc models.ProviderAccount
		if err := a.DB.First(&acc, ip.ProviderAccountID).Error; err != nil {
			return errJSON(c, fiber.StatusBadRequest, "provider account not found")
		}
		p, err := providers.New(acc.Type, json.RawMessage(acc.Credentials))
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(c.UserContext(), 30*time.Second)
		defer cancel()
		if err := p.ReleaseIP(ctx, ip.Ref); err != nil && !strings.Contains(err.Error(), "not found") {
			return errJSON(c, fiber.StatusBadGateway, err.Error())
		}
	}
	ip.Status = models.IPReleased
	ip.Reusable = false
	a.DB.Save(&ip)
	return c.JSON(ip)
}

// ---- ad-hoc checks ----

func (a *App) RunCheck(c *fiber.Ctx) error {
	var in struct {
		Host string `json:"host"`
		Port int    `json:"port"`
	}
	if err := c.BodyParser(&in); err != nil || strings.TrimSpace(in.Host) == "" {
		return errJSON(c, fiber.StatusBadRequest, "host is required")
	}
	if in.Port <= 0 {
		in.Port = 443
	}
	res := a.Orch.Check(c.UserContext(), strings.TrimSpace(in.Host), in.Port)
	run := models.CheckRun{Host: res.Host, Port: res.Port, Verdict: res.Verdict, OK: res.OK, Summary: res.Summary}
	for _, p := range res.Probes {
		run.Probes = append(run.Probes, models.CheckProbe{Source: p.Source, Node: p.Node, Country: p.Country, City: p.City, ASN: p.ASN, Network: p.Network, OK: p.OK, NoAnswer: p.NoAnswer, LatencyMs: p.LatencyMs, Error: p.Error})
	}
	a.DB.Create(&run)
	return c.JSON(fiber.Map{"run": run, "errors": res.Errors})
}

func (a *App) ListChecks(c *fiber.Ctx) error {
	var runs []models.CheckRun
	if err := a.DB.Preload("Probes").Order("id desc").Limit(c.QueryInt("limit", 30)).Find(&runs).Error; err != nil {
		return err
	}
	return c.JSON(runs)
}

// ---- jobs ----

func (a *App) ListJobs(c *fiber.Ctx) error {
	q := a.DB.Order("id desc").Limit(c.QueryInt("limit", 50))
	if sid := c.QueryInt("server_id", 0); sid > 0 {
		q = q.Where("server_id = ?", sid)
	}
	if st := c.Query("status"); st != "" {
		q = q.Where("status = ?", st)
	}
	var list []models.Job
	if err := q.Find(&list).Error; err != nil {
		return err
	}
	return c.JSON(list)
}

func (a *App) GetJob(c *fiber.Ctx) error {
	id, err := idParam(c)
	if err != nil {
		return err
	}
	var j models.Job
	if err := a.DB.Preload("Logs", func(db *gormDB) *gormDB { return db.Order("id") }).First(&j, id).Error; err != nil {
		return errJSON(c, fiber.StatusNotFound, "job not found")
	}
	return c.JSON(j)
}

func (a *App) CancelJob(c *fiber.Ctx) error {
	id, err := idParam(c)
	if err != nil {
		return err
	}
	if err := a.Engine.Cancel(id); err != nil {
		return errJSON(c, fiber.StatusConflict, err.Error())
	}
	return c.JSON(fiber.Map{"ok": true})
}

func (a *App) EnqueueGlobal(kind string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		j, err := a.Engine.Enqueue(kind, nil, struct{}{}, source(c))
		if err != nil {
			return err
		}
		return c.Status(fiber.StatusAccepted).JSON(j)
	}
}

// Events streams job/log/server events as Server-Sent Events.
func (a *App) Events(c *fiber.Ctx) error {
	c.Set("Content-Type", "text/event-stream")
	c.Set("Cache-Control", "no-cache")
	c.Set("Connection", "keep-alive")
	c.Set("X-Accel-Buffering", "no")
	ch := a.Engine.Broker.Subscribe()
	c.Context().SetBodyStreamWriter(fasthttp.StreamWriter(func(w *bufio.Writer) {
		defer a.Engine.Broker.Unsubscribe(ch)
		ping := time.NewTicker(20 * time.Second)
		defer ping.Stop()
		fmt.Fprint(w, "retry: 3000\n\n")
		if w.Flush() != nil {
			return
		}
		for {
			select {
			case e, ok := <-ch:
				if !ok {
					return
				}
				b, _ := json.Marshal(e)
				fmt.Fprintf(w, "data: %s\n\n", b)
			case <-ping.C:
				fmt.Fprint(w, ": ping\n\n")
			}
			if err := w.Flush(); err != nil {
				return
			}
		}
	}))
	return nil
}

// ---- settings ----

func (a *App) GetSettings(c *fiber.Ctx) error {
	return c.JSON(a.Settings.Public())
}

func (a *App) PutSettings(c *fiber.Ctx) error {
	var in map[string]string
	if err := c.BodyParser(&in); err != nil {
		return errJSON(c, fiber.StatusBadRequest, "invalid body")
	}
	if err := a.Settings.Set(in); err != nil {
		return err
	}
	return c.JSON(a.Settings.Public())
}

func (a *App) TestVPNPanel(c *fiber.Ctx) error {
	u := a.Settings.Get(settings.VPNPanelURL)
	if u == "" {
		return errJSON(c, fiber.StatusBadRequest, "آدرس vpn-pannel تنظیم نشده است")
	}
	ctx, cancel := context.WithTimeout(c.UserContext(), 20*time.Second)
	defer cancel()
	v := destination.NewVPNPanel(u, a.Settings.Get(settings.VPNPanelEmail), a.Settings.Get(settings.VPNPanelPassword))
	if err := v.Test(ctx); err != nil {
		return errJSON(c, fiber.StatusBadGateway, err.Error())
	}
	return c.JSON(fiber.Map{"ok": true})
}

// UploadConfigs sends share links or full xray JSON configs, built by hand in
// the dashboard, to vpn-pannel. vpn-pannel stores a JSON entry as a custom node.
func (a *App) UploadConfigs(c *fiber.Ctx) error {
	var in struct {
		Configs  []string `json:"configs"`
		BaseName string   `json:"base_name"`
		Country  string   `json:"country"`
		Ads      bool     `json:"ads"`
	}
	if err := c.BodyParser(&in); err != nil {
		return errJSON(c, fiber.StatusBadRequest, "invalid body")
	}
	var configs []string
	for _, cfg := range in.Configs {
		cfg = strings.TrimSpace(cfg)
		if cfg == "" {
			continue
		}
		// One line per entry keeps the multiline form field unambiguous.
		if strings.HasPrefix(cfg, "{") {
			var buf bytes.Buffer
			if err := json.Compact(&buf, []byte(cfg)); err != nil {
				return errJSON(c, fiber.StatusBadRequest, "invalid JSON config: "+err.Error())
			}
			cfg = buf.String()
		}
		configs = append(configs, cfg)
	}
	if len(configs) == 0 {
		return errJSON(c, fiber.StatusBadRequest, "no configs to upload")
	}
	u := a.Settings.Get(settings.VPNPanelURL)
	if u == "" {
		return errJSON(c, fiber.StatusBadRequest, "آدرس vpn-pannel تنظیم نشده است")
	}
	ctx, cancel := context.WithTimeout(c.UserContext(), 60*time.Second)
	defer cancel()
	v := destination.NewVPNPanel(u, a.Settings.Get(settings.VPNPanelEmail), a.Settings.Get(settings.VPNPanelPassword))
	if err := v.Upload(ctx, destination.UploadRequest{Links: configs, BaseName: in.BaseName, Country: in.Country, Ads: in.Ads}); err != nil {
		return errJSON(c, fiber.StatusBadGateway, err.Error())
	}
	return c.JSON(fiber.Map{"ok": true, "count": len(configs)})
}

func (a *App) TestTelegramHandler(c *fiber.Ctx) error {
	if a.TestTelegram == nil {
		return errJSON(c, fiber.StatusBadRequest, "bot is not configured")
	}
	if err := a.TestTelegram(); err != nil {
		return errJSON(c, fiber.StatusBadGateway, err.Error())
	}
	return c.JSON(fiber.Map{"ok": true})
}

// ---- meta ----

var (
	releasesMu    sync.Mutex
	releasesCache []string
	releasesAt    time.Time
)

// XUIReleases lists 3x-ui release tags from GitHub, cached for an hour.
func (a *App) XUIReleases(c *fiber.Ctx) error {
	releasesMu.Lock()
	defer releasesMu.Unlock()
	if time.Since(releasesAt) < time.Hour && len(releasesCache) > 0 {
		return c.JSON(releasesCache)
	}
	ctx, cancel := context.WithTimeout(c.UserContext(), 15*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/MHSanaei/3x-ui/releases?per_page=30", nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return errJSON(c, fiber.StatusBadGateway, err.Error())
	}
	defer resp.Body.Close()
	var rel []struct {
		Tag        string `json:"tag_name"`
		Prerelease bool   `json:"prerelease"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return errJSON(c, fiber.StatusBadGateway, "github: "+err.Error())
	}
	tags := make([]string, 0, len(rel))
	for _, r := range rel {
		if !r.Prerelease {
			tags = append(tags, r.Tag)
		}
	}
	releasesCache, releasesAt = tags, time.Now()
	return c.JSON(tags)
}

func (a *App) Meta(c *fiber.Ctx) error {
	return c.JSON(fiber.Map{
		"ssh_public_key": a.Orch.Key.PublicKey,
		"provider_types": providers.Descriptors(),
		"job_kinds": []string{
			orchestrator.KindProvision, orchestrator.KindRotate, orchestrator.KindCheck, orchestrator.KindInstall,
			orchestrator.KindSync, orchestrator.KindSyncAll, orchestrator.KindUpload, orchestrator.KindDelete,
			orchestrator.KindXrayVersion, orchestrator.KindPanelVersion, orchestrator.KindCheckAll,
			orchestrator.KindProvisionAuto,
		},
	})
}
