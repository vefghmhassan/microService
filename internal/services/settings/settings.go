// Package settings is the typed view of the key/value settings table.
package settings

import (
	"strconv"
	"strings"
	"sync"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"microserver/internal/models"
	"microserver/internal/services/secret"
)

// Keys. Every key the UI may edit is listed in Defaults.
const (
	XUIVersion        = "xui.version"
	XUIInstallURL     = "xui.install_url"
	XUIPublic         = "xui.public_panel"
	XrayVersion       = "xray.version"
	CheckSources      = "check.sources"
	CheckMinOK        = "check.min_ok"
	CheckMinASN       = "check.min_asn"
	CheckPreInstall   = "check.pre_install_port"
	CheckIntervalMin  = "check.interval_min"
	GlobalpingToken   = "check.globalping_token"
	GlobalpingLimit   = "check.globalping_limit"
	AtlasKey          = "check.atlas_key"
	AtlasLimit        = "check.atlas_limit"
	RotateMaxAttempts = "rotate.max_attempts"
	RotateKeepOld     = "rotate.keep_filtered_ip"
	RotateRetryHours  = "rotate.retry_kept_after_hours"
	AutoRotate        = "rotate.auto"
	RotateFallback    = "rotate.fallback_location"
	AutoMaxPerLoc     = "auto.max_per_location"
	TrafficLimitGB    = "traffic.limit_gb"
	TrafficAuto       = "traffic.auto_replace"
	StatsIntervalMin  = "stats.interval_min"
	VPNPanelURL       = "vpnpannel.url"
	VPNPanelEmail     = "vpnpannel.email"
	VPNPanelPassword  = "vpnpannel.password"
	VPNPanelAds       = "vpnpannel.ads"
	VPNPanelAuto      = "vpnpannel.auto_upload"
	TelegramToken     = "telegram.token"
	TelegramChatIDs   = "telegram.chat_ids"
	TelegramProxy     = "telegram.proxy"
	TelegramNotify    = "telegram.notify"
	GeoIPCityURL      = "geoip.city_url"
	GeoIPASNURL       = "geoip.asn_url"
	AcmeStaging       = "acme.staging"
	CleanIPOperators  = "cleanip.operators"
	CleanIPInterval   = "cleanip.interval_hours"
	CleanIPPerLink    = "cleanip.per_link"
)

var Defaults = map[string]string{
	XUIVersion:       "v3.7.0",
	XUIInstallURL:    "https://raw.githubusercontent.com/mhsanaei/3x-ui/master/install.sh",
	XUIPublic:        "true",
	XrayVersion:      "",
	CheckSources:     "checkhost,globalping",
	CheckMinOK:       "2",
	CheckMinASN:      "2",
	CheckPreInstall:  "22",
	CheckIntervalMin: "30",
	GlobalpingToken:  "",
	GlobalpingLimit:  "8",
	// RIPE Atlas has Iranian probes with IPv6; add "atlas" to check.sources.
	AtlasKey:          "",
	AtlasLimit:        "8",
	RotateMaxAttempts: "5",
	RotateKeepOld:     "false",
	RotateRetryHours:  "24",
	AutoRotate:        "false",
	RotateFallback:    "true",
	AutoMaxPerLoc:     "5",
	TrafficLimitGB:    "0",
	TrafficAuto:       "false",
	StatsIntervalMin:  "5",
	VPNPanelURL:       "",
	VPNPanelEmail:     "",
	VPNPanelPassword:  "",
	VPNPanelAds:       "false",
	VPNPanelAuto:      "true",
	TelegramToken:     "",
	TelegramChatIDs:   "",
	TelegramProxy:     "",
	TelegramNotify:    "true",
	// Empty: DB-IP Lite of the current month.
	GeoIPCityURL: "",
	GeoIPASNURL:  "",
	// Let's Encrypt's staging CA: untrusted certificates, no rate limits.
	AcmeStaging: "false",
	// Operators taken from the built-in clean-IP list (empty: all).
	CleanIPOperators: "MCI,MTN",
	CleanIPInterval:  "6",
	// How many clean IPs a proxied server's CDN links are built with.
	CleanIPPerLink: "5",
}

// Secret keys are encrypted in the table and masked when listed.
var Secret = map[string]bool{
	GlobalpingToken:  true,
	AtlasKey:         true,
	VPNPanelPassword: true,
	TelegramToken:    true,
}

const Mask = "••••••"

type Store struct {
	db        *gorm.DB
	mu        sync.RWMutex
	cache     map[string]string
	listeners []func(changed []string)
}

func New(db *gorm.DB) (*Store, error) {
	s := &Store{db: db, cache: map[string]string{}}
	var rows []models.Setting
	if err := db.Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, r := range rows {
		v := r.Value
		if Secret[r.Key] {
			plain, err := secret.Decrypt(v)
			if err != nil {
				return nil, err
			}
			v = plain
		}
		s.cache[r.Key] = v
	}
	return s, nil
}

// OnChange registers a callback run after Set with the keys that changed.
func (s *Store) OnChange(f func(changed []string)) {
	s.mu.Lock()
	s.listeners = append(s.listeners, f)
	s.mu.Unlock()
}

func (s *Store) Get(key string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if v, ok := s.cache[key]; ok {
		return v
	}
	return Defaults[key]
}

func (s *Store) Int(key string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s.Get(key)))
	if err != nil {
		n, _ = strconv.Atoi(Defaults[key])
	}
	return n
}

func (s *Store) Bool(key string) bool {
	return strings.EqualFold(strings.TrimSpace(s.Get(key)), "true")
}

func (s *Store) List(key string) []string {
	var out []string
	for _, p := range strings.Split(s.Get(key), ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Public returns every known key with secrets masked (empty stays empty so the
// UI can tell "not set" from "set").
func (s *Store) Public() map[string]string {
	out := map[string]string{}
	for k := range Defaults {
		v := s.Get(k)
		if Secret[k] && v != "" {
			v = Mask
		}
		out[k] = v
	}
	return out
}

// Set writes known keys. A secret sent back as the mask is left unchanged, and
// unlike vpn-pannel an empty value is a real value: it clears the setting.
func (s *Store) Set(values map[string]string) error {
	var changed []string
	err := s.db.Transaction(func(tx *gorm.DB) error {
		for k, v := range values {
			if _, known := Defaults[k]; !known {
				continue
			}
			if Secret[k] && v == Mask {
				continue
			}
			v = strings.TrimSpace(v)
			stored := v
			if Secret[k] {
				enc, err := secret.Encrypt(v)
				if err != nil {
					return err
				}
				stored = enc
			}
			row := models.Setting{Key: k, Value: stored}
			if err := tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&row).Error; err != nil {
				return err
			}
			if s.Get(k) != v {
				changed = append(changed, k)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.mu.Lock()
	for k, v := range values {
		if _, known := Defaults[k]; !known || (Secret[k] && v == Mask) {
			continue
		}
		s.cache[k] = strings.TrimSpace(v)
	}
	listeners := append([]func([]string){}, s.listeners...)
	s.mu.Unlock()
	if len(changed) > 0 {
		for _, f := range listeners {
			f(changed)
		}
	}
	return nil
}
