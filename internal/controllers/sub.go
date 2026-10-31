package controllers

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"

	"microserver/internal/models"
	"microserver/internal/services/orchestrator"
	"microserver/internal/services/settings"
)

// Sub serves the subscription of every live server's configs, built from the
// templates on each request so new servers and moved IPs show up at once.
// ?raw=1 returns the links as plain text instead of base64.
func (a *App) Sub(c *fiber.Ctx) error {
	token := a.Settings.Get(settings.SubToken)
	got := c.Params("token")
	if !a.Settings.Bool(settings.SubEnabled) || token == "" || subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
		return c.SendStatus(fiber.StatusNotFound)
	}
	body, up, down, err := BuildSub(a.DB)
	if err != nil {
		return err
	}
	c.Set("Content-Type", "text/plain; charset=utf-8")
	c.Set("Cache-Control", "no-store")
	c.Set("Profile-Update-Interval", "1")
	c.Set("Profile-Title", "base64:"+base64.StdEncoding.EncodeToString([]byte("microServer")))
	c.Set("Subscription-Userinfo", fmt.Sprintf("upload=%d; download=%d; total=0; expire=0", up, down))
	if c.Query("raw") != "" {
		return c.SendString(body)
	}
	return c.SendString(base64.StdEncoding.EncodeToString([]byte(body)))
}

// BuildSub lists the links of every active or filtered server, one per line,
// each labelled with its server (⛔ for a filtered one), and the servers'
// total traffic.
func BuildSub(db *gorm.DB) (body string, up, down int64, err error) {
	var servers []models.Server
	if err := db.Where("status IN ? AND ipv4 <> ''", []string{models.ServerActive, models.ServerFiltered}).
		Order("location_id, id").Find(&servers).Error; err != nil {
		return "", 0, 0, err
	}
	snap, err := orchestrator.LoadSnapshot(db)
	if err != nil {
		return "", 0, 0, err
	}
	var lines []string
	for _, s := range servers {
		label := s.Name + " · "
		if f := flag(s.Country); f != "" {
			label = f + " " + label
		}
		if s.Status == models.ServerFiltered {
			label = "⛔ " + label
		}
		// A template that fails to render is skipped; the rest still serve.
		links, _ := orchestrator.SubLinks(snap, orchestrator.LinkHost(&s), label)
		for _, l := range links {
			lines = append(lines, l.URL)
		}
		up += s.TrafficUp
		down += s.TrafficDown
	}
	return strings.Join(lines, "\n"), up, down, nil
}

// GetSub returns whether the subscription is on and its token, making the
// token on first use.
func (a *App) GetSub(c *fiber.Ctx) error {
	token := a.Settings.Get(settings.SubToken)
	if token == "" {
		var err error
		if token, err = a.newSubToken(); err != nil {
			return err
		}
	}
	return c.JSON(fiber.Map{"enabled": a.Settings.Bool(settings.SubEnabled), "token": token})
}

// RenewSubToken replaces the token; the old link stops working.
func (a *App) RenewSubToken(c *fiber.Ctx) error {
	token, err := a.newSubToken()
	if err != nil {
		return err
	}
	return c.JSON(fiber.Map{"enabled": a.Settings.Bool(settings.SubEnabled), "token": token})
}

func (a *App) newSubToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := hex.EncodeToString(b)
	return token, a.Settings.Set(map[string]string{settings.SubToken: token})
}

// flag turns a two-letter country code into its flag emoji.
func flag(cc string) string {
	if len(cc) != 2 {
		return ""
	}
	cc = strings.ToUpper(cc)
	if cc[0] < 'A' || cc[0] > 'Z' || cc[1] < 'A' || cc[1] > 'Z' {
		return ""
	}
	return string([]rune{rune(cc[0]-'A') + 0x1F1E6, rune(cc[1]-'A') + 0x1F1E6})
}
