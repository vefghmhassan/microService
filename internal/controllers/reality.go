package controllers

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"microserver/internal/models"
	"microserver/internal/services/xui"
)

// realityScanner is the part of *xui.Client the Reality scanner needs; the
// probe runs from the server, which is what a Reality inbound dials from.
type realityScanner interface {
	ScanRealityTarget(ctx context.Context, target, sni string, xver int) (*xui.RealityScan, error)
	ScanRealityTargets(ctx context.Context, targets string) ([]xui.RealityScan, error)
}

type realityScanIn struct {
	ServerID int    `json:"server_id"`
	Target   string `json:"target"`
	SNI      string `json:"sni"`
	Xver     int    `json:"xver"`
	Targets  string `json:"targets"`
}

// ScanReality checks the inbound form's current Reality target from a server.
func (a *App) ScanReality(c *fiber.Ctx) error {
	return a.realityScan(c, 30*time.Second, func(ctx context.Context, p realityScanner, in realityScanIn) (any, error) {
		if strings.TrimSpace(in.Target) == "" {
			return nil, fiber.NewError(fiber.StatusBadRequest, "هدف (target) خالی است")
		}
		return p.ScanRealityTarget(ctx, strings.TrimSpace(in.Target), strings.TrimSpace(in.SNI), in.Xver)
	})
}

// ScanRealityMany scans candidate targets (hosts, IPs or CIDRs; empty =
// 3x-ui's built-in list) from a server, best first.
func (a *App) ScanRealityMany(c *fiber.Ctx) error {
	return a.realityScan(c, 120*time.Second, func(ctx context.Context, p realityScanner, in realityScanIn) (any, error) {
		return p.ScanRealityTargets(ctx, strings.TrimSpace(in.Targets))
	})
}

// scanServers lists the servers a scan may run from: a server in error still
// counts, since its 3x-ui panel often answers. Active and filtered come first.
func (a *App) scanServers(id int) ([]models.Server, error) {
	var list []models.Server
	q := a.DB.Preload("ProviderAccount").
		Where("status IN ? AND xui_port > 0 AND ipv4 <> ''", []string{models.ServerActive, models.ServerFiltered, models.ServerError})
	if id > 0 {
		q = q.Where("id = ?", id)
	}
	if err := q.Order("CASE WHEN status = 'error' THEN 1 ELSE 0 END, id").Find(&list).Error; err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, fiber.NewError(fiber.StatusConflict, "هیچ سروری با 3x-ui پیدا نشد")
	}
	return list, nil
}

func (a *App) realityScan(c *fiber.Ctx, timeout time.Duration, run func(context.Context, realityScanner, realityScanIn) (any, error)) error {
	var in realityScanIn
	if err := c.BodyParser(&in); err != nil {
		return errJSON(c, fiber.StatusBadRequest, "invalid body")
	}
	list, err := a.scanServers(in.ServerID)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(c.UserContext(), timeout)
	defer cancel()
	// Without a chosen server, the first panel that answers runs the scan.
	var failed []string
	for i := range list {
		s := &list[i]
		res, err := a.scanFrom(ctx, s, in, run)
		var fe *fiber.Error
		switch {
		case err == nil:
			return c.JSON(fiber.Map{"server": s.Name, "result": res})
		case errors.As(err, &fe):
			return err
		case errors.Is(err, xui.ErrNotFound):
			failed = append(failed, s.Name+": اسکنر Reality از 3x-ui نسخه 3.7 به بعد هست؛ پنل را به‌روز کنید")
		default:
			failed = append(failed, s.Name+": "+err.Error())
		}
		if ctx.Err() != nil {
			break
		}
	}
	return errJSON(c, fiber.StatusBadGateway, strings.Join(failed, "\n"))
}

func (a *App) scanFrom(ctx context.Context, s *models.Server, in realityScanIn, run func(context.Context, realityScanner, realityScanIn) (any, error)) (any, error) {
	// A dead server (often one in error) must fail fast so the next one gets a turn.
	dialCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	remote, panel, err := a.Orch.RemoteFor(dialCtx, s)
	if err != nil {
		return nil, err
	}
	defer remote.Close()
	p, ok := panel.(realityScanner)
	if !ok {
		return nil, fiber.NewError(fiber.StatusNotImplemented, "این پنل اسکنر Reality ندارد")
	}
	return run(ctx, p, in)
}
