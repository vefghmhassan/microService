package middleware

import (
	"errors"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"
)

const CookieName = "ms_token"

type Auth struct {
	Secret []byte
	TTL    time.Duration
}

func (a *Auth) Issue(userID uint, username string) (string, time.Time, error) {
	exp := time.Now().Add(a.TTL)
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": float64(userID), "name": username, "exp": exp.Unix(), "iat": time.Now().Unix(),
	})
	s, err := tok.SignedString(a.Secret)
	return s, exp, err
}

func (a *Auth) parse(raw string) (uint, error) {
	tok, err := jwt.Parse(raw, func(t *jwt.Token) (any, error) {
		if t.Method != jwt.SigningMethodHS256 {
			return nil, errors.New("unexpected signing method")
		}
		return a.Secret, nil
	})
	if err != nil || !tok.Valid {
		return 0, errors.New("invalid token")
	}
	claims, _ := tok.Claims.(jwt.MapClaims)
	sub, _ := claims["sub"].(float64)
	if sub <= 0 {
		return 0, errors.New("invalid subject")
	}
	return uint(sub), nil
}

// Required accepts a Bearer token (API clients) or the session cookie (the
// dashboard). Cookie-authenticated writes must carry X-Requested-With, which a
// cross-site form cannot set — that, plus SameSite=Strict, is the CSRF guard.
// exists reports whether the user still exists, so a deleted admin's token
// stops working immediately.
func (a *Auth) Required(exists func(uint) bool) fiber.Handler {
	return func(c *fiber.Ctx) error {
		raw := ""
		fromCookie := false
		if h := c.Get(fiber.HeaderAuthorization); strings.HasPrefix(h, "Bearer ") {
			raw = strings.TrimPrefix(h, "Bearer ")
		} else if v := c.Cookies(CookieName); v != "" {
			raw, fromCookie = v, true
		}
		if raw == "" {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
		}
		uid, err := a.parse(raw)
		if err != nil || !exists(uid) {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
		}
		if fromCookie && c.Method() != fiber.MethodGet && c.Method() != fiber.MethodHead && c.Get("X-Requested-With") == "" {
			return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "missing X-Requested-With"})
		}
		c.Locals("uid", uid)
		return c.Next()
	}
}
