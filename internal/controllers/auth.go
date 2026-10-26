package controllers

import (
	"time"

	"github.com/gofiber/fiber/v2"

	"microserver/internal/middleware"
	"microserver/internal/models"
)

func (a *App) Login(c *fiber.Ctx) error {
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.BodyParser(&in); err != nil {
		return errJSON(c, fiber.StatusBadRequest, "invalid body")
	}
	var u models.AdminUser
	if err := a.DB.Where("username = ?", in.Username).First(&u).Error; err != nil || !u.CheckPassword(in.Password) {
		// Same answer and similar time either way.
		time.Sleep(300 * time.Millisecond)
		return errJSON(c, fiber.StatusUnauthorized, "نام کاربری یا رمز عبور اشتباه است")
	}
	tok, exp, err := a.Auth.Issue(u.ID, u.Username)
	if err != nil {
		return err
	}
	c.Cookie(&fiber.Cookie{
		Name: middleware.CookieName, Value: tok, Expires: exp, HTTPOnly: true,
		Secure: a.Cfg.CookieSecure, SameSite: "Strict", Path: "/",
	})
	return c.JSON(fiber.Map{"token": tok, "expires_at": exp, "user": u})
}

func (a *App) Logout(c *fiber.Ctx) error {
	c.Cookie(&fiber.Cookie{Name: middleware.CookieName, Value: "", Expires: time.Unix(0, 0), HTTPOnly: true, Path: "/", SameSite: "Strict"})
	return c.JSON(fiber.Map{"ok": true})
}

func (a *App) Me(c *fiber.Ctx) error {
	var u models.AdminUser
	if err := a.DB.First(&u, c.Locals("uid")).Error; err != nil {
		return errJSON(c, fiber.StatusUnauthorized, "unauthorized")
	}
	return c.JSON(u)
}

func (a *App) ChangePassword(c *fiber.Ctx) error {
	var in struct {
		Current  string `json:"current"`
		Password string `json:"password"`
		Username string `json:"username"`
	}
	if err := c.BodyParser(&in); err != nil {
		return errJSON(c, fiber.StatusBadRequest, "invalid body")
	}
	var u models.AdminUser
	if err := a.DB.First(&u, c.Locals("uid")).Error; err != nil {
		return errJSON(c, fiber.StatusUnauthorized, "unauthorized")
	}
	if !u.CheckPassword(in.Current) {
		return errJSON(c, fiber.StatusBadRequest, "رمز فعلی اشتباه است")
	}
	if len(in.Password) < 8 {
		return errJSON(c, fiber.StatusBadRequest, "رمز جدید باید حداقل ۸ کاراکتر باشد")
	}
	if err := u.SetPassword(in.Password); err != nil {
		return err
	}
	if in.Username != "" {
		u.Username = in.Username
	}
	if err := a.DB.Save(&u).Error; err != nil {
		return errJSON(c, fiber.StatusBadRequest, err.Error())
	}
	return c.JSON(u)
}
