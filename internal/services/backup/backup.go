// Package backup exports the whole panel (every table plus the SSH key) to one
// portable file and imports it back.
//
// Encrypted cells are decrypted on export and encrypted again with the target
// panel's master key on import, so a backup does not depend on master.key and
// can be restored on any install. The file itself is optionally encrypted with
// a password (scrypt + AES-256-GCM).
package backup

import (
	"bytes"
	"compress/gzip"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/crypto/scrypt"
	"gorm.io/gorm"

	"microserver/internal/models"
	"microserver/internal/services/secret"
	"microserver/internal/version"
)

// FormatVersion is bumped when the file layout changes incompatibly.
const FormatVersion = 1

// Ext is the extension of backup files.
const Ext = ".msbk"

// sshKeyFile is the panel's SSH identity in DATA_DIR (see sshx.LoadOrCreateKey).
const sshKeyFile = "id_ed25519"

// PendingFile is a validated backup waiting to be applied on the next boot.
const PendingFile = "restore.pending"

// ConfigModels are always exported; HistoryModels only on request. Together
// they must cover models.All() (enforced by a test).
func ConfigModels() []any {
	return []any{
		&models.AdminUser{}, &models.ProviderAccount{}, &models.Location{}, &models.Server{},
		&models.IPAddress{}, &models.InboundStat{}, &models.XrayTemplate{}, &models.InboundTemplate{},
		&models.Setting{}, &models.Destination{}, &models.CloudflareAccount{}, &models.Certificate{},
		&models.CleanIP{}, &models.CleanIPSource{}, &models.CDNAddon{},
		&models.SmartProfile{}, &models.SmartRule{}, &models.SmartUpload{},
	}
}

func HistoryModels() []any {
	return []any{
		&models.Job{}, &models.JobLog{}, &models.CheckRun{}, &models.CheckProbe{},
		&models.ClientIP{}, &models.IPGeo{},
	}
}

// File is the content of a backup.
type File struct {
	Format     int              `json:"format"`
	AppVersion string           `json:"app_version"`
	Commit     string           `json:"commit,omitempty"`
	Created    time.Time        `json:"created"`
	History    bool             `json:"history"`
	SSHKey     string           `json:"ssh_key,omitempty"`
	Tables     map[string]Table `json:"tables"`
}

// Table holds rows as arrays aligned with Columns.
type Table struct {
	Columns []string `json:"columns"`
	Rows    [][]any  `json:"rows"`
}

// Summary describes a backup for the UI.
type Summary struct {
	Format     int            `json:"format"`
	AppVersion string         `json:"app_version"`
	Commit     string         `json:"commit"`
	Created    time.Time      `json:"created"`
	History    bool           `json:"history"`
	SSHKey     bool           `json:"ssh_key"`
	Encrypted  bool           `json:"encrypted"`
	Counts     map[string]int `json:"counts"`
}

func (f *File) Summary(encrypted bool) Summary {
	s := Summary{Format: f.Format, AppVersion: f.AppVersion, Commit: f.Commit, Created: f.Created,
		History: f.History, SSHKey: f.SSHKey != "", Encrypted: encrypted, Counts: map[string]int{}}
	for name, t := range f.Tables {
		s.Counts[name] = len(t.Rows)
	}
	return s
}

// Cell wrappers for values JSON cannot carry as-is.
const (
	secretTag = "$secret" // a cell that is encrypted at rest
	bytesTag  = "$bytes"  // a BLOB, base64
)

func tableName(db *gorm.DB, m any) (string, error) {
	stmt := &gorm.Statement{DB: db}
	if err := stmt.Parse(m); err != nil {
		return "", err
	}
	return stmt.Schema.Table, nil
}

