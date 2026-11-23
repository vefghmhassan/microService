package database

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCompactShrinksAfterDelete(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	// Nothing deleted yet: not worth it.
	res, err := Compact(db)
	if err != nil || res.Skipped == "" {
		t.Fatalf("empty db compacted: %+v %v", res, err)
	}

	old := CompactMinFreeBytes
	CompactMinFreeBytes = 1 << 20
	defer func() { CompactMinFreeBytes = old }()

	// Tens of MB of rows, then most of them deleted.
	err = db.Exec(`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i < 100000)
		INSERT INTO client_ips (server_id, day, ip, email, inbound, conns, first_seen, last_seen)
		SELECT i % 50, '2026-11-01', 'ip' || i, printf('%.200c', 'x') || i, 'DE', 1, datetime('now'), datetime('now') FROM n`).Error
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("DELETE FROM client_ips WHERE id > 5000").Error; err != nil {
		t.Fatal(err)
	}
	db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	before, _ := os.Stat(path)

	res, err = Compact(db)
	if err != nil {
		t.Fatal(err)
	}
	if res.Skipped != "" {
		t.Fatalf("skipped: %s", res.Skipped)
	}
	after, _ := os.Stat(path)
	if after.Size() >= before.Size()/2 || res.After != after.Size() {
		t.Fatalf("file %d -> %d MB (result %+v)", before.Size()>>20, after.Size()>>20, res)
	}
	var n int64
	db.Table("client_ips").Count(&n)
	if n != 5000 {
		t.Fatalf("rows after compact: %d", n)
	}
	t.Logf("file %d MB -> %d MB", before.Size()>>20, after.Size()>>20)
}
