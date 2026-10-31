package controllers

import (
	"encoding/base64"
	"io"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"

	"microserver/internal/database"
	"microserver/internal/models"
	"microserver/internal/services/secret"
	"microserver/internal/services/settings"
)

func subApp(t *testing.T) (*App, *fiber.App) {
	t.Helper()
	if err := secret.Init(make([]byte, 32)); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(db); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			sqlDB.Close()
		}
	})
	st, err := settings.New(db)
	if err != nil {
		t.Fatal(err)
	}
	vless := `{"clients":[{"id":"11111111-2222-4333-8444-555555555555","email":"u1"}],"decryption":"none"}`
	stream := `{"network":"tcp","security":"none"}`
	db.Create(&models.InboundTemplate{Remark: "DE", Protocol: "vless", Port: 443, Enabled: true, Upload: true, Settings: vless, StreamSettings: stream})
	// Not uploaded to vpn-pannel, but still part of the test sub.
	db.Create(&models.InboundTemplate{Remark: "TEST", Protocol: "vless", Port: 8443, Enabled: true, Settings: vless, StreamSettings: stream})
	db.Create(&models.InboundTemplate{Remark: "OFF", Protocol: "vless", Port: 9443, Settings: vless, StreamSettings: stream})
	acc := &models.ProviderAccount{Name: "fake", Type: "fake", Enabled: true}
	db.Create(acc)
	db.Create(&models.Server{ProviderAccountID: acc.ID, Name: "a", Country: "DE", Status: models.ServerActive, IPv4: "10.0.0.1", TrafficUp: 5, TrafficDown: 7})
	db.Create(&models.Server{ProviderAccountID: acc.ID, Name: "b", Status: models.ServerFiltered, IPv4: "10.0.0.2", Hostname: "fr1.example.com"})
	db.Create(&models.Server{ProviderAccountID: acc.ID, Name: "c", Status: models.ServerDeleted, IPv4: "10.0.0.3"})

	a := &App{DB: db, Settings: st}
	app := fiber.New()
	app.Get("/sub/:token", a.Sub)
	return a, app
}

func fetch(t *testing.T, app *fiber.App, path string) (int, string) {
	t.Helper()
	res, err := app.Test(httptest.NewRequest("GET", path, nil))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

func TestSubLinksEveryLiveServer(t *testing.T) {
	a, app := subApp(t)
	if code, _ := fetch(t, app, "/sub/x"); code != 404 {
		t.Fatalf("disabled sub served: %d", code)
	}
	a.Settings.Set(map[string]string{settings.SubEnabled: "true", settings.SubToken: "secret"})
	if code, _ := fetch(t, app, "/sub/wrong"); code != 404 {
		t.Fatalf("wrong token served: %d", code)
	}

	code, body := fetch(t, app, "/sub/secret")
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	raw, err := base64.StdEncoding.DecodeString(body)
	if err != nil {
		t.Fatalf("not base64: %v", err)
	}
	lines := strings.Split(string(raw), "\n")
	// Two live servers × two enabled inbounds; the deleted server and the
	// disabled inbound are left out.
	if len(lines) != 4 {
		t.Fatalf("%d lines:\n%s", len(lines), raw)
	}
	all := string(raw)
	for _, want := range []string{"@10.0.0.1:443", "@10.0.0.1:8443", "@fr1.example.com:443", "%E2%9B%94"} {
		if !strings.Contains(all, want) {
			t.Fatalf("missing %q in\n%s", want, all)
		}
	}
	if strings.Contains(all, "10.0.0.3") || strings.Contains(all, ":9443") {
		t.Fatalf("deleted server or disabled inbound in\n%s", all)
	}
	if !strings.Contains(lines[0], "#%F0%9F%87%A9%F0%9F%87%AA%20a%20%C2%B7%20DE") {
		t.Fatalf("label of first link: %s", lines[0])
	}

	_, plain := fetch(t, app, "/sub/secret?raw=1")
	if plain != all {
		t.Fatalf("raw differs:\n%s", plain)
	}
}
