package controllers

import (
	"context"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"microserver/internal/models"
	"microserver/internal/services/cloudflare"
	"microserver/internal/services/secret"
)

// cloudflareView hides the token; HasToken tells the form whether leaving it
// empty keeps the stored one.
type cloudflareView struct {
	models.CloudflareAccount
	HasToken      bool  `json:"has_token"`
	LocationCount int64 `json:"location_count"`
	ServerCount   int64 `json:"server_count"`
}

func (a *App) cloudflareView(acc models.CloudflareAccount) cloudflareView {
	v := cloudflareView{CloudflareAccount: acc, HasToken: acc.Token != ""}
	a.DB.Model(&models.Location{}).Where("cf_account_id = ?", acc.ID).Count(&v.LocationCount)
	a.DB.Model(&models.Server{}).Where("cf_account_id = ? AND status <> ? AND hostname <> ''", acc.ID, models.ServerDeleted).Count(&v.ServerCount)
	return v
}

func (a *App) ListCloudflare(c *fiber.Ctx) error {
	var rows []models.CloudflareAccount
	if err := a.DB.Order("id").Find(&rows).Error; err != nil {
		return err
	}
	out := make([]cloudflareView, len(rows))
	for i, acc := range rows {
		out[i] = a.cloudflareView(acc)
	}
	return c.JSON(out)
}

func (a *App) CreateCloudflare(c *fiber.Ctx) error {
	return a.saveCloudflare(c, &models.CloudflareAccount{Enabled: true})
}

func (a *App) UpdateCloudflare(c *fiber.Ctx) error {
	acc, err := a.cloudflareAccount(c)
	if err != nil {
		return err
	}
	return a.saveCloudflare(c, acc)
}

func (a *App) DeleteCloudflare(c *fiber.Ctx) error {
	acc, err := a.cloudflareAccount(c)
	if err != nil {
		return err
	}
	if v := a.cloudflareView(*acc); v.LocationCount > 0 || v.ServerCount > 0 {
		return errJSON(c, fiber.StatusConflict, "این حساب هنوز در لوکیشن‌ها یا سرورها استفاده می‌شود")
	}
	return a.DB.Delete(acc).Error
}

// TestCloudflare lists the zones with the stored token.
func (a *App) TestCloudflare(c *fiber.Ctx) error {
	acc, err := a.cloudflareAccount(c)
	if err != nil {
		return err
	}
	zones, err := a.zones(c, acc)
	if err != nil {
		return errJSON(c, fiber.StatusBadGateway, err.Error())
	}
	return c.JSON(fiber.Map{"ok": true, "zones": len(zones)})
}

// CloudflareZones lists the domains of one account for the location form.
func (a *App) CloudflareZones(c *fiber.Ctx) error {
	acc, err := a.cloudflareAccount(c)
	if err != nil {
		return err
	}
	zones, err := a.zones(c, acc)
	if err != nil {
		return errJSON(c, fiber.StatusBadGateway, err.Error())
	}
	return c.JSON(zones)
}

func (a *App) zones(c *fiber.Ctx, acc *models.CloudflareAccount) ([]cloudflare.Zone, error) {
	ctx, cancel := context.WithTimeout(c.UserContext(), 30*time.Second)
	defer cancel()
	return cloudflare.New(string(acc.Token)).Zones(ctx)
}

// certView is a zone certificate as the UI shows it.
type certView struct {
	models.Certificate
	ServerCount int64 `json:"server_count"`
}

// ListCertificates shows every zone certificate and how many servers use it.
func (a *App) ListCertificates(c *fiber.Ctx) error {
	var rows []models.Certificate
	if err := a.DB.Order("zone_name").Find(&rows).Error; err != nil {
		return err
	}
	out := make([]certView, len(rows))
	for i, cert := range rows {
		out[i] = certView{Certificate: cert}
		a.DB.Model(&models.Server{}).Where("ssl = ? AND cf_zone_name = ? AND status <> ?", true, cert.ZoneName, models.ServerDeleted).Count(&out[i].ServerCount)
	}
	return c.JSON(out)
}

// IssueCertificate issues (or renews, when due) a zone's wildcard
// certificate now, then copies it to the servers holding an older one.
func (a *App) IssueCertificate(c *fiber.Ctx) error {
	acc, err := a.cloudflareAccount(c)
	if err != nil {
		return err
	}
	var in struct {
		ZoneID   string `json:"zone_id"`
		ZoneName string `json:"zone_name"`
	}
	if err := c.BodyParser(&in); err != nil || in.ZoneID == "" || in.ZoneName == "" {
		return errJSON(c, fiber.StatusBadRequest, "zone_id و zone_name لازم است")
	}
	if a.Orch.Certs == nil {
		return errJSON(c, fiber.StatusServiceUnavailable, "certificates are not enabled")
	}
	ctx, cancel := context.WithTimeout(c.UserContext(), 5*time.Minute)
	defer cancel()
	cert, _, err := a.Orch.Certs.Ensure(ctx, acc.ID, in.ZoneID, in.ZoneName, nil)
	if err != nil {
		return errJSON(c, fiber.StatusBadGateway, err.Error())
	}
	_ = a.Orch.RenewCerts(context.WithoutCancel(ctx))
	return c.JSON(certView{Certificate: *cert})
}

func (a *App) cloudflareAccount(c *fiber.Ctx) (*models.CloudflareAccount, error) {
	id, err := idParam(c)
	if err != nil {
		return nil, err
	}
	var acc models.CloudflareAccount
	if err := a.DB.First(&acc, id).Error; err != nil {
		return nil, fiber.NewError(fiber.StatusNotFound, "cloudflare account not found")
	}
	return &acc, nil
}

func (a *App) saveCloudflare(c *fiber.Ctx, acc *models.CloudflareAccount) error {
	var in struct {
		Name      string `json:"name"`
		Token     string `json:"token"`
		AcmeEmail string `json:"acme_email"`
		Enabled   *bool  `json:"enabled"`
	}
	if err := c.BodyParser(&in); err != nil {
		return errJSON(c, fiber.StatusBadRequest, "invalid body")
	}
	if s := strings.TrimSpace(in.Name); s != "" {
		acc.Name = s
	}
	if t := strings.TrimSpace(in.Token); t != "" {
		acc.Token = secret.String(t)
	}
	acc.AcmeEmail = strings.TrimSpace(in.AcmeEmail)
	if in.Enabled != nil {
		acc.Enabled = *in.Enabled
	}
	if acc.Name == "" || acc.Token == "" {
		return errJSON(c, fiber.StatusBadRequest, "نام و توکن لازم است")
	}
	// A token that cannot list zones is useless here; refuse it up front.
	if in.Token != "" {
		if _, err := a.zones(c, acc); err != nil {
			return errJSON(c, fiber.StatusBadRequest, "توکن کلودفلر کار نکرد: "+err.Error())
		}
	}
	if err := a.DB.Save(acc).Error; err != nil {
		return err
	}
	return c.JSON(a.cloudflareView(*acc))
}