func quote(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

// Export reads every config table (and the history tables when history is
// set) in one transaction, plus the SSH key from dataDir.
func Export(db *gorm.DB, dataDir string, history bool) (*File, error) {
	v := version.Get()
	f := &File{Format: FormatVersion, AppVersion: v.Version, Commit: v.Commit, Created: time.Now().UTC(),
		History: history, Tables: map[string]Table{}}
	list := ConfigModels()
	if history {
		list = append(list, HistoryModels()...)
	}
	err := db.Transaction(func(tx *gorm.DB) error {
		for _, m := range list {
			name, err := tableName(tx, m)
			if err != nil {
				return err
			}
			t, err := dumpTable(tx, name)
			if err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			f.Tables[name] = t
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if dataDir != "" {
		raw, err := os.ReadFile(filepath.Join(dataDir, sshKeyFile))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("ssh key: %w", err)
		}
		f.SSHKey = string(raw)
	}
	return f, nil
}

func dumpTable(tx *gorm.DB, name string) (Table, error) {
	rows, err := tx.Raw("SELECT * FROM " + quote(name)).Rows()
	if err != nil {
		return Table{}, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return Table{}, err
	}
	t := Table{Columns: cols, Rows: [][]any{}}
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return Table{}, err
		}
		for i, v := range vals {
			cell, err := exportCell(v)
			if err != nil {
				return Table{}, fmt.Errorf("column %s: %w", cols[i], err)
			}
			vals[i] = cell
		}
		t.Rows = append(t.Rows, vals)
	}
	return t, rows.Err()
}

func exportCell(v any) (any, error) {
	switch x := v.(type) {
	case string:
		if strings.HasPrefix(x, "enc:v1:") {
			plain, err := secret.Decrypt(x)
			if err != nil {
				return nil, err
			}
			return map[string]string{secretTag: plain}, nil
		}
		return x, nil
	case []byte:
		if strings.HasPrefix(string(x), "enc:v1:") {
			return exportCell(string(x))
		}
		return map[string]string{bytesTag: base64.StdEncoding.EncodeToString(x)}, nil
	case time.Time:
		return x.Format(time.RFC3339Nano), nil
	}
	return v, nil
}

// ---- file encoding ----

// encMagic starts a password-protected file; a plain one is gzipped JSON.
var encMagic = []byte("MSBK1E")

const saltLen = 16

// ErrPassword is returned for a missing or wrong password.
var ErrPassword = errors.New("رمز فایل بکاپ اشتباه است یا وارد نشده")

func deriveKey(password string, salt []byte) ([]byte, error) {
	return scrypt.Key([]byte(password), salt, 1<<15, 8, 1, 32)
}

// Encode serialises f; with a password the result is encrypted.
func Encode(f *File, password string) ([]byte, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if err := json.NewEncoder(zw).Encode(f); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	if password == "" {
		return buf.Bytes(), nil
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	key, err := deriveKey(password, salt)
	if err != nil {
		return nil, err
	}
	g, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, g.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	out := append([]byte{}, encMagic...)
	out = append(out, salt...)
	out = append(out, nonce...)
	return g.Seal(out, nonce, buf.Bytes(), encMagic), nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// IsEncrypted reports whether raw needs a password.
func IsEncrypted(raw []byte) bool { return bytes.HasPrefix(raw, encMagic) }

// Decode parses a backup file, decrypting it with password when needed.
func Decode(raw []byte, password string) (*File, error) {
	if IsEncrypted(raw) {
		if password == "" {
			return nil, ErrPassword
		}
		rest := raw[len(encMagic):]
		if len(rest) < saltLen+12 {
			return nil, errors.New("فایل بکاپ خراب است")
		}
		key, err := deriveKey(password, rest[:saltLen])
		if err != nil {
			return nil, err
		}
		g, err := newGCM(key)
		if err != nil {
			return nil, err
		}
		ns := g.NonceSize()
		rest = rest[saltLen:]
		plain, err := g.Open(nil, rest[:ns], rest[ns:], encMagic)
		if err != nil {
			return nil, ErrPassword
		}
		raw = plain
	}
	var r io.Reader = bytes.NewReader(raw)
	if len(raw) > 2 && raw[0] == 0x1f && raw[1] == 0x8b {
		zr, err := gzip.NewReader(r)
		if err != nil {
			return nil, fmt.Errorf("فایل بکاپ خراب است: %w", err)
		}
		defer zr.Close()
		r = zr
	}
	dec := json.NewDecoder(r)
	dec.UseNumber()
	var f File
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("فایل بکاپ معتبر نیست: %w", err)
	}
	if f.Format == 0 || f.Tables == nil {
		return nil, errors.New("این فایل بکاپ پنل نیست")
	}
	if f.Format > FormatVersion {
		return nil, fmt.Errorf("این بکاپ با نسخه جدیدتر پنل ساخته شده (فرمت %d)؛ اول پنل را به‌روز کنید", f.Format)
	}
	return &f, nil
}

// ---- restore ----

type column struct {
	name string
	typ  string
}

func tableColumns(tx *gorm.DB, name string) (map[string]column, error) {
	rows, err := tx.Raw("SELECT name, type FROM pragma_table_info(?)", name).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]column{}
	for rows.Next() {
		var c column
		if err := rows.Scan(&c.name, &c.typ); err != nil {
			return nil, err
		}
		out[c.name] = c
	}
	return out, rows.Err()
}

// Apply replaces every table with the backup's rows, in one transaction, and
// writes the SSH key to dataDir. The schema must already be migrated. It must
// run while nothing else uses the database (at boot).
func Apply(db *gorm.DB, dataDir string, f *File) error {
	all := append(ConfigModels(), HistoryModels()...)
	names := make([]string, 0, len(all))
	for _, m := range all {
		n, err := tableName(db, m)
		if err != nil {
			return err
		}
		names = append(names, n)
	}
	// Rows reference each other (servers → providers, …); the order of the
	// inserts must not matter. The pragma is a no-op inside a transaction, so
	// it is switched around it. The pool has a single connection.
	if err := db.Exec("PRAGMA foreign_keys = OFF").Error; err != nil {
		return err
	}
	defer db.Exec("PRAGMA foreign_keys = ON")

	err := db.Transaction(func(tx *gorm.DB) error {
		for _, n := range names {
			if err := tx.Exec("DELETE FROM " + quote(n)).Error; err != nil {
				return fmt.Errorf("clear %s: %w", n, err)
			}
		}
		known := map[string]bool{}
		for _, n := range names {
			known[n] = true
		}
		tables := make([]string, 0, len(f.Tables))
		for n := range f.Tables {
			tables = append(tables, n)
		}
		sort.Strings(tables)
		for _, n := range tables {
			if !known[n] {
				continue // a table this version no longer has
			}
			if err := loadTable(tx, n, f.Tables[n]); err != nil {
				return fmt.Errorf("%s: %w", n, err)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if f.SSHKey != "" && dataDir != "" {
		if err := writeAtomic(filepath.Join(dataDir, sshKeyFile), []byte(f.SSHKey)); err != nil {
			return fmt.Errorf("ssh key: %w", err)
		}
	}
	return nil
}

func loadTable(tx *gorm.DB, name string, t Table) error {
	if len(t.Rows) == 0 {
		return nil
	}
	have, err := tableColumns(tx, name)
	if err != nil {
		return err
	}
	// Only the columns the current schema has; newer columns keep defaults.
	var idx []int
	var cols []column
	for i, c := range t.Columns {
		if col, ok := have[c]; ok {
			idx = append(idx, i)
			cols = append(cols, col)
		}
	}
	if len(cols) == 0 {
		return nil
	}
	names := make([]string, len(cols))
	for i, c := range cols {
		names[i] = quote(c.name)
	}
	rowPH := "(" + strings.TrimSuffix(strings.Repeat("?,", len(cols)), ",") + ")"
	head := "INSERT INTO " + quote(name) + " (" + strings.Join(names, ",") + ") VALUES "
	// SQLite allows 32766 parameters; stay well below.
	per := 2000 / len(cols)
	if per < 1 {
		per = 1
	}
	for start := 0; start < len(t.Rows); start += per {
		end := min(start+per, len(t.Rows))
		phs := make([]string, 0, end-start)
		args := make([]any, 0, (end-start)*len(cols))
		for _, row := range t.Rows[start:end] {
			if len(row) != len(t.Columns) {
				return errors.New("row width does not match columns")
			}
			for j, i := range idx {
				v, err := importCell(row[i], cols[j].typ)
				if err != nil {
					return fmt.Errorf("column %s: %w", cols[j].name, err)
				}
				args = append(args, v)
			}
			phs = append(phs, rowPH)
		}
		if err := tx.Exec(head+strings.Join(phs, ","), args...).Error; err != nil {
			return err
		}
	}
	return nil
}

func importCell(v any, typ string) (any, error) {
	switch x := v.(type) {
	case nil:
		return nil, nil
	case json.Number:
		if n, err := x.Int64(); err == nil {
			return n, nil
		}
		return x.Float64()
	case bool:
		return x, nil
	case map[string]any:
		if s, ok := x[secretTag].(string); ok {
			return secret.Encrypt(s)
		}
		if s, ok := x[bytesTag].(string); ok {
			return base64.StdEncoding.DecodeString(s)
		}
		return nil, errors.New("unknown cell object")
	case string:
		t := strings.ToLower(typ)
		if strings.Contains(t, "date") || strings.Contains(t, "time") {
			if ts, err := time.Parse(time.RFC3339Nano, x); err == nil {
				return ts, nil
			}
		}
		return x, nil
	}
	return nil, fmt.Errorf("unsupported cell %T", v)
}

func writeAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ---- staging (import is applied on the next boot) ----

// Stage writes f as the pending restore in dataDir.
func Stage(dataDir string, f *File) error {
	raw, err := Encode(f, "")
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(dataDir, PendingFile), raw)
}

// ApplyPending applies a staged restore, if any. On failure the file is kept
// as restore.failed and the database is left as it was. It reports whether a
// restore was applied.
func ApplyPending(db *gorm.DB, dataDir string) (bool, error) {
	path := filepath.Join(dataDir, PendingFile)
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	fail := func(err error) (bool, error) {
		_ = os.Rename(path, filepath.Join(dataDir, "restore.failed"))
		return false, err
	}
	f, err := Decode(raw, "")
	if err != nil {
		return fail(err)
	}
	if err := Apply(db, dataDir, f); err != nil {
		return fail(err)
	}
	return true, os.Remove(path)
}

// SaveLocal writes an unencrypted backup of the current state to
// dataDir/backups-panel and returns its path. It is taken before an import.
func SaveLocal(db *gorm.DB, dataDir, prefix string) (string, error) {
	// Without history: this is the copy an import can be undone with, and
	// job logs and checks only made it slow.
	f, err := Export(db, dataDir, false)
	if err != nil {
		return "", err
	}
	raw, err := Encode(f, "")
	if err != nil {
		return "", err
	}
	dir := filepath.Join(dataDir, "backups-panel")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, prefix+"-"+time.Now().UTC().Format("20060102-150405")+Ext)
	return path, writeAtomic(path, raw)
}

// FileName is the download name of a backup taken now.
func FileName() string {
	return "microserver-backup-" + time.Now().Format("2006-01-02-1504") + Ext
}
