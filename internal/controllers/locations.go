package controllers

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"

	"microserver/internal/models"
	"microserver/internal/services/orchestrator"
)

func (a *App) ListLocations(c *fiber.Ctx) error {
	var list []models.Location
	if err := a.DB.Preload("ProviderAccount").Order("priority, id").Find(&list).Error; err != nil {
		return err
	}
	return c.JSON(list)
}

type locationInput struct {
	ProviderAccountID uint   `json:"provider_account_id"`
	Region            string `json:"region"`
	Plan              string `json:"plan"`
	Image             string `json:"image"`
	Country           string `json:"country"`
	Capacity          int    `json:"capacity"`
	Enabled           *bool  `json:"enabled"`
	Note              string `json:"note"`
	CFAccountID       uint   `json:"cf_account_id"`
	CFZoneID          string `json:"cf_zone_id"`
	CFZoneName        string `json:"cf_zone_name"`
	DNSPattern        string `json:"dns_pattern"`
	DNSProxied        bool   `json:"dns_proxied"`
	SSL               bool   `json:"ssl"`
	IPFamily          string `json:"ip_family"`
	NamePrefix        string `json:"name_prefix"`
}

func (a *App) saveLocation(c *fiber.Ctx, l *models.Location) error {
	var in locationInput
	if err := c.BodyParser(&in); err != nil {
		return errJSON(c, fiber.StatusBadRequest, "invalid body")
	}
	var acc models.ProviderAccount
	if err := a.DB.First(&acc, in.ProviderAccountID).Error; err != nil {
		return errJSON(c, fiber.StatusBadRequest, "provider account not found")
	}
	if strings.TrimSpace(in.Region) == "" || strings.TrimSpace(in.Plan) == "" {
		return errJSON(c, fiber.StatusBadRequest, "منطقه و پلن لازم است")
	}
	if in.Capacity < 0 || in.Capacity > 50 {
		return errJSON(c, fiber.StatusBadRequest, "ظرفیت باید بین 0 تا 50 باشد")
	}
	prefix := strings.ToLower(strings.TrimSpace(in.NamePrefix))
	if err := orchestrator.ValidNamePrefix(prefix); err != nil {
		return errJSON(c, fiber.StatusBadRequest, err.Error())
	}
	dns := models.DNS{}
	if in.CFAccountID != 0 {
		var cf models.CloudflareAccount
		if err := a.DB.First(&cf, in.CFAccountID).Error; err != nil {
			return errJSON(c, fiber.StatusBadRequest, "حساب کلودفلر پیدا نشد")
		}
		if in.CFZoneID == "" || in.CFZoneName == "" {
			return errJSON(c, fiber.StatusBadRequest, "دامنه کلودفلر را انتخاب کنید")
		}
		if err := orchestrator.ValidPattern(in.DNSPattern); err != nil {
			return errJSON(c, fiber.StatusBadRequest, err.Error())
		}
		dns = models.DNS{
			CFAccountID: cf.ID, CFZoneID: in.CFZoneID,
			CFZoneName: strings.TrimSuffix(strings.ToLower(strings.TrimSpace(in.CFZoneName)), "."),
			DNSPattern: strings.ToLower(strings.TrimSpace(in.DNSPattern)), DNSProxied: in.DNSProxied, SSL: in.SSL,
		}
	}
	if l.ID == 0 {
		// New entries go to the end of the list.
		var max struct{ M int }
		a.DB.Model(&models.Location{}).Select("COALESCE(MAX(priority), 0) AS m").Scan(&max)
		l.Priority = max.M + 1
		l.Enabled = true
	}
	l.ProviderAccountID = acc.ID
	l.Region, l.Plan, l.Image = strings.TrimSpace(in.Region), strings.TrimSpace(in.Plan), strings.TrimSpace(in.Image)
	l.Country = strings.ToUpper(strings.TrimSpace(in.Country))
	l.Capacity, l.Note, l.NamePrefix = in.Capacity, in.Note, prefix
	if err := checkFamily(c, in.IPFamily); err != nil {
		return err
	}
	l.IPFamily = models.NormFamily(in.IPFamily)
	l.DNS = dns
	if in.Enabled != nil {
		l.Enabled = *in.Enabled
	}
	if err := a.DB.Save(l).Error; err != nil {
		return err
	}
	if dns.DNSProxied {
		// Proxied servers serve CDN inbounds; get the zone ready in the background.
		go func(cfID uint, zoneID string) {
			var cf models.CloudflareAccount
			if a.DB.First(&cf, cfID).Error != nil {
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := a.zoneCDN(ctx, &cf, zoneID); err != nil {
				log.Printf("cloudflare: zone %s CDN settings: %v", zoneID, err)
			}
		}(dns.CFAccountID, dns.CFZoneID)
	}
	l.ProviderAccount = &acc
	return c.JSON(l)
}

func (a *App) CreateLocation(c *fiber.Ctx) error {
	return a.saveLocation(c, &models.Location{})
}

func (a *App) UpdateLocation(c *fiber.Ctx) error {
	id, err := idParam(c)
	if err != nil {
		return err
	}
	var l models.Location
	if err := a.DB.First(&l, id).Error; err != nil {
		return errJSON(c, fiber.StatusNotFound, "location not found")
	}
	return a.saveLocation(c, &l)
}

func (a *App) DeleteLocation(c *fiber.Ctx) error {
	id, err := idParam(c)
	if err != nil {
		return err
	}
	return a.DB.Delete(&models.Location{}, id).Error
}

// ReorderLocations takes the full list of ids in their new order.
func (a *App) ReorderLocations(c *fiber.Ctx) error {
	var in struct {
		IDs []uint `json:"ids"`
	}
	if err := c.BodyParser(&in); err != nil || len(in.IDs) == 0 {
		return errJSON(c, fiber.StatusBadRequest, "ids required")
	}
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		for i, id := range in.IDs {
			if err := tx.Model(&models.Location{}).Where("id = ?", id).Update("priority", i+1).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return a.ListLocations(c)
}
