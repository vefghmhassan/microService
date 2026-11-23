package controllers

import (
	"context"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"

	"microserver/internal/models"
)

// A report that runs past its time limit is stopped and frees SQLite's one
// connection, so the rest of the panel keeps answering.
func TestGeoReportTimeoutFreesDB(t *testing.T) {
	a, _ := subApp(t)
	// Many rows, so the report's queries take a while.
	err := a.DB.Exec(`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i < 200000)
		INSERT INTO client_ips (server_id, day, ip, email, inbound, conns, first_seen, last_seen)
		SELECT i % 50, '2026-11-23', '10.' || (i % 250) || '.' || (i / 250 % 250) || '.' || (i % 7), 'u' || (i % 3000), 'DE', 1,
			datetime('now'), datetime('now') FROM n`).Error
	if err != nil {
		t.Fatal(err)
	}
	app := fiber.New()
	app.Get("/geo/report", a.GeoReport)

	old := GeoReportTimeout
	GeoReportTimeout = time.Millisecond
	code, body := call(t, app, "GET", "/geo/report?range=7d", "")
	GeoReportTimeout = old
	if code != fiber.StatusServiceUnavailable {
		t.Fatalf("timed-out report: %d %.200s", code, body)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var n int64
	if err := a.DB.WithContext(ctx).Model(&models.Server{}).Count(&n).Error; err != nil {
		t.Fatalf("db still held after the report stopped: %v", err)
	}

	// Within the limit it is built once and then served from the cache.
	if code, body := call(t, app, "GET", "/geo/report?range=1h", ""); code != 200 {
		t.Fatalf("report: %d %.200s", code, body)
	}
	a.DB.Exec("DELETE FROM client_ips")
	if code, body := call(t, app, "GET", "/geo/report?range=1h", ""); code != 200 || len(body) < 100 {
		t.Fatalf("cached report: %d %.200s", code, body)
	}
}
