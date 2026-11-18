package controllers

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"

	"microserver/internal/models"
	"microserver/internal/services/orchestrator"
	"microserver/internal/services/settings"
)

// Balancer modes: the balancer configs the admin keeps (all-auto, fr-auto,
// …) and the node-only switch. They are what vpn-pannel and the JSON
// subscription get.

var modeNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,59}$`)

// minModeProbe is the shortest probe interval a mode may ask for.
const minModeProbe = 10 * time.Second

func (a *App) modesPayload(c *fiber.Ctx) error {
	modes := []models.BalancerMode{}
	if err := a.DB.Order("priority, id").Find(&modes).Error; err != nil {
		return err
	}
	uploads := []models.NodeUpload{}
	a.DB.Order("mode_id, destination_id, ads").Find(&uploads)
	return c.JSON(fiber.Map{"only": a.Settings.Bool(settings.NodeOnly), "modes": modes, "uploads": uploads})
}

// ListModes returns the modes in order, their configs in vpn-pannel and the
// node-only switch.
func (a *App) ListModes(c *fiber.Ctx) error {
	return a.modesPayload(c)
}

// saveMode reads the body over m, so a PUT with only some fields (the
// enable switch) keeps the rest.
func (a *App) saveMode(c *fiber.Ctx, m *models.BalancerMode) error {
	id, created, prio := m.ID, m.CreatedAt, m.Priority
	if err := c.BodyParser(m); err != nil {
		return errJSON(c, fiber.StatusBadRequest, "invalid body")
	}
	m.ID, m.CreatedAt, m.Priority = id, created, prio
	m.Name = strings.TrimSpace(m.Name)
	if !modeNameRe.MatchString(m.Name) {
		return errJSON(c, fiber.StatusBadRequest, "نام فقط حروف انگلیسی، عدد، خط تیره، نقطه و زیرخط (حداکثر ۶۰ نویسه)")
	}
	// The ads copy of a mode is named <name>-ads; no mode may take the name
	// another one's copy has.
	var others []models.BalancerMode
	a.DB.Where("id <> ?", m.ID).Find(&others)
	for _, o := range others {
		switch {
		case strings.EqualFold(o.Name, m.Name):
			return errJSON(c, fiber.StatusConflict, "حالتی با این نام وجود دارد")
		case strings.EqualFold(o.Name+"-ads", m.Name), strings.EqualFold(m.Name+"-ads", o.Name):
			return errJSON(c, fiber.StatusConflict, "این نام با نسخه تبلیغاتی حالت «"+o.Name+"» یکی می‌شود")
		}
	}
	switch m.Strategy {
	case "", "leastPing", "leastLoad", "random", "roundRobin":
	default:
		return errJSON(c, fiber.StatusBadRequest, "استراتژی balancer معتبر نیست")
	}
	switch m.AdsMode {
	case "", models.AdsNormal, models.AdsOnly, models.AdsBoth:
	default:
		return errJSON(c, fiber.StatusBadRequest, "حالت تبلیغاتی معتبر نیست")
	}
	m.ProbeInterval = strings.TrimSpace(m.ProbeInterval)
	if m.ProbeInterval != "" {
		d, err := time.ParseDuration(m.ProbeInterval)
		if err != nil || d < minModeProbe {
			return errJSON(c, fiber.StatusBadRequest, "فاصله تست باید مثل 30s یا 1m و دست‌کم ۱۰ ثانیه باشد")
		}
	}
	m.Country = strings.ToUpper(strings.TrimSpace(m.Country))
	if len(m.Country) != 0 && len(m.Country) != 2 {
		return errJSON(c, fiber.StatusBadRequest, "کشور باید کد دوحرفی باشد")
	}
	ids := orchestrator.ModeLocationIDs(m.LocationIDs)
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		var n int64
		a.DB.Model(&models.Location{}).Where("id = ?", id).Count(&n)
		if n == 0 {
			return errJSON(c, fiber.StatusBadRequest, "لوکیشن "+strconv.FormatUint(uint64(id), 10)+" پیدا نشد")
		}
		parts = append(parts, strconv.FormatUint(uint64(id), 10))
	}
	m.LocationIDs = strings.Join(parts, ",")
	if m.ID == 0 {
		var last models.BalancerMode
		a.DB.Order("priority DESC").First(&last)
		m.Priority = last.Priority + 1
	}
	if err := a.DB.Save(m).Error; err != nil {
		return err
	}
	a.Orch.QueueNodeSync()
	return c.JSON(m)
}

func (a *App) mode(c *fiber.Ctx) (*models.BalancerMode, error) {
	id, err := idParam(c)
	if err != nil {
		return nil, err
	}
	var m models.BalancerMode
	if err := a.DB.First(&m, id).Error; err != nil {
		return nil, fiber.NewError(fiber.StatusNotFound, "حالت پیدا نشد")
	}
	return &m, nil
}

func (a *App) CreateMode(c *fiber.Ctx) error {
	return a.saveMode(c, &models.BalancerMode{Enabled: true})
}

func (a *App) UpdateMode(c *fiber.Ctx) error {
	m, err := a.mode(c)
	if err != nil {
		return err
	}
	return a.saveMode(c, m)
}

// DeleteMode removes a mode; the node sync it queues takes its configs out
// of vpn-pannel.
func (a *App) DeleteMode(c *fiber.Ctx) error {
	m, err := a.mode(c)
	if err != nil {
		return err
	}
	if err := a.DB.Delete(m).Error; err != nil {
		return err
	}
	a.Orch.QueueNodeSync()
	return c.SendStatus(fiber.StatusNoContent)
}

// ReorderModes stores the order of the modes, which is the order of the
// balancer configs in the JSON subscription.
func (a *App) ReorderModes(c *fiber.Ctx) error {
	var in struct {
		IDs []uint `json:"ids"`
	}
	if err := c.BodyParser(&in); err != nil || len(in.IDs) == 0 {
		return errJSON(c, fiber.StatusBadRequest, "ids required")
	}
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		for i, id := range in.IDs {
			if err := tx.Model(&models.BalancerMode{}).Where("id = ?", id).Update("priority", i+1).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return a.modesPayload(c)
}

// SetNodeOnly switches node-only mode and queues the job that takes the
// servers' own configs out of vpn-pannel, or puts them back.
func (a *App) SetNodeOnly(c *fiber.Ctx) error {
	var in struct {
		Enabled bool `json:"enabled"`
	}
	if err := c.BodyParser(&in); err != nil {
		return errJSON(c, fiber.StatusBadRequest, "invalid body")
	}
	if in.Enabled == a.Settings.Bool(settings.NodeOnly) {
		return errJSON(c, fiber.StatusConflict, "همین حالا در همین وضعیت است")
	}
	if in.Enabled {
		var n int64
		a.DB.Model(&models.BalancerMode{}).Where("enabled = ?", true).Count(&n)
		if n == 0 {
			return errJSON(c, fiber.StatusBadRequest, "اول دست‌کم یک حالت روشن بسازید؛ وگرنه پنل مقصد خالی می‌ماند")
		}
	}
	if err := a.Settings.Set(map[string]string{settings.NodeOnly: strconv.FormatBool(in.Enabled)}); err != nil {
		return err
	}
	j, err := a.Engine.Enqueue(orchestrator.KindNodeOnly, nil, struct{}{}, source(c))
	if err != nil {
		return err
	}
	return c.Status(fiber.StatusAccepted).JSON(j)
}
