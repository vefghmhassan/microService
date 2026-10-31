package controllers

import (
	"context"
	"fmt"
	"log"
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
	// A real certificate lets the zone use SSL "strict" for CDN inbounds.
	if err := a.zoneCDN(ctx, acc, in.ZoneID); err != nil {
		log.Printf("cloudflare: zone %s CDN settings: %v", in.ZoneName, err)
	}
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
		// With the Global API Key (and the account email) the panel makes a
		// Zone:Read + DNS:Edit token itself; the key is used once, never stored.
		GlobalEmail string `json:"global_email"`
		GlobalKey   string `json:"global_key"`
	}
	if err := c.BodyParser(&in); err != nil {
		return errJSON(c, fiber.StatusBadRequest, "invalid body")
	}
	if s := strings.TrimSpace(in.Name); s != "" {
		acc.Name = s
	}
	if t := strings.TrimSpace(in.Token); t != "" {
		acc.Token = secret.String(t)
	} else if k := strings.TrimSpace(in.GlobalKey); k != "" {
		email := strings.TrimSpace(in.GlobalEmail)
		if email == "" {
			return errJSON(c, fiber.StatusBadRequest, "ایمیل حساب کلودفلر لازم است")
		}
		if acc.Name == "" {
			acc.Name = email
		}
		ctx, cancel := context.WithTimeout(c.UserContext(), 30*time.Second)
		defer cancel()
		var t string
		if cloudflare.LooksLikeGlobalKey(k) {
			cf := cloudflare.New("")
			cf.AuthEmail, cf.AuthKey = email, k
			var err error
			if t, err = cf.CreateToken(ctx, "microserver panel ("+acc.Name+")"); err != nil {
				return errJSON(c, fiber.StatusBadRequest, "ساخت توکن با Global API Key نشد: "+err.Error())
			}
		} else if _, err := cloudflare.New(k).Zones(ctx); err == nil {
			// An API token pasted in the wrong field: it already works, use it.
			t = k
		} else {
			return errJSON(c, fiber.StatusBadRequest, "این مقدار Global API Key نیست (کلید ۳۷ حرف است و فقط 0-9 و a-f دارد) و به‌عنوان API Token هم کار نکرد: "+err.Error())
		}
		acc.Token = secret.String(t)
		in.Token = t
		if acc.AcmeEmail == "" && strings.TrimSpace(in.AcmeEmail) == "" {
			in.AcmeEmail = email
		}
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

// SetupZoneCDN turns on what CDN inbounds need on one zone (WebSockets,
// TLS 1.2+, SSL strict when the zone has a certificate). gRPC is not in the
// API and stays a manual switch.
func (a *App) SetupZoneCDN(c *fiber.Ctx) error {
	acc, err := a.cloudflareAccount(c)
	if err != nil {
		return err
	}
	var in struct {
		ZoneID string `json:"zone_id"`
	}
	if err := c.BodyParser(&in); err != nil || in.ZoneID == "" {
		return errJSON(c, fiber.StatusBadRequest, "zone_id لازم است")
	}
	ctx, cancel := context.WithTimeout(c.UserContext(), 30*time.Second)
	defer cancel()
	if err := a.zoneCDN(ctx, acc, in.ZoneID); err != nil {
		return errJSON(c, fiber.StatusBadGateway, err.Error())
	}
	return c.JSON(fiber.Map{"ok": true})
}

// zoneCDN is SetupZoneCDN without the request; a permission error says how
// to fix the token.
func (a *App) zoneCDN(ctx context.Context, acc *models.CloudflareAccount, zoneID string) error {
	var cert models.Certificate
	strict := a.DB.Where("cf_zone_id = ? AND not_after IS NOT NULL", zoneID).First(&cert).Error == nil
	err := cloudflare.New(string(acc.Token)).EnsureCDNSettings(ctx, zoneID, strict)
	if err != nil && strings.Contains(err.Error(), "10000") {
		return fmt.Errorf("توکن دسترسی Zone Settings:Edit ندارد؛ توکن را با دکمهٔ ساخت توکن (یا Global API Key) دوباره بسازید و در حساب جایگزین کنید: %w", err)
	}
	return err
}
