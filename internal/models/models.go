// Package models is the M of the MVC split: GORM tables and their invariants.
package models

import (
	"time"

	"golang.org/x/crypto/bcrypt"

	"microserver/internal/services/secret"
)

type AdminUser struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	Username     string    `gorm:"uniqueIndex;size:64;not null" json:"username"`
	PasswordHash string    `gorm:"not null" json:"-"`
	CreatedAt    time.Time `json:"created_at"`
}

func (u *AdminUser) SetPassword(p string) error {
	h, err := bcrypt.GenerateFromPassword([]byte(p), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	u.PasswordHash = string(h)
	return nil
}

func (u *AdminUser) CheckPassword(p string) bool {
	return bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(p)) == nil
}

// ProviderAccount is one API credential at a cloud provider.
//
// Credentials holds the provider-specific secret as JSON (a single token for
// most, app key/secret/consumer key/project for OVH) and is encrypted at rest.
type ProviderAccount struct {
	ID            uint          `gorm:"primaryKey" json:"id"`
	Name          string        `gorm:"size:100;not null" json:"name"`
	Type          string        `gorm:"size:32;not null;index" json:"type"`
	Credentials   secret.String `json:"-"`
	DefaultRegion string        `gorm:"size:64" json:"default_region"`
	DefaultPlan   string        `gorm:"size:64" json:"default_plan"`
	DefaultImage  string        `gorm:"size:128" json:"default_image"`
	Country       string        `gorm:"size:2" json:"country"`
	Enabled       bool          `json:"enabled"`
	CreatedAt     time.Time     `json:"created_at"`
	UpdatedAt     time.Time     `json:"updated_at"`
}

