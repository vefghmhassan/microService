package controllers

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"microserver/internal/models"
	"microserver/internal/services/providers"
	"microserver/internal/services/secret"
	"microserver/internal/services/settings"
)

type providerView struct {
	models.ProviderAccount
	Credentials  map[string]string      `json:"credentials"`
	Capabilities providers.Capabilities `json:"capabilities"`
	ServerCount  int64                  `json:"server_count"`
}

func descriptor(typ string) (providers.Descriptor, bool) {
	for _, d := range providers.Descriptors() {
		if d.Type == typ {
			return d, true
		}
	}
	return providers.Descriptor{}, false
}

// maskedCreds shows non-secret credential fields and masks secret ones.
func maskedCreds(acc *models.ProviderAccount) map[string]string {
	out := map[string]string{}
	var raw map[string]string
	_ = json.Unmarshal([]byte(acc.Credentials), &raw)
	d, _ := descriptor(acc.Type)
	for _, f := range d.Fields {
		v := raw[f.Key]
		if f.Secret && v != "" {
			v = settings.Mask
		}
		out[f.Key] = v
	}
	return out
}

func (a *App) view(acc *models.ProviderAccount) providerView {
	v := providerView{ProviderAccount: *acc, Credentials: maskedCreds(acc)}
	if d, ok := descriptor(acc.Type); ok {
		v.Capabilities = d.Capabilities
	}
	a.DB.Model(&models.Server{}).Where("provider_account_id = ? AND status <> ?", acc.ID, models.ServerDeleted).Count(&v.ServerCount)
	return v
}

func (a *App) ProviderTypes(c *fiber.Ctx) error {
	return c.JSON(providers.Descriptors())
}

func (a *App) ListProviders(c *fiber.Ctx) error {
	var accs []models.ProviderAccount
	if err := a.DB.Order("id").Find(&accs).Error; err != nil {
		return err
	}
	out := make([]providerView, 0, len(accs))
	for i := range accs {
		out = append(out, a.view(&accs[i]))
	}
	return c.JSON(out)
}

type providerInput struct {
	Name          string            `json:"name"`
	Type          string            `json:"type"`
	Credentials   map[string]string `json:"credentials"`
	DefaultRegion string            `json:"default_region"`
	DefaultPlan   string            `json:"default_plan"`
	DefaultImage  string            `json:"default_image"`
	Country       string            `json:"country"`
	Enabled       *bool             `json:"enabled"`
}

func (a *App) saveProvider(c *fiber.Ctx, acc *models.ProviderAccount) error {
	var in providerInput
	if err := c.BodyParser(&in); err != nil {
		return errJSON(c, fiber.StatusBadRequest, "invalid body")
	}
	if acc.ID == 0 {
		if _, ok := descriptor(in.Type); !ok {
			return errJSON(c, fiber.StatusBadRequest, "unknown provider type")
		}
		acc.Type = in.Type
		acc.Enabled = true
	}
	if strings.TrimSpace(in.Name) != "" {
		acc.Name = strings.TrimSpace(in.Name)
	}
	if acc.Name == "" {
		return errJSON(c, fiber.StatusBadRequest, "name is required")
	}
	acc.DefaultRegion, acc.DefaultPlan, acc.DefaultImage = in.DefaultRegion, in.DefaultPlan, in.DefaultImage
	acc.Country = strings.ToUpper(strings.TrimSpace(in.Country))
	if in.Enabled != nil {
		acc.Enabled = *in.Enabled
	}

	// Merge credentials: a masked or empty secret keeps the stored value.
	creds := map[string]string{}
	_ = json.Unmarshal([]byte(acc.Credentials), &creds)
	for k, v := range in.Credentials {
		if v == settings.Mask || (v == "" && creds[k] != "") {
			continue
		}
		creds[k] = strings.TrimSpace(v)
	}
	b, _ := json.Marshal(creds)
	acc.Credentials = secret.String(b)

	p, err := providers.New(acc.Type, json.RawMessage(b))
	if err != nil {
		return errJSON(c, fiber.StatusBadRequest, err.Error())
	}
	ctx, cancel := context.WithTimeout(c.UserContext(), 20*time.Second)
	defer cancel()
	if err := p.Validate(ctx); err != nil {
		return errJSON(c, fiber.StatusBadRequest, "اعتبارسنجی API ناموفق بود: "+err.Error())
	}
	if err := a.DB.Save(acc).Error; err != nil {
		return err
	}
	return c.JSON(a.view(acc))
}

func (a *App) CreateProvider(c *fiber.Ctx) error {
	return a.saveProvider(c, &models.ProviderAccount{})
}

func (a *App) UpdateProvider(c *fiber.Ctx) error {
	acc, err := a.account(c)
	if err != nil {
		return err
	}
	return a.saveProvider(c, acc)
}

func (a *App) DeleteProvider(c *fiber.Ctx) error {
	acc, err := a.account(c)
	if err != nil {
		return err
	}
	var n int64
	a.DB.Model(&models.Server{}).Where("provider_account_id = ? AND status <> ?", acc.ID, models.ServerDeleted).Count(&n)
	if n > 0 {
		return errJSON(c, fiber.StatusConflict, "ابتدا سرورهای این حساب را حذف کنید")
	}
	return a.DB.Delete(acc).Error
}

func (a *App) account(c *fiber.Ctx) (*models.ProviderAccount, error) {
	id, err := idParam(c)
	if err != nil {
		return nil, err
	}
	var acc models.ProviderAccount
	if err := a.DB.First(&acc, id).Error; err != nil {
		return nil, fiber.NewError(fiber.StatusNotFound, "provider account not found")
	}
	return &acc, nil
}

func (a *App) providerFor(c *fiber.Ctx) (providers.Provider, error) {
	acc, err := a.account(c)
	if err != nil {
		return nil, err
	}
	p, err := providers.New(acc.Type, json.RawMessage(acc.Credentials))
	if err != nil {
		return nil, fiber.NewError(fiber.StatusBadRequest, err.Error())
	}
	return p, nil
}

func (a *App) TestProvider(c *fiber.Ctx) error {
	p, err := a.providerFor(c)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(c.UserContext(), 20*time.Second)
	defer cancel()
	if err := p.Validate(ctx); err != nil {
		return errJSON(c, fiber.StatusBadGateway, err.Error())
	}
	return c.JSON(fiber.Map{"ok": true})
}

func (a *App) ProviderRegions(c *fiber.Ctx) error {
	p, err := a.providerFor(c)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(c.UserContext(), 30*time.Second)
	defer cancel()
	opts, err := p.Regions(ctx)
	if err != nil {
		return errJSON(c, fiber.StatusBadGateway, err.Error())
	}
	return c.JSON(opts)
}

func (a *App) ProviderPlans(c *fiber.Ctx) error {
	p, err := a.providerFor(c)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(c.UserContext(), 30*time.Second)
	defer cancel()
	opts, err := p.Plans(ctx, c.Query("region"))
	if err != nil {
		return errJSON(c, fiber.StatusBadGateway, err.Error())
	}
	return c.JSON(opts)
}
