package controllers

import (
	"strings"

	"github.com/gofiber/fiber/v2"

	"microserver/internal/models"
	"microserver/internal/services/orchestrator"
	"microserver/internal/services/smart"
)

// ---- smart profiles ----

func (a *App) ListSmartProfiles(c *fiber.Ctx) error {
	list := []models.SmartProfile{}
	if err := a.DB.Order("id").Find(&list).Error; err != nil {
		return err
	}
	return c.JSON(list)
}

// saveSmartProfile reads the body over p, so a PUT with only some fields
// (the enable switch) keeps the rest.
func (a *App) saveSmartProfile(c *fiber.Ctx, p *models.SmartProfile) error {
	id, created := p.ID, p.CreatedAt
	if err := c.BodyParser(p); err != nil {
		return errJSON(c, fiber.StatusBadRequest, "invalid body")
	}
	p.ID, p.CreatedAt = id, created
	smart.Normalize(p)
	if err := smart.Validate(p); err != nil {
		return errJSON(c, fiber.StatusBadRequest, err.Error())
	}
	if err := a.DB.Save(p).Error; err != nil {
		return err
	}
	a.Orch.QueueSmartSync(true)
	return c.JSON(p)
}

func (a *App) smartProfile(c *fiber.Ctx) (*models.SmartProfile, error) {
	id, err := idParam(c)
	if err != nil {
		return nil, err
	}
	var p models.SmartProfile
	if err := a.DB.First(&p, id).Error; err != nil {
		return nil, fiber.NewError(fiber.StatusNotFound, "پروفایل پیدا نشد")
	}
	return &p, nil
}

func (a *App) CreateSmartProfile(c *fiber.Ctx) error {
	return a.saveSmartProfile(c, &models.SmartProfile{Enabled: true})
}

func (a *App) UpdateSmartProfile(c *fiber.Ctx) error {
	p, err := a.smartProfile(c)
	if err != nil {
		return err
	}
	return a.saveSmartProfile(c, p)
}

// DeleteSmartProfile removes the profile; the next smart sync deletes its
// nodes from vpn-pannel (their rows stay until then).
func (a *App) DeleteSmartProfile(c *fiber.Ctx) error {
	p, err := a.smartProfile(c)
	if err != nil {
		return err
	}
	if err := a.DB.Delete(p).Error; err != nil {
		return err
	}
	a.Orch.QueueSmartSync(true)
	return c.JSON(fiber.Map{"ok": true})
}

// PreviewSmartProfile builds the profile's configs from the body (unsaved
// form) or the stored profile, without uploading.
func (a *App) PreviewSmartProfile(c *fiber.Ctx) error {
	var p models.SmartProfile
	if len(c.Body()) > 0 {
		if err := c.BodyParser(&p); err != nil {
			return errJSON(c, fiber.StatusBadRequest, "invalid body")
		}
	} else if sp, err := a.smartProfile(c); err == nil {
		p = *sp
	} else {
		return err
	}
	smart.Normalize(&p)
	if err := smart.Validate(&p); err != nil {
		return errJSON(c, fiber.StatusBadRequest, err.Error())
	}
	out, err := a.Orch.PreviewSmart(&p, uint(c.QueryInt("server_id")))
	if err != nil {
		return err
	}
	if out == nil {
		out = []orchestrator.SmartPreview{}
	}
	return c.JSON(out)
}

// SyncSmart queues a smart sync now.
func (a *App) SyncSmart(c *fiber.Ctx) error {
	j, err := a.Engine.Enqueue(orchestrator.KindSmartSync, nil, orchestrator.SmartSyncParams{Verify: true}, "web")
	if err != nil {
		return err
	}
	return c.JSON(j)
}

// smartUploadView is a pushed node with its profile and server names.
type smartUploadView struct {
	models.SmartUpload
	Profile     string `json:"profile"`
	Server      string `json:"server"`
	Destination string `json:"destination"`
}

