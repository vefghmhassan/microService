// Package geoip resolves client addresses to country, province, city and ISP
// from local MMDB files (DB-IP Lite by default, any GeoLite2-compatible file
// works), so no address is ever sent to an outside service.
package geoip

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/oschwald/maxminddb-golang"
	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"

	"microserver/internal/models"
)

const (
	cityFile = "city.mmdb"
	asnFile  = "asn.mmdb"
)

// DefaultURLs are the free DB-IP Lite databases for month t (CC BY 4.0).
func DefaultURLs(t time.Time) (city, asn string) {
	m := t.UTC().Format("2006-01")
	return "https://download.db-ip.com/free/dbip-city-lite-" + m + ".mmdb.gz",
		"https://download.db-ip.com/free/dbip-asn-lite-" + m + ".mmdb.gz"
}

type DB struct {
	dir  string
	HTTP *http.Client

	mu       sync.RWMutex
	city     *maxminddb.Reader
	asn      *maxminddb.Reader
	updating sync.Mutex
	lastErr  string
}

// Open loads the databases already in dir, if any.
func Open(dir string) *DB {
	d := &DB{dir: dir, HTTP: &http.Client{Timeout: 15 * time.Minute}}
	_ = os.MkdirAll(dir, 0o755)
	d.reload()
	return d
}

func (d *DB) reload() {
	city, _ := maxminddb.Open(filepath.Join(d.dir, cityFile))
	asn, _ := maxminddb.Open(filepath.Join(d.dir, asnFile))
	d.mu.Lock()
	old := []*maxminddb.Reader{d.city, d.asn}
	d.city, d.asn = city, asn
	d.mu.Unlock()
	for _, r := range old {
		if r != nil {
			_ = r.Close()
		}
	}
}

// Status describes the loaded databases for the settings page.
type Status struct {
	City     bool      `json:"city"`
	ASN      bool      `json:"asn"`
	CityDate time.Time `json:"city_date"`
	ASNDate  time.Time `json:"asn_date"`
	Error    string    `json:"error"`
}

func (d *DB) Status() Status {
	d.mu.RLock()
	defer d.mu.RUnlock()
	st := Status{City: d.city != nil, ASN: d.asn != nil, Error: d.lastErr}
	if d.city != nil {
		st.CityDate = time.Unix(int64(d.city.Metadata.BuildEpoch), 0).UTC()
	}
	if d.asn != nil {
		st.ASNDate = time.Unix(int64(d.asn.Metadata.BuildEpoch), 0).UTC()
	}
	return st
}

// Ready reports whether lookups give at least a country.
func (d *DB) Ready() bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.city != nil
}

// Stale reports whether the databases are missing or older than maxAge.
func (d *DB) Stale(maxAge time.Duration) bool {
	st := d.Status()
	return !st.City || !st.ASN || time.Since(st.CityDate) > maxAge
}

type cityRecord struct {
	City struct {
		Names map[string]string `maxminddb:"names"`
	} `maxminddb:"city"`
	Country struct {
		ISOCode string `maxminddb:"iso_code"`
	} `maxminddb:"country"`
	Subdivisions []struct {
		Names map[string]string `maxminddb:"names"`
	} `maxminddb:"subdivisions"`
}

type asnRecord struct {
	Number uint   `maxminddb:"autonomous_system_number"`
	Org    string `maxminddb:"autonomous_system_organization"`
}

// Lookup resolves ip; ok is false when it is not a valid address or no
// database is loaded.
func (d *DB) Lookup(ip string) (g models.IPGeo, ok bool) {
	addr := net.ParseIP(ip)
	if addr == nil {
		return g, false
	}
	g.IP = ip
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.city == nil && d.asn == nil {
		return g, false
	}
	if d.city != nil {
		var r cityRecord
		if err := d.city.Lookup(addr, &r); err == nil {
			g.Country = r.Country.ISOCode
			g.City = clean(r.City.Names["en"])
			if len(r.Subdivisions) > 0 {
				g.Region = clean(r.Subdivisions[0].Names["en"])
			}
		}
	}
	if d.asn != nil {
		var r asnRecord
		if err := d.asn.Lookup(addr, &r); err == nil {
			g.ASN, g.ISP = r.Number, r.Org
		}
	}
	return g, true
}

// clean folds transliteration marks ("Tehrān" → "Tehran") and drops a
// trailing "(District 6)" so the same place groups as one.
func clean(s string) string {
	if i := strings.Index(s, " ("); i > 0 {
		s = s[:i]
	}
	out, _, err := transform.String(transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC), s)
	if err != nil {
		return strings.TrimSpace(s)
	}
	return strings.TrimSpace(out)
}

// Update downloads both databases and swaps them in. Empty URLs mean DB-IP
// Lite for this month, falling back to last month early in a month before
// the new files are published.
func (d *DB) Update(ctx context.Context, cityURL, asnURL string) error {
	d.updating.Lock()
	defer d.updating.Unlock()
	err := d.update(ctx, cityURL, asnURL)
	d.mu.Lock()
	d.lastErr = ""
	if err != nil {
		d.lastErr = err.Error()
	}
	d.mu.Unlock()
	return err
}

func (d *DB) update(ctx context.Context, cityURL, asnURL string) error {
	jobs := []struct{ url, file string }{{cityURL, cityFile}, {asnURL, asnFile}}
	now := time.Now()
	for i, j := range jobs {
		urls := []string{j.url}
		if j.url == "" {
			c1, a1 := DefaultURLs(now)
			c2, a2 := DefaultURLs(now.AddDate(0, -1, 0))
			urls = []string{c1, c2}
			if i == 1 {
				urls = []string{a1, a2}
			}
		}
		var err error
		for _, u := range urls {
			if err = d.download(ctx, u, j.file); err == nil {
				break
			}
		}
		if err != nil {
			return fmt.Errorf("%s: %w", j.file, err)
		}
	}
	d.reload()
	return nil
}

var errNotFound = errors.New("not found")

func (d *DB) download(ctx context.Context, url, name string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := d.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("%s: %w", url, errNotFound)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
	}
	var r io.Reader = resp.Body
	if strings.HasSuffix(strings.SplitN(url, "?", 2)[0], ".gz") {
		gz, err := gzip.NewReader(resp.Body)
		if err != nil {
			return err
		}
		defer gz.Close()
		r = gz
	}
	tmp, err := os.CreateTemp(d.dir, name+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, r); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// Refuse a file that is not a database before replacing a good one.
	check, err := maxminddb.Open(tmp.Name())
	if err != nil {
		return fmt.Errorf("%s: not an MMDB file: %w", url, err)
	}
	_ = check.Close()
	return os.Rename(tmp.Name(), filepath.Join(d.dir, name))
}
