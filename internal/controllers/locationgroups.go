package controllers

import (
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"

	"microserver/internal/models"
	"microserver/internal/services/orchestrator"
)

// Location bundles: named, ordered lists of locations. Building N servers
// from a bundle puts one in each of its first N locations that work.

func (a *App) ListLocationGroups(c *fiber.Ctx) error {
	out := []models.LocationGroup{}
	if err := a.DB.Order("priority, id").Find(&out).Error; err != nil {
		return err
	}
	return c.JSON(out)
}

// saveLocationGroup reads the body over g, so a PUT with only some fields
// (the enable switch) keeps the rest.
func (a *App) saveLocationGroup(c *fiber.Ctx, g *models.LocationGroup) error {
	id, created, prio := g.ID, g.CreatedAt, g.Priority
	if err := c.BodyParser(g); err != nil {
		return errJSON(c, fiber.StatusBadRequest, "invalid body")
	}
	g.ID, g.CreatedAt, g.Priority = id, created, prio
	g.Name = strings.TrimSpace(g.Name)
	g.Note = strings.TrimSpace(g.Note)
	if g.Name == "" || len([]rune(g.Name)) > 100 {
		return errJSON(c, fiber.StatusBadRequest, "نام بسته لازم است (حداکثر ۱۰۰ نویسه)")
	}
	var n int64
	a.DB.Model(&models.LocationGroup{}).Where("id <> ? AND LOWER(name) = LOWER(?)", g.ID, g.Name).Count(&n)
	if n > 0 {
		return errJSON(c, fiber.StatusConflict, "بسته‌ای با این نام وجود دارد")
	}
	ids := orchestrator.ModeLocationIDs(g.LocationIDs)
	if len(ids) == 0 {
		return errJSON(c, fiber.StatusBadRequest, "دست‌کم یک لوکیشن انتخاب کنید")
	}
	parts := make([]string, 0, len(ids))
	for _, lid := range ids {
		var cnt int64
		a.DB.Model(&models.Location{}).Where("id = ?", lid).Count(&cnt)
		if cnt == 0 {
			return errJSON(c, fiber.StatusBadRequest, "لوکیشن "+strconv.FormatUint(uint64(lid), 10)+" پیدا نشد")
		}
		parts = append(parts, strconv.FormatUint(uint64(lid), 10))
	}
	g.LocationIDs = strings.Join(parts, ",")
	if g.ID == 0 {
		var last models.LocationGroup
		a.DB.Order("priority DESC").First(&last)
		g.Priority = last.Priority + 1
	}
	if err := a.DB.Save(g).Error; err != nil {
		return err
	}
	return c.JSON(g)
}

func (a *App) locationGroup(c *fiber.Ctx) (*models.LocationGroup, error) {
	id, err := idParam(c)
	if err != nil {
		return nil, err
	}
	var g models.LocationGroup
	if err := a.DB.First(&g, id).Error; err != nil {
		return nil, fiber.NewError(fiber.StatusNotFound, "بسته پیدا نشد")
	}
	return &g, nil
}

func (a *App) CreateLocationGroup(c *fiber.Ctx) error {
	return a.saveLocationGroup(c, &models.LocationGroup{Enabled: true})
}

func (a *App) UpdateLocationGroup(c *fiber.Ctx) error {
	g, err := a.locationGroup(c)
	if err != nil {
		return err
	}
	return a.saveLocationGroup(c, g)
}

func (a *App) DeleteLocationGroup(c *fiber.Ctx) error {
	g, err := a.locationGroup(c)
	if err != nil {
		return err
	}
	if err := a.DB.Delete(g).Error; err != nil {
		return err
	}
	return c.SendStatus(fiber.StatusNoContent)
}

func (a *App) ReorderLocationGroups(c *fiber.Ctx) error {
	var in struct {
		IDs []uint `json:"ids"`
	}
	if err := c.BodyParser(&in); err != nil || len(in.IDs) == 0 {
		return errJSON(c, fiber.StatusBadRequest, "ids required")
	}
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		for i, id := range in.IDs {
			if err := tx.Model(&models.LocationGroup{}).Where("id = ?", id).Update("priority", i+1).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return a.ListLocationGroups(c)
}
