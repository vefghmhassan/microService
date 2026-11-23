package database

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"gorm.io/gorm"
)

// CompactMinFree is how much free space inside the file makes compacting
// worth it, as bytes and as a share of the file.
var (
	CompactMinFreeBytes int64 = 256 << 20
	CompactMinFreeShare       = 0.25
)

// CompactResult says what Compact did.
type CompactResult struct {
	Before, After int64  // file size in bytes
	Skipped       string // why it did not run; empty when it did
}

// Compact gives the space of deleted rows back to the disk: SQLite keeps
// freed pages inside the file for reuse, so after a large delete the file
// stays as big as before. It runs VACUUM only when enough of the file is free
// space and the disk has room for the rewrite (a copy of the live data plus
// the WAL), and truncates the WAL after.
//
// VACUUM takes the write connection for its whole run (minutes on gigabytes):
// writes wait, reads go on from the read pool.
func Compact(db *gorm.DB) (CompactResult, error) {
	var res CompactResult
	var pageSize, pages, free int64
	if err := db.Exec("PRAGMA wal_checkpoint(PASSIVE)").Error; err != nil {
		return res, err
	}
	db.Raw("PRAGMA page_size").Scan(&pageSize)
	db.Raw("PRAGMA page_count").Scan(&pages)
	db.Raw("PRAGMA freelist_count").Scan(&free)
	if pageSize <= 0 || pages <= 0 {
		return res, errors.New("could not read the database size")
	}
	res.Before = pageSize * pages
	freeBytes := pageSize * free
	if freeBytes < CompactMinFreeBytes || float64(free) < CompactMinFreeShare*float64(pages) {
		res.Skipped = fmt.Sprintf("only %d MB of %d MB is free space", freeBytes>>20, res.Before>>20)
		res.After = res.Before
		return res, nil
	}
	path, err := dbPath(db)
	if err != nil {
		return res, err
	}
	need := 2 * (res.Before - freeBytes)
	if avail, err := diskFree(filepath.Dir(path)); err == nil && avail < need+(256<<20) {
		res.Skipped = fmt.Sprintf("disk has %d MB free, the rewrite needs about %d MB", avail>>20, need>>20)
		res.After = res.Before
		return res, nil
	}
	if err := db.Exec("VACUUM").Error; err != nil {
		return res, fmt.Errorf("vacuum: %w", err)
	}
	db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	if st, err := os.Stat(path); err == nil {
		res.After = st.Size()
	}
	return res, nil
}

// dbPath is the main database file.
func dbPath(db *gorm.DB) (string, error) {
	var rows []struct {
		Name string
		File string
	}
	if err := db.Raw("PRAGMA database_list").Scan(&rows).Error; err != nil {
		return "", err
	}
	for _, r := range rows {
		if r.Name == "main" && r.File != "" {
			return r.File, nil
		}
	}
	return "", errors.New("database file not found")
}

func diskFree(dir string) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * int64(st.Bsize), nil
}
