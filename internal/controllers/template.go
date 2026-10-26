package controllers

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"

	"microserver/internal/models"
	"microserver/internal/services/linkgen"
	"microserver/internal/services/orchestrator"
)

type gormDB = gorm.DB

func validJSON(s string, kind string) error {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, kind+": JSON نامعتبر: "+err.Error())
	}
	return nil
}

func (a *App) GetTemplate(c *fiber.Ctx) error {
	snap, err := orchestrator.LoadSnapshot(a.DB)
	if err != nil {
		return err
	}
	var servers []models.Server
	a.DB.Select("id", "name", "ipv4", "status", "sync_status", "applied_hash").
		Where("status <> ? AND xui_port > 0", models.ServerDeleted).Find(&servers)
	return c.JSON(fiber.Map{"xray": snap.Xray, "inbounds": snap.Inbounds, "hash": snap.Hash, "servers": servers})
}

func (a *App) PutXrayTemplate(c *fiber.Ctx) error {
	var in struct {
		Outbounds   string `json:"outbounds"`
		Routing     string `json:"routing"`
		DNS         string `json:"dns"`
		Observatory string `json:"observatory"`
		// Full is the whole template (Advanced tab); "" leaves full mode.
		// Absent: the form tabs saved, and their keys are written into Full.
		Full *string `json:"full"`
		Sync bool    `json:"sync"`
	}
	if err := c.BodyParser(&in); err != nil {
		return errJSON(c, fiber.StatusBadRequest, "invalid body")
	}
	var t models.XrayTemplate
	a.DB.FirstOrCreate(&t, models.XrayTemplate{ID: 1})
	switch {
	case in.Full != nil && strings.TrimSpace(*in.Full) != "":
		full, err := parseFullTemplate(*in.Full)
		if err != nil {
			return errJSON(c, fiber.StatusBadRequest, err.Error())
		}
		in.Outbounds, in.Routing, in.DNS, in.Observatory = jsonKey(full, "outbounds"), jsonKey(full, "routing"), jsonKey(full, "dns"), jsonKey(full, "observatory")
		t.Full = *in.Full
	case in.Full != nil:
		t.Full = ""
	case t.Full != "":
		full, err := parseFullTemplate(t.Full)
		if err != nil {
			return errJSON(c, fiber.StatusBadRequest, err.Error())
		}
		for k, v := range map[string]string{"outbounds": in.Outbounds, "routing": in.Routing, "dns": in.DNS, "observatory": in.Observatory} {
			if strings.TrimSpace(v) == "" {
				delete(full, k)
				continue
			}
			var val any
			if err := json.Unmarshal([]byte(v), &val); err != nil {
				return errJSON(c, fiber.StatusBadRequest, k+": JSON نامعتبر: "+err.Error())
			}
			full[k] = val
		}
		b, _ := json.MarshalIndent(full, "", "  ")
		t.Full = string(b)
	}
	for k, v := range map[string]string{"outbounds": in.Outbounds, "routing": in.Routing, "dns": in.DNS, "observatory": in.Observatory} {
		if err := validJSON(v, k); err != nil {
			return err
		}
	}
	if in.Outbounds != "" {
		var arr []any
		if json.Unmarshal([]byte(in.Outbounds), &arr) != nil {
			return errJSON(c, fiber.StatusBadRequest, "outbounds باید آرایه JSON باشد")
		}
	}
	t.Outbounds, t.Routing, t.DNS, t.Observatory = in.Outbounds, in.Routing, in.DNS, in.Observatory
	if err := a.DB.Save(&t).Error; err != nil {
		return err
	}
	return a.afterTemplateChange(c, in.Sync)
}

// parseFullTemplate checks a whole xray template. 3x-ui counts traffic
// through the api/stats/policy sections, so they must be kept.
func parseFullTemplate(s string) (map[string]any, error) {
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil, fiber.NewError(fiber.StatusBadRequest, "کانفیگ کامل: JSON نامعتبر: "+err.Error())
	}
	if m == nil {
		return nil, fiber.NewError(fiber.StatusBadRequest, "کانفیگ کامل باید یک شیء JSON باشد")
	}
	if _, ok := m["outbounds"].([]any); !ok {
		return nil, fiber.NewError(fiber.StatusBadRequest, "کانفیگ کامل باید outbounds (آرایه) داشته باشد")
	}
	var missing []string
	for _, k := range []string{"api", "stats", "policy"} {
		if _, ok := m[k]; !ok {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return nil, fiber.NewError(fiber.StatusBadRequest, "بخش‌های "+strings.Join(missing, "، ")+" لازم است؛ 3x-ui ترافیک کاربران را از طریق آن‌ها می‌شمارد")
	}
	return m, nil
}

