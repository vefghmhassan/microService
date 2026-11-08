package controllers

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"microserver/internal/models"
)

var updaterClient = &http.Client{Timeout: 2 * time.Minute}

// UpdaterProxy forwards /api/v1/update/<path> to the updater sidecar, which
// builds new versions and swaps the panel container. The panel cannot do this
// itself: it would have to restart itself mid-request and has no git or docker.
func (a *App) UpdaterProxy(c *fiber.Ctx) error {
	path := "/" + strings.TrimPrefix(c.Params("*"), "/")
	url := a.Cfg.UpdaterURL + path
	if q := string(c.Request().URI().QueryString()); q != "" {
		url += "?" + q
	}
	ctx, cancel := context.WithTimeout(c.UserContext(), 2*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, c.Method(), url, bytes.NewReader(c.Body()))
	if err != nil {
		return errJSON(c, fiber.StatusBadRequest, err.Error())
	}
	req.Header.Set("Authorization", "Bearer "+a.Cfg.UpdaterToken)
	req.Header.Set("Content-Type", "application/json")
	if c.Method() != fiber.MethodGet {
		// Tell the updater who started the change, for its history.
		req.Header.Set("X-Panel-User", a.username(c))
		// Jobs still running will be re-queued after the panel restarts.
		var running int64
		a.DB.Model(&models.Job{}).Where("status = ?", models.JobRunning).Count(&running)
		req.Header.Set("X-Panel-Running-Jobs", strconv.FormatInt(running, 10))
	}
	resp, err := updaterClient.Do(req)
	if err != nil {
		return errJSON(c, fiber.StatusServiceUnavailable,
			"سرویس updater در دسترس نیست (کانتینر updater اجرا نشده یا پنل بیرون از docker compose اجرا می‌شود): "+err.Error())
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return errJSON(c, fiber.StatusBadGateway, err.Error())
	}
	c.Set(fiber.HeaderContentType, resp.Header.Get("Content-Type"))
	return c.Status(resp.StatusCode).Send(body)
}

func (a *App) username(c *fiber.Ctx) string {
	var u models.AdminUser
	if a.DB.First(&u, c.Locals("uid")).Error != nil {
		return "panel"
	}
	return u.Username
}
