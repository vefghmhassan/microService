// Package cleanip keeps the list of Cloudflare edge addresses that work from
// Iranian operators: fetched from URLs (the built-in Clean-IP-Resolver list or
// any custom one) and added by hand.
package cleanip

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"microserver/internal/models"
)

// BuiltinURL is Clean-IP-Resolver's list (ircf.space data, per operator,
// updated several times a day).
const BuiltinURL = "https://raw.githubusercontent.com/vAHiD55555/Clean-IP-Resolver/main/list.json"

const Manual = "manual"

// Entry is one address with the operator it was found clean on ("" = any).
type Entry struct {
	Address  string
	Operator string
}

// SourceKey is CleanIP.Source for rows fetched from src.
func SourceKey(id uint) string { return fmt.Sprintf("source:%d", id) }

// Parse reads Clean-IP-Resolver JSON ({"ipv4":[{"ip","operator"}],"ipv6":…})
// or plain text: one address per line, optionally followed by an operator
// (comma, space or tab separated); "#" starts a comment.
func Parse(body []byte) ([]Entry, error) {
	var out []Entry
	seen := map[string]bool{}
	add := func(addr, op string) {
		addr = strings.ToLower(strings.TrimSpace(addr))
		if !ValidAddress(addr) || seen[addr] {
			return
		}
		seen[addr] = true
		out = append(out, Entry{Address: addr, Operator: strings.ToUpper(strings.TrimSpace(op))})
	}
	if b := strings.TrimSpace(string(body)); strings.HasPrefix(b, "{") || strings.HasPrefix(b, "[") {
		var doc struct {
			IPv4 []struct{ IP, Operator string }
			IPv6 []struct{ IP, Operator string }
		}
		var list []struct{ IP, Operator string }
		if json.Unmarshal(body, &doc) == nil && (doc.IPv4 != nil || doc.IPv6 != nil) {
			for _, e := range append(doc.IPv4, doc.IPv6...) {
				add(e.IP, e.Operator)
			}
			return out, nil
		}
		if err := json.Unmarshal(body, &list); err != nil {
			return nil, fmt.Errorf("unknown JSON format: %w", err)
		}
		for _, e := range list {
			add(e.IP, e.Operator)
		}
		return out, nil
	}
	for _, line := range strings.Split(string(body), "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		f := strings.FieldsFunc(line, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\r' || r == ';' })
		switch len(f) {
		case 0:
		case 1:
			add(f[0], "")
		default:
			add(f[0], f[1])
		}
	}
	return out, nil
}

// ValidAddress accepts an IP or a hostname (a domain that resolves to clean IPs).
func ValidAddress(s string) bool {
	if net.ParseIP(s) != nil {
		return true
	}
	if len(s) > 253 || !strings.Contains(s, ".") {
		return false
	}
	for _, label := range strings.Split(s, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
				return false
			}
		}
	}
	return true
}

// Fetch downloads and parses one source.
func Fetch(ctx context.Context, client *http.Client, url string) ([]Entry, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	return Parse(body)
}

// Syncer refreshes the fetched part of the list.
type Syncer struct {
	DB   *gorm.DB
	HTTP *http.Client
	// Operators filters the built-in source (empty: all).
	Operators func() []string
}

// EnsureBuiltin creates the built-in source on first use.
func EnsureBuiltin(db *gorm.DB) error {
	var n int64
	db.Model(&models.CleanIPSource{}).Where("builtin = ?", true).Count(&n)
	if n > 0 {
		return nil
	}
	return db.Create(&models.CleanIPSource{URL: BuiltinURL, Builtin: true, Enabled: true}).Error
}

// Sync fetches every enabled source. Rows a source no longer lists are
// removed; a source that fails keeps its rows. Manual rows are never touched.
func (s *Syncer) Sync(ctx context.Context) error {
	if err := EnsureBuiltin(s.DB); err != nil {
		return err
	}
	var srcs []models.CleanIPSource
	if err := s.DB.Where("enabled = ?", true).Order("id").Find(&srcs).Error; err != nil {
		return err
	}
	var errs []error
	for _, src := range srcs {
		if err := s.syncOne(ctx, &src); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", src.URL, err))
		}
	}
	return errors.Join(errs...)
}

// SyncSource fetches one source now.
func (s *Syncer) SyncSource(ctx context.Context, id uint) error {
	var src models.CleanIPSource
	if err := s.DB.First(&src, id).Error; err != nil {
		return err
	}
	return s.syncOne(ctx, &src)
}

func (s *Syncer) syncOne(ctx context.Context, src *models.CleanIPSource) error {
	client := s.HTTP
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	entries, err := Fetch(ctx, client, src.URL)
	now := time.Now()
	src.LastFetch = &now
	if err != nil {
		src.LastError = err.Error()
		s.DB.Save(src)
		return err
	}
	if src.Builtin && s.Operators != nil {
		if ops := s.Operators(); len(ops) > 0 {
			entries = slices.DeleteFunc(entries, func(e Entry) bool {
				return !slices.ContainsFunc(ops, func(op string) bool { return strings.EqualFold(op, e.Operator) })
			})
		}
	}
	key := SourceKey(src.ID)
	err = s.DB.Transaction(func(tx *gorm.DB) error {
		for _, e := range entries {
			row := models.CleanIP{Address: e.Address, Operator: e.Operator, Source: key, Enabled: true, LastSeen: now}
			// A fetched address takes over a fetched row (keeping its
			// enabled switch and test result) but never a manual one.
			err := tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "address"}},
				Where:     clause.Where{Exprs: []clause.Expression{clause.Neq{Column: "clean_ips.source", Value: Manual}}},
				DoUpdates: clause.AssignmentColumns([]string{"operator", "source", "last_seen", "updated_at"}),
			}).Create(&row).Error
			if err != nil {
				return err
			}
		}
		return tx.Where("source = ? AND last_seen < ?", key, now).Delete(&models.CleanIP{}).Error
	})
	src.Count = len(entries)
	src.LastError = ""
	if err != nil {
		src.LastError = err.Error()
	}
	s.DB.Save(src)
	return err
}

// Pick is the addresses CDN links are built with: enabled, the ones that
// passed a test from Iran first, then never-tested ones, newest first; the
// ones that failed their last test are left out.
func Pick(db *gorm.DB, limit int) ([]models.CleanIP, error) {
	if limit <= 0 {
		return nil, nil
	}
	var rows []models.CleanIP
	err := db.Where("enabled = ? AND (check_ok IS NULL OR check_ok = ?)", true, true).
		Order("CASE WHEN check_ok THEN 0 ELSE 1 END, last_seen DESC, id DESC").
		Limit(limit).Find(&rows).Error
	return rows, err
}
