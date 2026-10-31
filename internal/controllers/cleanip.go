package controllers

import (
	"context"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gofiber/fiber/v2"

	"microserver/internal/models"
	"microserver/internal/services/cleanip"
	"microserver/internal/services/settings"
)

// ListCleanIPs returns the whole clean-IP list with its sources.
func (a *App) ListCleanIPs(c *fiber.Ctx) error {
	if err := cleanip.EnsureBuiltin(a.DB); err != nil {
		return err
	}
	var ips []models.CleanIP
	if err := a.DB.Order("operator, address").Find(&ips).Error; err != nil {
		return err
	}
	var srcs []models.CleanIPSource
	if err := a.DB.Order("builtin desc, id").Find(&srcs).Error; err != nil {
		return err
	}
	return c.JSON(fiber.Map{"ips": ips, "sources": srcs, "checking": cleanChecking.Load()})
}

// AddCleanIPs adds addresses by hand, one per line (optionally "addr,operator");
// the form's operator applies to lines without their own.
func (a *App) AddCleanIPs(c *fiber.Ctx) error {
	var in struct {
		Text     string `json:"text"`
		Operator string `json:"operator"`
	}
	if err := c.BodyParser(&in); err != nil {
		return errJSON(c, fiber.StatusBadRequest, "invalid body")
	}
	entries, err := cleanip.Parse([]byte(in.Text))
	if err != nil || len(entries) == 0 {
		return errJSON(c, fiber.StatusBadRequest, "هیچ IP یا دامنهٔ معتبری پیدا نشد")
	}
	added := 0
	for _, e := range entries {
		op := e.Operator
		if op == "" {
			op = strings.ToUpper(strings.TrimSpace(in.Operator))
		}
		row := models.CleanIP{Address: e.Address, Operator: op, Source: cleanip.Manual, Enabled: true, LastSeen: time.Now()}
		// An address that was fetched becomes manual, so a sync no longer removes it.
		res := a.DB.Where(models.CleanIP{Address: e.Address}).
			Assign(map[string]any{"operator": op, "source": cleanip.Manual, "enabled": true}).
			FirstOrCreate(&row)
		if res.Error != nil {
			return res.Error
		}
		added++
	}
	return c.JSON(fiber.Map{"added": added})
}

func (a *App) UpdateCleanIP(c *fiber.Ctx) error {
	id, err := idParam(c)
	if err != nil {
		return err
	}
	var in struct {
		Enabled  *bool   `json:"enabled"`
		Operator *string `json:"operator"`
	}
	if err := c.BodyParser(&in); err != nil {
		return errJSON(c, fiber.StatusBadRequest, "invalid body")
	}
	up := map[string]any{}
	if in.Enabled != nil {
		up["enabled"] = *in.Enabled
	}
	if in.Operator != nil {
		up["operator"] = strings.ToUpper(strings.TrimSpace(*in.Operator))
	}
	return a.DB.Model(&models.CleanIP{}).Where("id = ?", id).Updates(up).Error
}

func (a *App) DeleteCleanIP(c *fiber.Ctx) error {
	id, err := idParam(c)
	if err != nil {
		return err
	}
	return a.DB.Delete(&models.CleanIP{}, id).Error
}

// DeleteCleanIPs removes many at once: the given IDs, or every failed one.
func (a *App) DeleteCleanIPs(c *fiber.Ctx) error {
	var in struct {
		IDs    []uint `json:"ids"`
		Failed bool   `json:"failed"`
	}
	if err := c.BodyParser(&in); err != nil {
		return errJSON(c, fiber.StatusBadRequest, "invalid body")
	}
	q := a.DB
	switch {
	case in.Failed:
		q = q.Where("check_ok = ?", false)
	case len(in.IDs) > 0:
		q = q.Where("id IN ?", in.IDs)
	default:
		return errJSON(c, fiber.StatusBadRequest, "ids or failed is required")
	}
	res := q.Delete(&models.CleanIP{})
	if res.Error != nil {
		return res.Error
	}
	return c.JSON(fiber.Map{"deleted": res.RowsAffected})
}

func (a *App) cleanSyncer() *cleanip.Syncer {
	return &cleanip.Syncer{DB: a.DB, Operators: func() []string { return a.Settings.List(settings.CleanIPOperators) }}
}

