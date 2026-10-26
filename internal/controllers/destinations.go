package controllers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"

	"microserver/internal/models"
	"microserver/internal/services/destination"
	"microserver/internal/services/orchestrator"
	"microserver/internal/services/secret"
	"microserver/internal/services/settings"
)

// destinationView hides the password; HasPassword tells the form whether
// leaving it empty keeps the stored one.
type destinationView struct {
	models.Destination
	HasPassword bool `json:"has_password"`
}

func (a *App) ListDestinations(c *fiber.Ctx) error {
	var rows []models.Destination
	if err := a.DB.Order("id").Find(&rows).Error; err != nil {
		return err
	}
	out := make([]destinationView, len(rows))
	for i, d := range rows {
		d.AdsMode = d.Mode()
		out[i] = destinationView{Destination: d, HasPassword: d.Password != ""}
	}
	return c.JSON(out)
}

func (a *App) CreateDestination(c *fiber.Ctx) error {
	return a.saveDestination(c, &models.Destination{Enabled: true})
}

func (a *App) UpdateDestination(c *fiber.Ctx) error {
	d, err := a.destination(c)
	if err != nil {
		return err
	}
	return a.saveDestination(c, d)
}

func (a *App) DeleteDestination(c *fiber.Ctx) error {
	d, err := a.destination(c)
	if err != nil {
		return err
	}
	return a.DB.Delete(d).Error
}

// TestDestination logs in to one vpn-pannel with its stored credentials.
func (a *App) TestDestination(c *fiber.Ctx) error {
	d, err := a.destination(c)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(c.UserContext(), 20*time.Second)
	defer cancel()
	if err := destination.NewVPNPanel(d.URL, d.Email, string(d.Password)).Test(ctx); err != nil {
		return errJSON(c, fiber.StatusBadGateway, err.Error())
	}
	return c.JSON(fiber.Map{"ok": true})
}

func (a *App) destination(c *fiber.Ctx) (*models.Destination, error) {
	id, err := idParam(c)
	if err != nil {
		return nil, err
	}
	var d models.Destination
	if err := a.DB.First(&d, id).Error; err != nil {
		return nil, fiber.NewError(fiber.StatusNotFound, "destination not found")
	}
	return &d, nil
}

func (a *App) saveDestination(c *fiber.Ctx, d *models.Destination) error {
	var in struct {
		Name     string `json:"name"`
		URL      string `json:"url"`
		Email    string `json:"email"`
		Password string `json:"password"`
		AdsMode  string `json:"ads_mode"`
		Enabled  *bool  `json:"enabled"`
	}
	if err := c.BodyParser(&in); err != nil {
		return errJSON(c, fiber.StatusBadRequest, "invalid body")
	}
	if s := strings.TrimSpace(in.Name); s != "" {
		d.Name = s
	}
	if s := strings.TrimRight(strings.TrimSpace(in.URL), "/"); s != "" {
		if !strings.HasPrefix(s, "http://") && !strings.HasPrefix(s, "https://") {
			return errJSON(c, fiber.StatusBadRequest, "آدرس باید با http:// یا https:// شروع شود")
		}
		d.URL = s
	}
	if in.Email != "" || d.ID == 0 {
		d.Email = strings.TrimSpace(in.Email)
	}
	if in.Password != "" {
		d.Password = secret.String(in.Password)
	}
	switch in.AdsMode {
	case "":
	case models.AdsNormal, models.AdsOnly, models.AdsBoth:
		d.AdsMode = in.AdsMode
	default:
		return errJSON(c, fiber.StatusBadRequest, "حالت تبلیغ باید normal، ads یا both باشد")
	}
	d.AdsMode = d.Mode()
	if in.Enabled != nil {
		d.Enabled = *in.Enabled
	}
	if d.Name == "" || d.URL == "" {
		return errJSON(c, fiber.StatusBadRequest, "نام و آدرس لازم است")
	}
	if err := a.DB.Save(d).Error; err != nil {
		return err
	}
	return c.JSON(destinationView{Destination: *d, HasPassword: d.Password != ""})
}

// MigrateLegacyDestination turns the single vpn-pannel of older versions,
// kept in settings, into the first destination row.
func MigrateLegacyDestination(db *gorm.DB, st *settings.Store) error {
	db.Exec("UPDATE destinations SET ads_mode = CASE WHEN ads THEN ? ELSE ? END WHERE ads_mode IS NULL OR ads_mode = ''", models.AdsOnly, models.AdsNormal)
	var n int64
	db.Model(&models.Destination{}).Count(&n)
	u := st.Get(settings.VPNPanelURL)
	if n > 0 || u == "" {
		return nil
	}
	mode := models.AdsNormal
	if st.Bool(settings.VPNPanelAds) {
		mode = models.AdsOnly
	}
	d := models.Destination{
		Name: "vpn-pannel", URL: strings.TrimRight(u, "/"), Email: st.Get(settings.VPNPanelEmail),
		Password: secret.String(st.Get(settings.VPNPanelPassword)), AdsMode: mode, Enabled: true,
	}
	return db.Create(&d).Error
}

// uploadToDestinations sends configs to the chosen destinations (all enabled
// ones when ids is empty) and reports per panel.
func (a *App) uploadToDestinations(ctx context.Context, ids []uint, req destination.UploadRequest, adsOverride *bool) ([]fiber.Map, error) {
	q := a.DB.Where("enabled = ?", true)
	if len(ids) > 0 {
		q = a.DB.Where("id IN ?", ids)
	}
	var rows []models.Destination
	q.Order("id").Find(&rows)
	if len(rows) == 0 {
		return nil, errors.New("هیچ پنل مقصدی تنظیم یا انتخاب نشده است")
	}
	var out []fiber.Map
	ok := 0
	for _, d := range rows {
		ads := d.AdsValues()
		if adsOverride != nil {
			ads = []bool{*adsOverride}
		}
		v := destination.NewVPNPanel(d.URL, d.Email, string(d.Password))
		var errs []string
		for _, a := range ads {
			r := req
			r.Ads = a
			r.BaseName = orchestrator.AdsBaseName(req.BaseName, a, len(ads) > 1)
			if err := v.Upload(ctx, r); err != nil {
				errs = append(errs, err.Error())
			}
		}
		item := fiber.Map{"id": d.ID, "name": d.Name, "ok": len(errs) == 0, "sets": len(ads)}
		if len(errs) > 0 {
			item["error"] = strings.Join(errs, "; ")
		} else {
			ok++
		}
		out = append(out, item)
	}
	if ok == 0 {
		return out, fmt.Errorf("بارگذاری در هیچ پنلی انجام نشد: %v", out[0]["error"])
	}
	return out, nil
}
