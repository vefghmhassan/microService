package controllers

import (
	"strings"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"

	"microserver/internal/models"
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
	l.Capacity, l.Note = in.Capacity, in.Note
	if in.Enabled != nil {
		l.Enabled = *in.Enabled
	}
	if err := a.DB.Save(l).Error; err != nil {
		return err
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
