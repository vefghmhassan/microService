package controllers

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"microserver/internal/models"
	"microserver/internal/services/orchestrator"
)

func (a *App) server(c *fiber.Ctx) (*models.Server, error) {
	id, err := idParam(c)
	if err != nil {
		return nil, err
	}
	var s models.Server
	if err := a.DB.Preload("ProviderAccount").First(&s, id).Error; err != nil {
		return nil, fiber.NewError(fiber.StatusNotFound, "server not found")
	}
	return &s, nil
}

func (a *App) ListServers(c *fiber.Ctx) error {
	q := a.DB.Preload("ProviderAccount").Order("id desc")
	if c.Query("all") != "1" {
		q = q.Where("status <> ?", models.ServerDeleted)
	}
	if st := c.Query("status"); st != "" {
		q = q.Where("status = ?", st)
	}
	var list []models.Server
	if err := q.Find(&list).Error; err != nil {
		return err
	}
	return c.JSON(list)
}

func (a *App) GetServer(c *fiber.Ctx) error {
	s, err := a.server(c)
	if err != nil {
		return err
	}
	return c.JSON(s)
}

func (a *App) CreateServer(c *fiber.Ctx) error {
	if c.Query("auto") == "1" {
		return a.createServerAuto(c)
	}
	var p orchestrator.ProvisionParams
	if err := c.BodyParser(&p); err != nil {
		return errJSON(c, fiber.StatusBadRequest, "invalid body")
	}
	var acc models.ProviderAccount
	if err := a.DB.First(&acc, p.AccountID).Error; err != nil {
		return errJSON(c, fiber.StatusBadRequest, "provider account not found")
	}
	if !acc.Enabled {
		return errJSON(c, fiber.StatusBadRequest, "این حساب غیرفعال است")
	}
	count := c.QueryInt("count", 1)
	if count < 1 || count > 10 {
		return errJSON(c, fiber.StatusBadRequest, "count must be 1-10")
	}
	var out []*models.Job
	for i := 0; i < count; i++ {
		pp := p
		if count > 1 && p.Name != "" {
			pp.Name = p.Name + "-" + string(rune('a'+i))
		}
		j, err := a.Engine.Enqueue(orchestrator.KindProvision, nil, pp, source(c))
		if err != nil {
			return err
		}
		out = append(out, j)
	}
	return c.Status(fiber.StatusAccepted).JSON(out)
}

// ServerAction queues one of the per-server jobs.
func (a *App) ServerAction(kind string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		s, err := a.server(c)
		if err != nil {
			return err
		}
		if s.Status == models.ServerDeleted {
			return errJSON(c, fiber.StatusConflict, "server is deleted")
		}
		var params map[string]any
		if len(c.Body()) > 0 {
			if err := c.BodyParser(&params); err != nil {
				return errJSON(c, fiber.StatusBadRequest, "invalid body")
			}
		}
		if params == nil {
			params = map[string]any{}
		}
		j, err := a.Engine.Enqueue(kind, &s.ID, params, source(c))
		if err != nil {
			return err
		}
		return c.Status(fiber.StatusAccepted).JSON(j)
	}
}

// UpdateServer edits the fields an operator may change by hand.
func (a *App) UpdateServer(c *fiber.Ctx) error {
	s, err := a.server(c)
	if err != nil {
		return err
	}
	var in struct {
		Name         *string `json:"name"`
		Country      *string `json:"country"`
		WantPanelVer *string `json:"want_panel_version"`
		WantXrayVer  *string `json:"want_xray_version"`
		TrafficLimit *int    `json:"traffic_limit_gb"`
	}
	if err := c.BodyParser(&in); err != nil {
		return errJSON(c, fiber.StatusBadRequest, "invalid body")
	}
	if in.Name != nil {
		s.Name = *in.Name
	}
	if in.Country != nil {
		s.Country = *in.Country
	}
	if in.WantPanelVer != nil {
		s.WantPanelVer = *in.WantPanelVer
	}
	if in.WantXrayVer != nil {
		s.WantXrayVer = *in.WantXrayVer
	}
	if in.TrafficLimit != nil && *in.TrafficLimit >= 0 {
		s.TrafficLimitGB = *in.TrafficLimit
	}
	if err := a.DB.Model(s).Select("name", "country", "want_panel_ver", "want_xray_ver", "traffic_limit_gb").Updates(s).Error; err != nil {
		return err
	}
	return c.JSON(s)
}

func (a *App) ServerLinks(c *fiber.Ctx) error {
	s, err := a.server(c)
	if err != nil {
		return err
	}
	snap, err := orchestrator.LoadSnapshot(a.DB)
	if err != nil {
		return err
	}
	links, lerr := orchestrator.Links(snap, orchestrator.LinkHost(s))
	resp := fiber.Map{"links": links}
	if lerr != nil {
		resp["warning"] = lerr.Error()
	}
	return c.JSON(resp)
}