// jsonKey returns one key of m as JSON text ("" when absent).
func jsonKey(m map[string]any, k string) string {
	v, ok := m[k]
	if !ok || v == nil {
		return ""
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func (a *App) afterTemplateChange(c *fiber.Ctx, sync bool) error {
	snap, err := orchestrator.BumpTemplate(a.DB)
	if err != nil {
		return err
	}
	resp := fiber.Map{"version": snap.Xray.Version, "hash": snap.Hash}
	if sync {
		j, err := a.Engine.Enqueue(orchestrator.KindSyncAll, nil, struct{}{}, source(c))
		if err != nil {
			return err
		}
		resp["job"] = j
	}
	return c.JSON(resp)
}

type inboundInput struct {
	Remark         string `json:"remark"`
	Protocol       string `json:"protocol"`
	Port           int    `json:"port"`
	Enabled        *bool  `json:"enabled"`
	Settings       string `json:"settings"`
	StreamSettings string `json:"stream_settings"`
	Sniffing       string `json:"sniffing"`
	Upload         *bool  `json:"upload"`
	UploadJSON     *bool  `json:"upload_json"`
	ClientCount    int    `json:"client_count"`
	Sync           bool   `json:"sync"`
}

var protocols = map[string]bool{"vless": true, "vmess": true, "trojan": true, "shadowsocks": true}

func (a *App) saveInbound(c *fiber.Ctx, t *models.InboundTemplate) error {
	var in inboundInput
	if err := c.BodyParser(&in); err != nil {
		return errJSON(c, fiber.StatusBadRequest, "invalid body")
	}
	if t.ID == 0 {
		t.Enabled, t.Upload = true, true
	}
	if in.Remark != "" {
		t.Remark = strings.TrimSpace(in.Remark)
	}
	if in.Protocol != "" {
		t.Protocol = in.Protocol
	}
	if !protocols[t.Protocol] {
		return errJSON(c, fiber.StatusBadRequest, "protocol must be vless, vmess, trojan or shadowsocks")
	}
	if in.Port != 0 {
		t.Port = in.Port
	}
	if t.Port < 1 || t.Port > 65535 || t.Remark == "" {
		return errJSON(c, fiber.StatusBadRequest, "remark and a valid port are required")
	}
	if in.Enabled != nil {
		t.Enabled = *in.Enabled
	}
	if in.Upload != nil {
		t.Upload = *in.Upload
	}
	if in.UploadJSON != nil {
		t.UploadJSON = *in.UploadJSON
	}
	for k, v := range map[string]string{"settings": in.Settings, "stream_settings": in.StreamSettings, "sniffing": in.Sniffing} {
		if err := validJSON(v, k); err != nil {
			return err
		}
	}
	t.Settings, t.StreamSettings, t.Sniffing = in.Settings, in.StreamSettings, in.Sniffing

	// Clients and Reality keys are generated here, once, so every server
	// gets the same ones and links match across the fleet.
	count := in.ClientCount
	if count == 0 && t.ID == 0 {
		count = 1
	}
	var err error
	if t.Settings, err = linkgen.PrepareSettings(t.Protocol, t.Settings, count, strings.ToLower(sanitize(t.Remark))+"-"); err != nil {
		return errJSON(c, fiber.StatusBadRequest, err.Error())
	}
	if t.StreamSettings, err = linkgen.PrepareStream(t.StreamSettings); err != nil {
		return errJSON(c, fiber.StatusBadRequest, err.Error())
	}
	if err := a.DB.Save(t).Error; err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return errJSON(c, fiber.StatusConflict, "این پورت قبلاً استفاده شده است")
		}
		return err
	}
	return a.afterTemplateChange(c, in.Sync)
}

func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "u"
	}
	return b.String()
}