// SyncCleanIPs fetches every enabled source now.
func (a *App) SyncCleanIPs(c *fiber.Ctx) error {
	ctx, cancel := context.WithTimeout(c.UserContext(), 2*time.Minute)
	defer cancel()
	err := a.cleanSyncer().Sync(ctx)
	var n int64
	a.DB.Model(&models.CleanIP{}).Count(&n)
	resp := fiber.Map{"count": n}
	if err != nil {
		resp["warning"] = err.Error()
	}
	return c.JSON(resp)
}

func (a *App) AddCleanIPSource(c *fiber.Ctx) error {
	var in struct {
		URL string `json:"url"`
	}
	if err := c.BodyParser(&in); err != nil {
		return errJSON(c, fiber.StatusBadRequest, "invalid body")
	}
	u := strings.TrimSpace(in.URL)
	if !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") {
		return errJSON(c, fiber.StatusBadRequest, "لینک باید با http:// یا https:// شروع شود")
	}
	src := models.CleanIPSource{URL: u, Enabled: true}
	if err := a.DB.Create(&src).Error; err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(c.UserContext(), time.Minute)
	defer cancel()
	if err := a.cleanSyncer().SyncSource(ctx, src.ID); err != nil {
		return errJSON(c, fiber.StatusBadRequest, "لینک اضافه شد ولی خوانده نشد: "+err.Error())
	}
	a.DB.First(&src, src.ID)
	return c.JSON(src)
}

func (a *App) UpdateCleanIPSource(c *fiber.Ctx) error {
	id, err := idParam(c)
	if err != nil {
		return err
	}
	var in struct {
		Enabled *bool `json:"enabled"`
	}
	if err := c.BodyParser(&in); err != nil || in.Enabled == nil {
		return errJSON(c, fiber.StatusBadRequest, "enabled is required")
	}
	return a.DB.Model(&models.CleanIPSource{}).Where("id = ?", id).Update("enabled", *in.Enabled).Error
}

// DeleteCleanIPSource removes a custom source and the addresses it brought.
func (a *App) DeleteCleanIPSource(c *fiber.Ctx) error {
	id, err := idParam(c)
	if err != nil {
		return err
	}
	var src models.CleanIPSource
	if err := a.DB.First(&src, id).Error; err != nil {
		return errJSON(c, fiber.StatusNotFound, "source not found")
	}
	if src.Builtin {
		return errJSON(c, fiber.StatusBadRequest, "لیست داخلی حذف نمی‌شود؛ خاموشش کنید")
	}
	a.DB.Where("source = ?", cleanip.SourceKey(src.ID)).Delete(&models.CleanIP{})
	return a.DB.Delete(&src).Error
}

// cleanChecking is set while a test from Iran runs; one at a time.
var cleanChecking atomic.Bool

// CheckCleanIPs tests addresses on port 443 from Iranian probes (check-host,
// Globalping) in the background. Each test spends Globalping credits.
func (a *App) CheckCleanIPs(c *fiber.Ctx) error {
	var in struct {
		IDs []uint `json:"ids"`
	}
	if err := c.BodyParser(&in); err != nil {
		return errJSON(c, fiber.StatusBadRequest, "invalid body")
	}
	var rows []models.CleanIP
	q := a.DB.Where("enabled = ?", true)
	if len(in.IDs) > 0 {
		q = a.DB.Where("id IN ?", in.IDs)
	}
	if err := q.Order("id").Find(&rows).Error; err != nil {
		return err
	}
	if len(rows) == 0 {
		return errJSON(c, fiber.StatusBadRequest, "آدرسی برای تست نیست")
	}
	if !cleanChecking.CompareAndSwap(false, true) {
		return errJSON(c, fiber.StatusConflict, "یک تست دیگر در حال اجراست")
	}
	go func() {
		defer cleanChecking.Store(false)
		sem := make(chan struct{}, 2)
		var wg sync.WaitGroup
		for i := range rows {
			sem <- struct{}{}
			wg.Add(1)
			go func(r *models.CleanIP) {
				defer func() { <-sem; wg.Done() }()
				res := a.Orch.Check(context.Background(), r.Address, 443)
				now := time.Now()
				ok := res.OK
				note := res.Summary
				if len(note) > 250 {
					note = note[:250]
				}
				a.DB.Model(&models.CleanIP{}).Where("id = ?", r.ID).
					Updates(map[string]any{"checked_at": &now, "check_ok": &ok, "check_note": note})
			}(&rows[i])
		}
		wg.Wait()
		log.Printf("clean IPs: tested %d addresses from Iran", len(rows))
	}()
	return c.JSON(fiber.Map{"started": len(rows)})
}