func (a *App) ListSmartUploads(c *fiber.Ctx) error {
	var rows []models.SmartUpload
	if err := a.DB.Order("profile_id, server_id, destination_id, ads").Find(&rows).Error; err != nil {
		return err
	}
	names := func(model any) map[uint]string {
		var list []struct {
			ID   uint
			Name string
		}
		a.DB.Model(model).Select("id, name").Find(&list)
		m := map[uint]string{}
		for _, x := range list {
			m[x.ID] = x.Name
		}
		return m
	}
	prof, srv, dst := names(&models.SmartProfile{}), names(&models.Server{}), names(&models.Destination{})
	out := make([]smartUploadView, 0, len(rows))
	for _, r := range rows {
		out = append(out, smartUploadView{SmartUpload: r, Profile: prof[r.ProfileID], Server: srv[r.ServerID], Destination: dst[r.DestinationID]})
	}
	return c.JSON(out)
}

// ---- smart rules ----

func (a *App) ListSmartRules(c *fiber.Ctx) error {
	list := []models.SmartRule{}
	if err := a.DB.Order("sort_order, id").Find(&list).Error; err != nil {
		return err
	}
	return c.JSON(list)
}

func (a *App) saveSmartRule(c *fiber.Ctx, r *models.SmartRule) error {
	id, key, builtin, created := r.ID, r.Key, r.Builtin, r.CreatedAt
	if err := c.BodyParser(r); err != nil {
		return errJSON(c, fiber.StatusBadRequest, "invalid body")
	}
	r.ID, r.Key, r.Builtin, r.CreatedAt = id, key, builtin, created
	r.Name = strings.TrimSpace(r.Name)
	if r.Name == "" {
		return errJSON(c, fiber.StatusBadRequest, "نام قانون لازم است")
	}
	r.Network = strings.ToLower(strings.ReplaceAll(r.Network, " ", ""))
	switch r.Network {
	case "", "tcp", "udp", "tcp,udp":
	default:
		return errJSON(c, fiber.StatusBadRequest, "شبکه باید tcp، udp یا tcp,udp باشد")
	}
	if len(smart.Lines(r.Domains)) == 0 && len(smart.Lines(r.IPs)) == 0 && strings.TrimSpace(r.Ports) == "" {
		return errJSON(c, fiber.StatusBadRequest, "حداقل یک دامنه، IP یا پورت لازم است")
	}
	if err := a.DB.Save(r).Error; err != nil {
		return err
	}
	a.Orch.QueueSmartSync(false)
	return c.JSON(r)
}

func (a *App) smartRule(c *fiber.Ctx) (*models.SmartRule, error) {
	id, err := idParam(c)
	if err != nil {
		return nil, err
	}
	var r models.SmartRule
	if err := a.DB.First(&r, id).Error; err != nil {
		return nil, fiber.NewError(fiber.StatusNotFound, "قانون پیدا نشد")
	}
	return &r, nil
}

func (a *App) CreateSmartRule(c *fiber.Ctx) error {
	var last struct{ N int }
	a.DB.Model(&models.SmartRule{}).Select("COALESCE(MAX(sort_order), 0) AS n").Scan(&last)
	return a.saveSmartRule(c, &models.SmartRule{Enabled: true, Order: last.N + 10})
}

func (a *App) UpdateSmartRule(c *fiber.Ctx) error {
	r, err := a.smartRule(c)
	if err != nil {
		return err
	}
	return a.saveSmartRule(c, r)
}

func (a *App) DeleteSmartRule(c *fiber.Ctx) error {
	r, err := a.smartRule(c)
	if err != nil {
		return err
	}
	if r.Builtin {
		return errJSON(c, fiber.StatusConflict, "قانون‌های پیش‌فرض حذف نمی‌شوند؛ فقط خاموششان کنید")
	}
	if err := a.DB.Delete(r).Error; err != nil {
		return err
	}
	a.Orch.QueueSmartSync(false)
	return c.JSON(fiber.Map{"ok": true})
}
