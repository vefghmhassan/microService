package controllers

import (
	"encoding/json"
	"strings"

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
		Sync        bool   `json:"sync"`
	}
	if err := c.BodyParser(&in); err != nil {
		return errJSON(c, fiber.StatusBadRequest, "invalid body")
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
	var t models.XrayTemplate
	a.DB.FirstOrCreate(&t, models.XrayTemplate{ID: 1})
	t.Outbounds, t.Routing, t.DNS, t.Observatory = in.Outbounds, in.Routing, in.DNS, in.Observatory
	if err := a.DB.Save(&t).Error; err != nil {
		return err
	}
	return a.afterTemplateChange(c, in.Sync)
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
