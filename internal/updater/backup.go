package updater

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "github.com/glebarez/go-sqlite" // pure-Go driver "sqlite", same as the panel
)

// Backup is a copy of the panel database (and its keys) taken before a swap.
type Backup struct {
	ID      string    `json:"id"`
	Commit  string    `json:"commit"`
	Reason  string    `json:"reason"`
	Created time.Time `json:"created"`
	SizeKB  int64     `json:"size_kb"`
}

const dbFile = "microserver.db"

// keyFiles are restored with the database: rows encrypted with an old master
// key are unreadable without it.
var keyFiles = []string{"master.key", "jwt.secret"}

func (u *Updater) backupDir() string { return filepath.Join(u.cfg.DataDir, "backups") }

// backupDB snapshots the live database with VACUUM INTO, which is consistent
// even while the panel keeps writing (WAL mode), so no downtime is needed.
func (u *Updater) backupDB(ctx context.Context, w io.Writer, commit, reason string) (string, error) {
	src := filepath.Join(u.cfg.DataDir, dbFile)
	if _, err := os.Stat(src); err != nil {
		fmt.Fprintf(w, "no database at %s yet, nothing to back up\n", src)
		return "", nil
	}
	id := u.now().UTC().Format("20060102-150405")
	if commit != "" {
		id += "-" + short(commit)
	}
	dir := filepath.Join(u.backupDir(), id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	db, err := sql.Open("sqlite", "file:"+src+"?_pragma=busy_timeout(30000)")
	if err != nil {
		return "", err
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, "VACUUM INTO ?", filepath.Join(dir, dbFile)); err != nil {
		_ = os.RemoveAll(dir)
		return "", fmt.Errorf("backup database: %w", err)
	}
	for _, k := range keyFiles {
		if err := copyFile(filepath.Join(u.cfg.DataDir, k), filepath.Join(dir, k)); err != nil && !errors.Is(err, os.ErrNotExist) {
			_ = os.RemoveAll(dir)
			return "", fmt.Errorf("backup %s: %w", k, err)
		}
	}
	meta, _ := json.Marshal(Backup{ID: id, Commit: commit, Reason: reason, Created: u.now()})
	_ = os.WriteFile(filepath.Join(dir, "meta.json"), meta, 0o600)
	fmt.Fprintf(w, "database backed up to backups/%s\n", id)
	u.pruneBackups(w)
	return id, nil
}

func (u *Updater) Backups() ([]Backup, error) {
	ents, err := os.ReadDir(u.backupDir())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var res []Backup
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		b := Backup{ID: e.Name()}
		if raw, err := os.ReadFile(filepath.Join(u.backupDir(), e.Name(), "meta.json")); err == nil {
			_ = json.Unmarshal(raw, &b)
			b.ID = e.Name()
		}
		if st, err := os.Stat(filepath.Join(u.backupDir(), e.Name(), dbFile)); err == nil {
			b.SizeKB = st.Size() >> 10
			if b.Created.IsZero() {
				b.Created = st.ModTime()
			}
		} else {
			continue
		}
		res = append(res, b)
	}
	sort.Slice(res, func(i, j int) bool { return res[i].ID > res[j].ID })
	return res, nil
}

func (u *Updater) pruneBackups(w io.Writer) {
	bs, err := u.Backups()
	if err != nil {
		return
	}
	for i, b := range bs {
		if i < u.cfg.KeepBackups {
			continue
		}
		if err := os.RemoveAll(filepath.Join(u.backupDir(), b.ID)); err == nil {
			fmt.Fprintf(w, "removed old backup %s\n", b.ID)
		}
	}
}

func validBackupID(id string) bool {
	return id != "" && !strings.ContainsAny(id, `/\`) && !strings.Contains(id, "..")
}

// restoreDB replaces the database with a backup. The panel must be stopped.
func (u *Updater) restoreDB(w io.Writer, id string) error {
	if !validBackupID(id) {
		return errors.New("شناسه بکاپ نامعتبر است")
	}
	dir := filepath.Join(u.backupDir(), id)
	if _, err := os.Stat(filepath.Join(dir, dbFile)); err != nil {
		return fmt.Errorf("بکاپ %s پیدا نشد", id)
	}
	dst := filepath.Join(u.cfg.DataDir, dbFile)
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.Remove(dst + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err := copyFile(filepath.Join(dir, dbFile), dst); err != nil {
		return fmt.Errorf("restore database: %w", err)
	}
	for _, k := range keyFiles {
		if err := copyFile(filepath.Join(dir, k), filepath.Join(u.cfg.DataDir, k)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("restore %s: %w", k, err)
		}
	}
	fmt.Fprintf(w, "database restored from backups/%s\n", id)
	return nil
}

// copyFile writes through a temp file and renames, so a failed copy never
// leaves a half-written database behind.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}