func (a *App) CreateInbound(c *fiber.Ctx) error {
	return a.saveInbound(c, &models.InboundTemplate{})
}

func (a *App) UpdateInbound(c *fiber.Ctx) error {
	id, err := idParam(c)
	if err != nil {
		return err
	}
	var t models.InboundTemplate
	if err := a.DB.First(&t, id).Error; err != nil {
		return errJSON(c, fiber.StatusNotFound, "inbound not found")
	}
	return a.saveInbound(c, &t)
}

func (a *App) DeleteInbound(c *fiber.Ctx) error {
	id, err := idParam(c)
	if err != nil {
		return err
	}
	if err := a.DB.Delete(&models.InboundTemplate{}, id).Error; err != nil {
		return err
	}
	return a.afterTemplateChange(c, c.Query("sync") == "1")
}

// InboundLinks previews the links of one inbound for a sample host.
func (a *App) InboundLinks(c *fiber.Ctx) error {
	id, err := idParam(c)
	if err != nil {
		return err
	}
	var t models.InboundTemplate
	if err := a.DB.First(&t, id).Error; err != nil {
		return errJSON(c, fiber.StatusNotFound, "inbound not found")
	}
	host := c.Query("host", "1.2.3.4")
	links, err := linkgen.Links(t.Protocol, t.Settings, t.StreamSettings, host, t.Port, t.Remark)
	if err != nil {
		return errJSON(c, fiber.StatusBadRequest, err.Error())
	}
	return c.JSON(links)
}

// NewX25519 generates a Reality key pair for the inbound form.
func (a *App) NewX25519(c *fiber.Ctx) error {
	priv, pub, err := linkgen.X25519()
	if err != nil {
		return err
	}
	return c.JSON(fiber.Map{"private_key": priv, "public_key": pub})
}

// templateServer is the server named by ?server_id / server_id, or the first
// live one with 3x-ui, for actions that need a real panel.
func (a *App) templateServer(id int) (*models.Server, error) {
	var s models.Server
	q := a.DB.Preload("ProviderAccount").Where("status IN ? AND xui_port > 0 AND ipv4 <> ''", []string{models.ServerActive, models.ServerFiltered})
	if id > 0 {
		q = q.Where("id = ?", id)
	}
	if err := q.Order("id").First(&s).Error; err != nil {
		return nil, fiber.NewError(fiber.StatusConflict, "هیچ سرور فعالی با 3x-ui پیدا نشد")
	}
	return &s, nil
}

// XrayBase returns the live xray template of one server, to start the
// Advanced editor from what 3x-ui actually runs.
func (a *App) XrayBase(c *fiber.Ctx) error {
	s, err := a.templateServer(c.QueryInt("server_id"))
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(c.UserContext(), 40*time.Second)
	defer cancel()
	remote, panel, err := a.Orch.RemoteFor(ctx, s)
	if err != nil {
		return errJSON(c, fiber.StatusBadGateway, err.Error())
	}
	defer remote.Close()
	setting, err := panel.XraySetting(ctx)
	if err != nil {
		return errJSON(c, fiber.StatusBadGateway, err.Error())
	}
	return c.JSON(fiber.Map{"server": s.Name, "template": setting})
}

// TestOutbounds tests outbounds from a server, like 3x-ui's outbound test.
func (a *App) TestOutbounds(c *fiber.Ctx) error {
	var in struct {
		ServerID  int              `json:"server_id"`
		Mode      string           `json:"mode"`
		Outbounds []map[string]any `json:"outbounds"`
		Tags      []string         `json:"tags"`
	}
	if err := c.BodyParser(&in); err != nil {
		return errJSON(c, fiber.StatusBadRequest, "invalid body")
	}
	s, err := a.templateServer(in.ServerID)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(c.UserContext(), 60*time.Second)
	defer cancel()
	remote, _, err := a.Orch.RemoteFor(ctx, s)
	if err != nil {
		return errJSON(c, fiber.StatusBadGateway, err.Error())
	}
	defer remote.Close()
	res, err := orchestrator.TestOutbounds(ctx, remote, in.Mode, in.Outbounds, in.Tags)
	if err != nil {
		return errJSON(c, fiber.StatusBadGateway, err.Error())
	}
	return c.JSON(fiber.Map{"server": s.Name, "results": res})
}
