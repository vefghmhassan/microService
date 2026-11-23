package controllers

import (
	"context"
	"errors"
	"time"

	"github.com/gofiber/fiber/v2"

	"microserver/internal/models"
	"microserver/internal/services/orchestrator"
)

// NodesOverview is the node page's data: the master panel's server, the
// nodes as the master sees them and the balancer configs in vpn-pannel.
func (a *App) NodesOverview(c *fiber.Ctx) error {
	out := fiber.Map{"master": nil, "nodes": []any{}, "uploads": []any{}, "error": ""}
	var uploads []models.NodeUpload
	a.DB.Order("location_id, destination_id").Find(&uploads)
	out["uploads"] = uploads
	m, err := a.Orch.MasterServer()
	if err != nil {
		if !errors.Is(err, orchestrator.ErrNoMaster) {
			out["error"] = err.Error()
		}
		// A master that is still being built is shown too.
		var building models.Server
		if a.DB.Where("role = ? AND status NOT IN ?", models.RoleMaster,
			[]string{models.ServerDeleted, models.ServerDeleting}).Order("id DESC").First(&building).Error == nil {
			out["master"] = building
		}
		return c.JSON(out)
	}
	out["master"] = m
	ctx, cancel := context.WithTimeout(c.Context(), 25*time.Second)
	defer cancel()
	nodes, err := a.Orch.Nodes(ctx)
	if err != nil {
		out["error"] = err.Error()
		return c.JSON(out)
	}
	out["nodes"] = nodes
	return c.JSON(out)
}

// CreateMaster builds the master panel's server at a provider.
func (a *App) CreateMaster(c *fiber.Ctx) error {
	var p orchestrator.ProvisionParams
	if err := c.BodyParser(&p); err != nil {
		return errJSON(c, fiber.StatusBadRequest, "invalid body")
	}
	var n int64
	a.DB.Model(&models.Server{}).Where("role = ? AND status NOT IN ?", models.RoleMaster,
		[]string{models.ServerDeleted, models.ServerDeleting}).Count(&n)
	if n > 0 {
		return errJSON(c, fiber.StatusConflict, "یک پنل مادر از قبل وجود دارد")
	}
	var acc models.ProviderAccount
	if err := a.DB.First(&acc, p.AccountID).Error; err != nil {
		return errJSON(c, fiber.StatusBadRequest, "provider account not found")
	}
	if !acc.Enabled {
		return errJSON(c, fiber.StatusBadRequest, "این حساب غیرفعال است")
	}
	p.Role, p.IPFamily, p.Upload = models.RoleMaster, models.FamilyV4, nil
	if p.Name == "" {
		p.Name = "ms-master"
	}
	j, err := a.Engine.Enqueue(orchestrator.KindProvision, nil, p, source(c))
	if err != nil {
		return err
	}
	return c.Status(fiber.StatusAccepted).JSON(j)
}