// Location is one entry of the automatic placement list: where to try
// building servers, in Priority order (lowest first).
type Location struct {
	ID                uint             `gorm:"primaryKey" json:"id"`
	Priority          int              `gorm:"index" json:"priority"`
	ProviderAccountID uint             `gorm:"index" json:"provider_account_id"`
	ProviderAccount   *ProviderAccount `json:"provider_account,omitempty"`
	Region            string           `gorm:"size:64;not null" json:"region"`
	Plan              string           `gorm:"size:64;not null" json:"plan"`
	Image             string           `gorm:"size:128" json:"image"`
	Country           string           `gorm:"size:2" json:"country"`
	// Capacity is how many servers may be tried at once here. 0 means
	// "until the provider refuses", bounded by the auto.max_per_location setting.
	Capacity int  `json:"capacity"`
	Enabled  bool `json:"enabled"`
	// DNS: when CFAccountID is set, every server built here gets a numbered
	// subdomain of CFZoneName from DNSPattern ("fr{n}" → fr1.example.com),
	// and a replacement here takes over its predecessor's name.
	DNS
	Note      string    `gorm:"size:255" json:"note"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// DNS is the Cloudflare part of a Location, copied onto each server built
// there so editing or deleting the location never orphans a record.
type DNS struct {
	CFAccountID uint   `gorm:"column:cf_account_id;index" json:"cf_account_id"`
	CFZoneID    string `gorm:"column:cf_zone_id;size:64" json:"cf_zone_id"`
	CFZoneName  string `gorm:"column:cf_zone_name;size:255" json:"cf_zone_name"`
	DNSPattern  string `gorm:"column:dns_pattern;size:63" json:"dns_pattern"`
	DNSProxied  bool   `gorm:"column:dns_proxied" json:"dns_proxied"`
	// SSL pushes the zone's wildcard certificate to the server and serves
	// the 3x-ui panel over https.
	SSL bool `gorm:"column:ssl" json:"ssl"`
}

// On reports whether DNS is configured.
func (d DNS) On() bool { return d.CFAccountID != 0 && d.CFZoneID != "" && d.DNSPattern != "" }

// CloudflareAccount is one Cloudflare API token (Zone:Read + DNS:Edit).
type CloudflareAccount struct {
	ID    uint          `gorm:"primaryKey" json:"id"`
	Name  string        `gorm:"size:100;not null" json:"name"`
	Token secret.String `json:"-"`
	// AcmeEmail is the Let's Encrypt contact for this account's certificates.
	AcmeEmail string    `gorm:"size:255" json:"acme_email"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Certificate is the wildcard certificate of one zone (*.zone and zone),
// issued centrally over DNS-01 and copied to every server that uses it.
type Certificate struct {
	ID          uint          `gorm:"primaryKey" json:"id"`
	CFAccountID uint          `gorm:"column:cf_account_id;index" json:"cf_account_id"`
	CFZoneID    string        `gorm:"column:cf_zone_id;size:64" json:"cf_zone_id"`
	ZoneName    string        `gorm:"size:255;uniqueIndex" json:"zone_name"`
	CertPEM     string        `gorm:"type:text" json:"-"`
	KeyPEM      secret.String `json:"-"`
	AccountKey  secret.String `json:"-"` // the ACME account key (PEM)
	NotAfter    *time.Time    `json:"not_after"`
	IssuedAt    *time.Time    `json:"issued_at"`
	LastError   string        `gorm:"type:text" json:"last_error"`
	CreatedAt   time.Time     `json:"created_at"`
	UpdatedAt   time.Time     `json:"updated_at"`
}

// Server lifecycle states.
const (
	ServerCreating    = "creating"
	ServerChecking    = "checking"
	ServerInstalling  = "installing"
	ServerConfiguring = "configuring"
	ServerActive      = "active"
	ServerFiltered    = "filtered"
	ServerRotating    = "rotating"
	ServerError       = "error"
	ServerDeleting    = "deleting"
	ServerDeleted     = "deleted"
)

// Template sync states for a server.
const (
	SyncPending = "pending"
	SyncSynced  = "synced"
	SyncDrift   = "drift"
	SyncFailed  = "failed"
)

type Server struct {
	ID                uint             `gorm:"primaryKey" json:"id"`
	Name              string           `gorm:"size:100;not null" json:"name"`
	ProviderAccountID uint             `gorm:"index" json:"provider_account_id"`
	ProviderAccount   *ProviderAccount `json:"provider_account,omitempty"`
	ProviderRef       string           `gorm:"size:64;index" json:"provider_ref"`
	Region            string           `gorm:"size:64" json:"region"`
	Plan              string           `gorm:"size:64" json:"plan"`
	Image             string           `gorm:"size:128" json:"image"`
	Country           string           `gorm:"size:2" json:"country"`
	Status            string           `gorm:"size:20;index" json:"status"`
	IPv4              string           `gorm:"size:45;index" json:"ipv4"`
	IPRef             string           `gorm:"size:64" json:"ip_ref"`
	SSHHostKey        string           `gorm:"type:text" json:"-"`
	// LocationID is the placement entry the server was built for (0 = manual).
	LocationID uint `gorm:"index" json:"location_id"`
	DNS
	// Hostname is the server's subdomain (empty without DNS); links and
	// uploads use it instead of the IP.
	Hostname     string     `gorm:"size:255;index" json:"hostname"`
	DNSRecordID  string     `gorm:"column:dns_record_id;size:64" json:"dns_record_id"`
	DNSError     string     `gorm:"column:dns_error;size:255" json:"dns_error"`
	CertNotAfter *time.Time `json:"cert_not_after"`

	XUIPort     int           `gorm:"column:xui_port" json:"xui_port"`
	XUIBasePath string        `gorm:"column:xui_base_path;size:64" json:"xui_base_path"`
	XUIUsername string        `gorm:"column:xui_username;size:64" json:"xui_username"`
	XUIPassword secret.String `gorm:"column:xui_password" json:"-"`
	XUIToken    secret.String `gorm:"column:xui_token" json:"-"`
	XUIScheme   string        `gorm:"column:xui_scheme;size:8" json:"xui_scheme"`
	// XUIPublic is true when the panel listens on the public IP (reachable in
	// a browser); otherwise only through the SSH tunnel.
	XUIPublic    bool       `gorm:"column:xui_public" json:"xui_public"`
	PanelVersion string     `gorm:"size:32" json:"panel_version"`
	XrayVersion  string     `gorm:"size:32" json:"xray_version"`
	WantPanelVer string     `gorm:"size:32" json:"want_panel_version"`
	WantXrayVer  string     `gorm:"size:32" json:"want_xray_version"`
	AppliedHash  string     `gorm:"size:64" json:"applied_hash"`
	SyncStatus   string     `gorm:"size:16" json:"sync_status"`
	UploadedAt   *time.Time `json:"uploaded_at"`
	// UploadedIP is the address the links in vpn-pannel point at: the IP, or
	// the hostname when the server has one.
	UploadedIP    string     `gorm:"size:255" json:"uploaded_ip"`
	LastCheckAt   *time.Time `json:"last_check_at"`
	LastCheckOK   *bool      `json:"last_check_ok"`
	LastCheckNote string     `gorm:"size:255" json:"last_check_note"`
	RotateCount   int        `json:"rotate_count"`
	// Traffic and online users as last read from 3x-ui (all inbounds).
	TrafficUp   int64      `json:"traffic_up"`
	TrafficDown int64      `json:"traffic_down"`
	OnlineCount int        `json:"online_count"`
	StatsAt     *time.Time `json:"stats_at"`
	StatsError  string     `gorm:"size:255" json:"stats_error"`
	// How the last client-address collection for the GeoIP report went.
	IPCollectAt   *time.Time `json:"ip_collect_at"`
	IPCollectNote string     `gorm:"size:255" json:"ip_collect_note"`
	// TrafficLimitGB overrides the global cap (0 = use the setting).
	TrafficLimitGB int `json:"traffic_limit_gb"`
	// ReplacingAt is set once a replacement was queued, so the cap fires once.
	ReplacingAt *time.Time `json:"replacing_at"`
	LastError   string     `gorm:"type:text" json:"last_error"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// InboundStat is the latest traffic/online reading of one inbound on a server.
type InboundStat struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	ServerID  uint      `gorm:"uniqueIndex:idx_inbound_stat" json:"server_id"`
	Port      int       `gorm:"uniqueIndex:idx_inbound_stat" json:"port"`
	Remark    string    `gorm:"size:100" json:"remark"`
	Protocol  string    `gorm:"size:16" json:"protocol"`
	Enabled   bool      `json:"enabled"`
	Up        int64     `json:"up"`
	Down      int64     `json:"down"`
	Clients   int       `json:"clients"`
	Online    int       `json:"online"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Destination is one vpn-pannel admin that generated configs are pushed to.
// Every enabled destination gets every upload, IP move and deactivation.
type Destination struct {
	ID       uint          `gorm:"primaryKey" json:"id"`
	Name     string        `gorm:"size:100;not null" json:"name"`
	URL      string        `gorm:"size:255;not null" json:"url"`
	Email    string        `gorm:"size:255" json:"email"`
	Password secret.String `json:"-"`
	// AdsMode is what each server's configs are uploaded as: "normal",
	// "ads" or "both" (two sets, one of each).
	AdsMode string `gorm:"size:10" json:"ads_mode"`
	// Ads is the boolean of older versions, read when AdsMode is empty.
	Ads       bool      `json:"-"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Ads modes of a destination.
const (
	AdsNormal = "normal"
	AdsOnly   = "ads"
	AdsBoth   = "both"
)

// AdsValues lists the ads flag of every upload this destination gets.
func (d Destination) AdsValues() []bool {
	switch d.Mode() {
	case AdsOnly:
		return []bool{true}
	case AdsBoth:
		return []bool{false, true}
	}
	return []bool{false}
}

// Mode is AdsMode, falling back to the older Ads flag.
func (d Destination) Mode() string {
	switch d.AdsMode {
	case AdsNormal, AdsOnly, AdsBoth:
		return d.AdsMode
	}
	if d.Ads {
		return AdsOnly
	}
	return AdsNormal
}

// ClientIP is one client address seen in a server's xray access log, per
// user and day. Conns counts accepted connections that day.
type ClientIP struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	ServerID  uint      `gorm:"uniqueIndex:idx_client_ip;index" json:"server_id"`
	Day       string    `gorm:"size:10;uniqueIndex:idx_client_ip" json:"day"`
	IP        string    `gorm:"size:45;uniqueIndex:idx_client_ip;index" json:"ip"`
	Email     string    `gorm:"size:100;uniqueIndex:idx_client_ip;index" json:"email"`
	Inbound   string    `gorm:"size:100" json:"inbound"`
	Conns     int64     `json:"conns"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `gorm:"index" json:"last_seen"`
}

// IPGeo caches the GeoIP answer for a client address.
type IPGeo struct {
	IP        string    `gorm:"primaryKey;size:45" json:"ip"`
	Country   string    `gorm:"size:2" json:"country"`
	Region    string    `gorm:"size:100" json:"region"`
	City      string    `gorm:"size:100" json:"city"`
	ASN       uint      `json:"asn"`
	ISP       string    `gorm:"size:150" json:"isp"`
	UpdatedAt time.Time `json:"updated_at"`
}

// IP pool states.
const (
	IPInUse    = "in_use"   // attached to a live server
	IPFiltered = "filtered" // was detached because Iran could not reach it; still owned
	IPKept     = "kept"     // owned and unassigned, waiting to be retried
	IPReleased = "released" // given back to the provider
)

// IPAddress records every public IP a server has held, so a filtered address
// can be kept for a while and retried once it is unblocked.
type IPAddress struct {
	ID                uint       `gorm:"primaryKey" json:"id"`
	ProviderAccountID uint       `gorm:"index" json:"provider_account_id"`
	Address           string     `gorm:"size:45;index" json:"address"`
	Ref               string     `gorm:"size:64" json:"ref"`
	Region            string     `gorm:"size:64" json:"region"`
	Status            string     `gorm:"size:16;index" json:"status"`
	Reusable          bool       `json:"reusable"`
	ServerID          *uint      `gorm:"index" json:"server_id"`
	FilteredAt        *time.Time `json:"filtered_at"`
	LastCheckAt       *time.Time `json:"last_check_at"`
	LastCheckOK       *bool      `json:"last_check_ok"`
	Note              string     `gorm:"size:255" json:"note"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

// CheckRun is one reachability verdict for host:port, with the probes behind it.
type CheckRun struct {
	ID        uint         `gorm:"primaryKey" json:"id"`
	ServerID  *uint        `gorm:"index" json:"server_id"`
	Host      string       `gorm:"size:255" json:"host"`
	Port      int          `json:"port"`
	Verdict   string       `gorm:"size:16" json:"verdict"`
	OK        bool         `json:"ok"`
	Summary   string       `gorm:"size:255" json:"summary"`
	Probes    []CheckProbe `json:"probes"`
	CreatedAt time.Time    `gorm:"index" json:"created_at"`
}

type CheckProbe struct {
	ID         uint    `gorm:"primaryKey" json:"id"`
	CheckRunID uint    `gorm:"index" json:"-"`
	Source     string  `gorm:"size:32" json:"source"`
	Node       string  `gorm:"size:100" json:"node"`
	Country    string  `gorm:"size:2" json:"country"`
	City       string  `gorm:"size:64" json:"city"`
	ASN        string  `gorm:"size:16" json:"asn"`
	Network    string  `gorm:"size:100" json:"network"`
	OK         bool    `json:"ok"`
	NoAnswer   bool    `json:"no_answer"`
	LatencyMs  float64 `json:"latency_ms"`
	Error      string  `gorm:"size:255" json:"error"`
}

// XrayTemplate is the singleton (ID=1) holding the xray parts every panel shares.
type XrayTemplate struct {
	ID        uint   `gorm:"primaryKey" json:"id"`
	Outbounds string `gorm:"type:text" json:"outbounds"`
	Routing   string `gorm:"type:text" json:"routing"`
	DNS       string `gorm:"type:text" json:"dns"`
	// Observatory probes outbounds for leastPing/leastLoad balancers.
	Observatory string `gorm:"type:text" json:"observatory"`
	// Full, when set, is the whole xray template (3x-ui's "Advanced" view):
	// panels get exactly this instead of having the keys above merged in.
	Full      string    `gorm:"type:text" json:"full"`
	Version   int       `json:"version"`
	Hash      string    `gorm:"size:64" json:"hash"`
	UpdatedAt time.Time `json:"updated_at"`
}

// InboundTemplate is one inbound created identically on every panel. Settings
// already carries its clients so links are the same on every server.
type InboundTemplate struct {
	ID             uint   `gorm:"primaryKey" json:"id"`
	Remark         string `gorm:"size:100;not null" json:"remark"`
	Protocol       string `gorm:"size:16;not null" json:"protocol"`
	Port           int    `gorm:"uniqueIndex;not null" json:"port"`
	Enabled        bool   `json:"enabled"`
	Settings       string `gorm:"type:text" json:"settings"`
	StreamSettings string `gorm:"type:text" json:"stream_settings"`
	Sniffing       string `gorm:"type:text" json:"sniffing"`
	Upload         bool   `json:"upload"`
	// UploadJSON sends this inbound to vpn-pannel as full client JSON configs instead of share links.
	UploadJSON bool      `json:"upload_json"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// Job states.
const (
	JobQueued   = "queued"
	JobRunning  = "running"
	JobDone     = "done"
	JobFailed   = "failed"
	JobCanceled = "canceled"
)

type Job struct {
	ID         uint       `gorm:"primaryKey" json:"id"`
	Kind       string     `gorm:"size:32;index" json:"kind"`
	Status     string     `gorm:"size:16;index" json:"status"`
	ServerID   *uint      `gorm:"index" json:"server_id"`
	Params     string     `gorm:"type:text" json:"params"`
	State      string     `gorm:"type:text" json:"-"`
	Step       string     `gorm:"size:64" json:"step"`
	Error      string     `gorm:"type:text" json:"error"`
	Source     string     `gorm:"size:16" json:"source"`
	Logs       []JobLog   `json:"logs,omitempty"`
	StartedAt  *time.Time `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
	CreatedAt  time.Time  `gorm:"index" json:"created_at"`
}

type JobLog struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	JobID     uint      `gorm:"index" json:"job_id"`
	Level     string    `gorm:"size:8" json:"level"`
	Message   string    `gorm:"type:text" json:"message"`
	CreatedAt time.Time `json:"created_at"`
}

// Setting is a key/value row; values of secret keys are stored encrypted by
// the settings service.
type Setting struct {
	Key       string    `gorm:"primaryKey;size:64" json:"key"`
	Value     string    `gorm:"type:text" json:"value"`
	UpdatedAt time.Time `json:"updated_at"`
}

func All() []any {
	return []any{
		&AdminUser{}, &ProviderAccount{}, &Location{}, &Server{}, &InboundStat{}, &IPAddress{},
		&CheckRun{}, &CheckProbe{}, &XrayTemplate{}, &InboundTemplate{},
		&Job{}, &JobLog{}, &Setting{}, &ClientIP{}, &IPGeo{}, &Destination{},
		&CloudflareAccount{}, &Certificate{},
	}
}
