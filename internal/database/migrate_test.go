package database

import (
	"path/filepath"
	"testing"
	"time"

	"microserver/internal/models"
)

// An inbound port used to be unique on its own; after the IP families it is
// unique per family, so the same port can serve a v4 and a v6 inbound.
func TestMigrateMakesPortUniquePerFamily(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	// The table as older versions created it.
	if err := db.Exec(`CREATE TABLE inbound_templates (id integer PRIMARY KEY AUTOINCREMENT, remark text NOT NULL, protocol text NOT NULL, port integer NOT NULL)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE UNIQUE INDEX idx_inbound_templates_port ON inbound_templates(port)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO inbound_templates (remark, protocol, port) VALUES ('old', 'vless', 443)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	var old models.InboundTemplate
	db.First(&old)
	if old.Family != models.FamilyBoth {
		t.Fatalf("existing inbound family %q", old.Family)
	}
	db.Model(&old).Update("family", models.FamilyV4)
	if err := db.Create(&models.InboundTemplate{Remark: "v6", Protocol: "vless", Port: 443, Family: models.FamilyV6}).Error; err != nil {
		t.Fatalf("v6 inbound on the v4 port: %v", err)
	}
	if err := db.Create(&models.InboundTemplate{Remark: "dup", Protocol: "vless", Port: 443, Family: models.FamilyV6}).Error; err == nil {
		t.Fatal("two v6 inbounds on one port")
	}
	if err := Migrate(db); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
}

// Servers from before ip_since get it from their last IP rotation, else from
// when they were built.
func TestMigrateBackfillsIPSince(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	// Servers without a provider account: only the backfill is under test.
	db.Exec("PRAGMA foreign_keys = OFF")
	built := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	rotated := time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)
	a := models.Server{Name: "a", CreatedAt: built}
	b := models.Server{Name: "b", CreatedAt: built}
	db.Create(&a)
	db.Create(&b)
	db.Exec("UPDATE servers SET ip_since = NULL")
	db.Create(&models.Job{Kind: "rotate", Status: models.JobDone, ServerID: &b.ID, FinishedAt: &rotated})
	db.Create(&models.Job{Kind: "rotate", Status: models.JobFailed, ServerID: &a.ID, FinishedAt: &rotated})
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	db.First(&a, a.ID)
	db.First(&b, b.ID)
	if a.IPSince == nil || !a.IPSince.Equal(built) {
		t.Fatalf("never rotated: %v", a.IPSince)
	}
	if b.IPSince == nil || !b.IPSince.Equal(rotated) {
		t.Fatalf("rotated: %v", b.IPSince)
	}
}

// A long read (a report over a big table) no longer holds up other reads
// or writes: reads have their own connections and WAL lets them run beside
// the writer.
func TestLongReadDoesNotBlock(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	slow := make(chan error, 1)
	go func() {
		var n int64
		// Seconds of work on one read connection.
		slow <- db.Raw(`SELECT count(*) FROM (WITH RECURSIVE c(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM c WHERE i < 30000000) SELECT i FROM c)`).Scan(&n).Error
	}()
	time.Sleep(200 * time.Millisecond)

	start := time.Now()
	var n int64
	if err := db.Model(&models.AdminUser{}).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.Setting{Key: "k", Value: "v"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&models.Setting{}).Where("key = ?", "k").Count(&n).Error; err != nil || n != 1 {
		t.Fatalf("read after write: %d %v", n, err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("other queries waited %v behind the long read", d)
	}
	select {
	case err := <-slow:
		if err == nil {
			t.Log("long read finished before the others; the machine is fast")
		}
	default:
	}
	if err := <-slow; err != nil {
		t.Fatal(err)
	}
}

// A write sent through Raw is still a write: it goes to the writer, not to
// the read-only pool.
func TestRawWriteGoesToWriter(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO settings (key, value) VALUES ('a', 'b')").Error; err != nil {
		t.Fatalf("exec insert: %v", err)
	}
	var v string
	if err := db.Raw("SELECT value FROM settings WHERE key = 'a'").Scan(&v).Error; err != nil || v != "b" {
		t.Fatalf("raw select: %q %v", v, err)
	}
}
