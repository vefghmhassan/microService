package controllers

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"microserver/internal/models"
	"microserver/internal/services/orchestrator"
)

// cdnAddonView is an add-on with its inbounds and how much uses it.
type cdnAddonView struct {
	models.CDNAddon
	Inbounds  []models.InboundTemplate `json:"inbounds"`
	Locations int64                    `json:"locations"`
	Servers   int64                    `json:"servers"`
}

func (a *App) addonView(ad models.CDNAddon) cdnAddonView {
	v := cdnAddonView{CDNAddon: ad, Inbounds: []models.InboundTemplate{}}
	a.DB.Where("addon_id = ?", ad.ID).Order("port").Find(&v.Inbounds)
	for i := range v.Inbounds {
		v.Inbounds[i].Family = orchestrator.InboundFamily(v.Inbounds[i])
	}
	a.DB.Model(&models.Location{}).Where("cdn_addon_id = ?", ad.ID).Count(&v.Locations)
	a.DB.Model(&models.Server{}).Where("cdn_addon_id = ? AND status <> ?", ad.ID, models.ServerDeleted).Count(&v.Servers)
	return v
}

func (a *App) ListCDNAddons(c *fiber.Ctx) error {
	var list []models.CDNAddon
	if err := a.DB.Order("id").Find(&list).Error; err != nil {
		return err
	}
	out := make([]cdnAddonView, 0, len(list))
	for _, ad := range list {
		out = append(out, a.addonView(ad))
	}
	return c.JSON(out)
}

type cdnAddonInput struct {
	Name          string `json:"name"`
	Enabled       *bool  `json:"enabled"`
	CFAccountID   uint   `json:"cf_account_id"`
	CFZoneID      string `json:"cf_zone_id"`
	CFZoneName    string `json:"cf_zone_name"`
	DirectPattern string `json:"direct_pattern"`
	CDNPattern    string `json:"cdn_pattern"`
	CleanIPCount  int    `json:"clean_ip_count"`
	Note          string `json:"note"`
}

func (a *App) saveCDNAddon(c *fiber.Ctx, ad *models.CDNAddon) error {
	var in cdnAddonInput
	if err := c.BodyParser(&in); err != nil {
		return errJSON(c, fiber.StatusBadRequest, "invalid body")
	}
	if strings.TrimSpace(in.Name) == "" {
		return errJSON(c, fiber.StatusBadRequest, "نام افزونه لازم است")
	}
	var cf models.CloudflareAccount
	if err := a.DB.First(&cf, in.CFAccountID).Error; err != nil {
		return errJSON(c, fiber.StatusBadRequest, "حساب کلودفلر پیدا نشد")
	}
	if in.CFZoneID == "" || in.CFZoneName == "" {
		return errJSON(c, fiber.StatusBadRequest, "دامنه کلودفلر را انتخاب کنید")
	}
	direct := strings.ToLower(strings.TrimSpace(in.DirectPattern))
	cdn := strings.ToLower(strings.TrimSpace(in.CDNPattern))
	for _, p := range []string{direct, cdn} {
		if err := orchestrator.ValidPattern(p); err != nil {
			return errJSON(c, fiber.StatusBadRequest, err.Error())
		}
	}
	if direct == cdn {
		return errJSON(c, fiber.StatusBadRequest, "الگوی مستقیم و الگوی CDN باید فرق داشته باشند (مثل d{n} و c{n})")
	}
	if in.CleanIPCount < 0 || in.CleanIPCount > 30 {
		return errJSON(c, fiber.StatusBadRequest, "تعداد آی‌پی تمیز باید بین 0 تا 30 باشد")
	}
	if ad.ID == 0 {
		ad.Enabled = true
	}
	if in.Enabled != nil {
		ad.Enabled = *in.Enabled
	}
	ad.Name, ad.Note = strings.TrimSpace(in.Name), strings.TrimSpace(in.Note)
	ad.CFAccountID, ad.CFZoneID = cf.ID, in.CFZoneID
	ad.CFZoneName = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(in.CFZoneName)), ".")
	ad.DirectPattern, ad.CDNPattern, ad.CleanIPCount = direct, cdn, in.CleanIPCount
	if err := a.DB.Save(ad).Error; err != nil {
		return err
	}
	// The proxied names serve CDN inbounds; get the zone ready in the background.
	go func(cfID uint, zoneID string) {
		var acc models.CloudflareAccount
		if a.DB.First(&acc, cfID).Error != nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := a.zoneCDN(ctx, &acc, zoneID); err != nil {
			log.Printf("cloudflare: zone %s CDN settings: %v", zoneID, err)
		}
	}(ad.CFAccountID, ad.CFZoneID)
	return c.JSON(a.addonView(*ad))
}