func (a *App) ServerChecks(c *fiber.Ctx) error {
	s, err := a.server(c)
	if err != nil {
		return err
	}
	var runs []models.CheckRun
	if err := a.DB.Preload("Probes").Where("server_id = ?", s.ID).Order("id desc").Limit(c.QueryInt("limit", 20)).Find(&runs).Error; err != nil {
		return err
	}
	return c.JSON(runs)
}

// ServerLive reads the panel's status and installable xray versions over SSH.
func (a *App) ServerLive(c *fiber.Ctx) error {
	s, err := a.server(c)
	if err != nil {
		return err
	}
	if s.XUIPort == 0 || s.IPv4 == "" {
		return errJSON(c, fiber.StatusConflict, "3x-ui is not installed")
	}
	ctx, cancel := context.WithTimeout(c.UserContext(), 40*time.Second)
	defer cancel()
	remote, panel, err := a.Orch.RemoteFor(ctx, s)
	if err != nil {
		return errJSON(c, fiber.StatusBadGateway, err.Error())
	}
	defer remote.Close()
	st, err := panel.Status(ctx)
	if err != nil {
		return errJSON(c, fiber.StatusBadGateway, err.Error())
	}
	versions, verr := panel.XrayVersions(ctx)
	resp := fiber.Map{"status": st, "xray_versions": versions}
	if verr != nil {
		resp["xray_versions_error"] = verr.Error()
	}
	return c.JSON(resp)
}

// ServerPanelAccess returns the 3x-ui login: a direct browser URL when the
// panel listens publicly, else the SSH tunnel to reach it.
func (a *App) ServerPanelAccess(c *fiber.Ctx) error {
	s, err := a.server(c)
	if err != nil {
		return err
	}
	out := fiber.Map{
		"username": s.XUIUsername, "password": string(s.XUIPassword),
		"port": s.XUIPort, "base_path": s.XUIBasePath, "public": s.XUIPublic,
	}
	if s.XUIPublic {
		out["url"] = orchestrator.PanelURL(s)
	} else {
		out["tunnel"] = "ssh -i data/id_ed25519 -L 2053:127.0.0.1:" + strconv.Itoa(s.XUIPort) + " root@" + s.IPv4
		out["url"] = "http://127.0.0.1:2053/" + strings.Trim(s.XUIBasePath, "/") + "/"
	}
	return c.JSON(out)
}

// createServerAuto queues one placement job that walks the location priority
// list and keeps count healthy servers.
func (a *App) createServerAuto(c *fiber.Ctx) error {
	var p orchestrator.AutoParams
	if err := c.BodyParser(&p); err != nil {
		return errJSON(c, fiber.StatusBadRequest, "invalid body")
	}
	if p.Count < 1 || p.Count > 20 {
		return errJSON(c, fiber.StatusBadRequest, "count must be 1-20")
	}
	var n int64
	a.DB.Model(&models.Location{}).Where("enabled = ?", true).Count(&n)
	if n == 0 {
		return errJSON(c, fiber.StatusBadRequest, "ابتدا در صفحه لوکیشن‌ها حداقل یک لوکیشن فعال اضافه کنید")
	}
	j, err := a.Engine.Enqueue(orchestrator.KindProvisionAuto, nil, p, source(c))
	if err != nil {
		return err
	}
	return c.Status(fiber.StatusAccepted).JSON([]*models.Job{j})
}

// ServerInbounds returns the latest per-inbound traffic and online users.
func (a *App) ServerInbounds(c *fiber.Ctx) error {
	s, err := a.server(c)
	if err != nil {
		return err
	}
	var rows []models.InboundStat
	a.DB.Where("server_id = ?", s.ID).Order("port").Find(&rows)
	return c.JSON(fiber.Map{"inbounds": rows, "limit_bytes": a.Orch.TrafficLimit(s), "server": s})
}

// RefreshStats reads one panel's traffic right now.
func (a *App) RefreshStats(c *fiber.Ctx) error {
	s, err := a.server(c)
	if err != nil {
		return err
	}
	if s.XUIPort == 0 {
		return errJSON(c, fiber.StatusConflict, "3x-ui is not installed")
	}
	ctx, cancel := context.WithTimeout(c.UserContext(), 60*time.Second)
	defer cancel()
	if err := a.Orch.CollectStats(ctx, s); err != nil {
		return errJSON(c, fiber.StatusBadGateway, err.Error())
	}
	return a.ServerInbounds(c)
}

// ReplaceServer builds a fresh server for this one (same location first,
// then the priority list), moves its configs in vpn-pannel and deletes it.
func (a *App) ReplaceServer(c *fiber.Ctx) error {
	s, err := a.server(c)
	if err != nil {
		return err
	}
	if s.Status == models.ServerDeleted || s.Status == models.ServerDeleting {
		return errJSON(c, fiber.StatusConflict, "server is deleted")
	}
	j, err := a.Orch.QueueReplace(s, "manual (panel)", source(c))
	if err != nil {
		return errJSON(c, fiber.StatusConflict, err.Error())
	}
	return c.Status(fiber.StatusAccepted).JSON(j)
}
