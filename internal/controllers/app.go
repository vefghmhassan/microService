// Package controllers is the C of the MVC split: JSON handlers for /api/v1.
// The view is the React dashboard in web/, served as static files.
package controllers

import (
	"strconv"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"

	"microserver/internal/config"
	"microserver/internal/jobs"
	"microserver/internal/middleware"
	"microserver/internal/services/geoip"
	"microserver/internal/services/orchestrator"
	"microserver/internal/services/settings"
)

type App struct {
	DB       *gorm.DB
	Cfg      *config.Config
	Settings *settings.Store
	Engine   *jobs.Engine
	Orch     *orchestrator.Orchestrator
	Auth     *middleware.Auth
	Geo      *geoip.DB
	// TestTelegram sends a test message with the current bot settings.
	TestTelegram func() error
}

func errJSON(c *fiber.Ctx, status int, msg string) error {
	return c.Status(status).JSON(fiber.Map{"error": msg})
}

func idParam(c *fiber.Ctx) (uint, error) {
	n, err := strconv.ParseUint(c.Params("id"), 10, 64)
	if err != nil || n == 0 {
		return 0, fiber.NewError(fiber.StatusBadRequest, "invalid id")
	}
	return uint(n), nil
}

func source(c *fiber.Ctx) string {
	if c.Get(fiber.HeaderAuthorization) != "" {
		return "api"
	}
	return "panel"
}