func (a *App) CreateCDNAddon(c *fiber.Ctx) error {
	return a.saveCDNAddon(c, &models.CDNAddon{})
}

func (a *App) cdnAddon(c *fiber.Ctx) (*models.CDNAddon, error) {
	id, err := idParam(c)
	if err != nil {
		return nil, err
	}
	var ad models.CDNAddon
	if err := a.DB.First(&ad, id).Error; err != nil {
		return nil, fiber.NewError(fiber.StatusNotFound, "افزونه پیدا نشد")
	}
	return &ad, nil
}

func (a *App) UpdateCDNAddon(c *fiber.Ctx) error {
	ad, err := a.cdnAddon(c)
	if err != nil {
		return err
	}
	return a.saveCDNAddon(c, ad)
}

// DeleteCDNAddon removes an add-on that no live server uses; its locations
// go back to building servers without it.
func (a *App) DeleteCDNAddon(c *fiber.Ctx) error {
	ad, err := a.cdnAddon(c)
	if err != nil {
		return err
	}
	var n int64
	a.DB.Model(&models.Server{}).Where("cdn_addon_id = ? AND status <> ?", ad.ID, models.ServerDeleted).Count(&n)
	if n > 0 {
		return errJSON(c, fiber.StatusConflict, "سرورهایی با این افزونه هنوز فعال‌اند؛ اول آن‌ها را حذف یا جایگزین کنید")
	}
	a.DB.Model(&models.Location{}).Where("cdn_addon_id = ?", ad.ID).Update("cdn_addon_id", 0)
	a.DB.Where("addon_id = ?", ad.ID).Delete(&models.InboundTemplate{})
	return a.DB.Delete(ad).Error
}

// CreateAddonInbound adds an inbound to an add-on; editing, deleting and
// previewing go through the shared /template/inbounds/:id routes.
func (a *App) CreateAddonInbound(c *fiber.Ctx) error {
	ad, err := a.cdnAddon(c)
	if err != nil {
		return err
	}
	return a.saveInbound(c, &models.InboundTemplate{AddonID: ad.ID, Route: models.RouteCDN})
}

// SyncCDNAddon re-applies the template and add-on to every server using it.
func (a *App) SyncCDNAddon(c *fiber.Ctx) error {
	ad, err := a.cdnAddon(c)
	if err != nil {
		return err
	}
	return a.afterAddonChange(c, ad.ID, true)
}

// afterAddonChange records an add-on edit and, when asked, syncs its servers.
func (a *App) afterAddonChange(c *fiber.Ctx, id uint, sync bool) error {
	h, err := orchestrator.BumpAddon(a.DB, id)
	if err != nil {
		return err
	}
	resp := fiber.Map{"addon_hash": h}
	if sync {
		var ids []uint
		a.DB.Model(&models.Server{}).Where("cdn_addon_id = ? AND status IN ? AND xui_port > 0", id,
			[]string{models.ServerActive, models.ServerFiltered}).Pluck("id", &ids)
		for i := range ids {
			if _, err := a.Engine.Enqueue(orchestrator.KindSync, &ids[i], struct{}{}, source(c)); err != nil {
				return err
			}
		}
		resp["queued"] = len(ids)
	}
	return c.JSON(resp)
}
