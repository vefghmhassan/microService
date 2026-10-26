// Package orchestrator implements the server lifecycle as background jobs:
// provision → check from Iran → install 3x-ui → apply the shared template →
// upload links to vpn-pannel, plus rotation, sync, upgrades and deletion.
package orchestrator

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"microserver/internal/jobs"
	"microserver/internal/models"
	"microserver/internal/services/checker"
	"microserver/internal/services/destination"
	"microserver/internal/services/providers"
	"microserver/internal/services/settings"
	"microserver/internal/services/sshx"
	"microserver/internal/services/xui"
)

// Job kinds.
const (
	KindProvision     = "provision"
	KindRotate        = "rotate"
	KindCheck         = "check"
	KindInstall       = "install"
	KindSync          = "sync"
	KindSyncAll       = "sync_all"
	KindUpload        = "upload"
	KindDelete        = "delete"
	KindXrayVersion   = "xray_version"
	KindPanelVersion  = "panel_version"
	KindCheckAll      = "check_all"
	KindProvisionAuto = "provision_auto"
	KindPanelAccess   = "panel_access"
)

// Remote is the SSH session the orchestrator needs; sshx.Client satisfies it
// and tests substitute a fake.
type Remote interface {
	Run(ctx context.Context, cmd string, logLine func(string)) (string, error)
	ReadFile(ctx context.Context, path string) (string, error)
	DialContext(ctx context.Context, network, addr string) (net.Conn, error)
	Close() error
}

// Panel is the 3x-ui API surface used here; *xui.Client satisfies it.
type Panel interface {
	ListInbounds(ctx context.Context) ([]xui.Inbound, error)
	Onlines(ctx context.Context) ([]string, error)
	DisableSubscription(ctx context.Context) (bool, error)
	RestartPanel(ctx context.Context) error
	AddInbound(ctx context.Context, in xui.Inbound) error
	UpdateInbound(ctx context.Context, id int, in xui.Inbound) error
	DeleteInbound(ctx context.Context, id int) error
	XraySetting(ctx context.Context) (map[string]any, error)
	UpdateXraySetting(ctx context.Context, setting map[string]any) error
	RestartXray(ctx context.Context) error
	XrayVersions(ctx context.Context) ([]string, error)
	InstallXray(ctx context.Context, version string) error
	Status(ctx context.Context) (*xui.Status, error)
}

type Orchestrator struct {
	DB       *gorm.DB
	Settings *settings.Store
	Key      *sshx.Keypair
	Engine   *jobs.Engine

	// Seams, replaced in tests.
	Provider func(acc *models.ProviderAccount) (providers.Provider, error)
	Connect  func(ctx context.Context, s *models.Server) (Remote, string, error) // returns the host key seen
	PanelFor func(s *models.Server, r Remote) Panel
	Check    func(ctx context.Context, host string, port int) *checker.Result
	Dests    func() []NamedDest // every enabled vpn-pannel
	Notify   func(text string)
	// Geo resolves client addresses for the GeoIP report (nil = skip).
	Geo GeoLookup

	accessLogOK sync.Map // server ID → access log verified on
	xrayKicks   sync.Map // server ID → *int restarts tried for a missing access log

	// SSHWait bounds how long a fresh server may take to accept SSH.
	SSHWait time.Duration
	// RetryDelay is the pause between reachability attempts.
	RetryDelay time.Duration
}

// Destination is the vpn-pannel client surface.
type Destination interface {
	Upload(ctx context.Context, in destination.UploadRequest) error
	ReplaceAddress(ctx context.Context, oldIP, newIP, country string) (*destination.ReplaceResult, error)
	SetActive(ctx context.Context, address string, active bool) error
}

func New(db *gorm.DB, st *settings.Store, key *sshx.Keypair, engine *jobs.Engine) *Orchestrator {
	o := &Orchestrator{DB: db, Settings: st, Key: key, Engine: engine, SSHWait: 10 * time.Minute, RetryDelay: 20 * time.Second}
	o.Provider = func(acc *models.ProviderAccount) (providers.Provider, error) {
		return providers.New(acc.Type, json.RawMessage(acc.Credentials))
	}
	o.Connect = func(ctx context.Context, s *models.Server) (Remote, string, error) {
		c, err := sshx.DialRetry(ctx, s.IPv4, 22, key, s.SSHHostKey, o.SSHWait)
		if err != nil {
			return nil, "", err
		}
		return c, c.HostKey, nil
	}
	o.PanelFor = func(s *models.Server, r Remote) Panel {
		return xui.New(s.XUIScheme, "127.0.0.1", s.XUIPort, s.XUIBasePath, string(s.XUIToken), r.DialContext)
	}
	o.Check = o.defaultCheck
	o.Dests = o.defaultDests
	return o
}

// NewChecker builds a checker from the current settings.
func (o *Orchestrator) NewChecker() *checker.Checker {
	c := &checker.Checker{Policy: checker.Policy{
		MinOK: o.Settings.Int(settings.CheckMinOK), MinASN: o.Settings.Int(settings.CheckMinASN),
	}}
	for _, s := range o.Settings.List(settings.CheckSources) {
		switch s {
		case "checkhost":
			c.Sources = append(c.Sources, checker.NewCheckHost())
		case "globalping":
			c.Sources = append(c.Sources, checker.NewGlobalping(o.Settings.Get(settings.GlobalpingToken), o.Settings.Int(settings.GlobalpingLimit)))
		}
	}
	return c
}

func (o *Orchestrator) defaultCheck(ctx context.Context, host string, port int) *checker.Result {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	return o.NewChecker().Run(ctx, host, port)
}

// NamedDest is one enabled vpn-pannel with its per-panel upload options.
type NamedDest struct {
	Name string
	// Ads lists one ads flag per upload: [false], [true], or both for a
	// panel that keeps a normal and an ads set of every server's configs.
	Ads []bool
	Destination
}

func (o *Orchestrator) defaultDests() []NamedDest {
	var rows []models.Destination
	o.DB.Where("enabled = ?", true).Order("id").Find(&rows)
	out := make([]NamedDest, 0, len(rows))
	for _, d := range rows {
		out = append(out, NamedDest{Name: d.Name, Ads: d.AdsValues(), Destination: destination.NewVPNPanel(d.URL, d.Email, string(d.Password))})
	}
	return out
}

// Register wires every job kind into the engine.
func (o *Orchestrator) Register() {
	o.Engine.Register(KindProvision, o.provision)
	o.Engine.Register(KindRotate, o.rotate)
	o.Engine.Register(KindCheck, o.check)
	o.Engine.Register(KindInstall, o.install)
	o.Engine.Register(KindSync, o.sync)
	o.Engine.Register(KindSyncAll, o.syncAll)
	o.Engine.Register(KindUpload, o.upload)
	o.Engine.Register(KindDelete, o.delete)
	o.Engine.Register(KindXrayVersion, o.xrayVersion)
	o.Engine.Register(KindPanelVersion, o.panelVersion)
	o.Engine.Register(KindCheckAll, o.checkAll)
	o.Engine.Register(KindProvisionAuto, o.provisionAuto)
	o.Engine.Register(KindPanelAccess, o.panelAccess)
}

func serverEvent(s *models.Server) jobs.Event {
	return jobs.Event{Type: "server", ServerID: &s.ID, Data: s}
}

func (o *Orchestrator) notify(format string, args ...any) {
	if o.Notify != nil {
		o.Notify(fmt.Sprintf(format, args...))
	}
}

// ---- shared helpers ----

func (o *Orchestrator) loadServer(r *jobs.Run) (*models.Server, error) {
	if r.Job.ServerID == nil {
		return nil, errors.New("job has no server")
	}
	var s models.Server
	if err := o.DB.Preload("ProviderAccount").First(&s, *r.Job.ServerID).Error; err != nil {
		return nil, fmt.Errorf("server %d: %w", *r.Job.ServerID, err)
	}
	if s.ProviderAccount == nil {
		return nil, errors.New("server has no provider account")
	}
	return &s, nil
}

func (o *Orchestrator) save(r *jobs.Run, s *models.Server, fields ...string) {
	if len(fields) == 0 {
		o.DB.Save(s)
	} else {
		o.DB.Model(s).Select(fields).Updates(s)
	}
	if r != nil {
		r.PublishServer(s)
	}
}

func (o *Orchestrator) setStatus(r *jobs.Run, s *models.Server, status string) {
	s.Status = status
	o.save(r, s, "status")
}

func (o *Orchestrator) fail(r *jobs.Run, s *models.Server, err error) error {
	s.Status = models.ServerError
	s.LastError = err.Error()
	o.save(r, s, "status", "last_error")
	return err
}

// recordCheck stores a check and updates the server's last-check fields.
func (o *Orchestrator) recordCheck(r *jobs.Run, s *models.Server, res *checker.Result) {
	run := models.CheckRun{Host: res.Host, Port: res.Port, Verdict: res.Verdict, OK: res.OK, Summary: truncate(res.Summary, 255)}
	if s != nil {
		run.ServerID = &s.ID
	}
	for _, p := range res.Probes {
		run.Probes = append(run.Probes, models.CheckProbe{
			Source: p.Source, Node: p.Node, Country: p.Country, City: p.City, ASN: p.ASN,
			Network: truncate(p.Network, 100), OK: p.OK, NoAnswer: p.NoAnswer, LatencyMs: p.LatencyMs, Error: truncate(p.Error, 255),
		})
	}
	o.DB.Create(&run)
	if r != nil {
		r.Info("check %s:%d → %s", res.Host, res.Port, res.Summary)
		for _, e := range res.Errors {
			r.Logf("warn", "check source error: %s", e)
		}
	}
	if s != nil {
		now := time.Now()
		ok := res.OK
		s.LastCheckAt, s.LastCheckOK, s.LastCheckNote = &now, &ok, truncate(res.Summary, 255)
		o.save(r, s, "last_check_at", "last_check_ok", "last_check_note")
		o.DB.Model(&models.IPAddress{}).Where("address = ? AND server_id = ?", s.IPv4, s.ID).
			Updates(map[string]any{"last_check_at": now, "last_check_ok": ok})
	}
}

// checkPort is where reachability is measured: the first enabled inbound once
// 3x-ui is configured, else sshd (the only thing listening on a fresh server).
// Iranian filtering is usually per IP, so either port answers the question.
func (o *Orchestrator) checkPort(s *models.Server) int {
	if s.AppliedHash != "" {
		var in models.InboundTemplate
		if o.DB.Where("enabled = ?", true).Order("id").First(&in).Error == nil {
			return in.Port
		}
	}
	if p := o.Settings.Int(settings.CheckPreInstall); p > 0 {
		return p
	}
	return 22
}

func (o *Orchestrator) poolUse(s *models.Server) {
	if s.IPv4 == "" {
		return
	}
	var ip models.IPAddress
	err := o.DB.Where("address = ? AND provider_account_id = ?", s.IPv4, s.ProviderAccountID).First(&ip).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		ip = models.IPAddress{Address: s.IPv4, ProviderAccountID: s.ProviderAccountID}
	}
	ip.Ref = s.IPRef
	ip.Region = s.Region
	ip.Status = models.IPInUse
	ip.ServerID = &s.ID
	o.DB.Save(&ip)
}

// poolRetire records what happened to an IP the server just left.
func (o *Orchestrator) poolRetire(s *models.Server, addr, ref string, kept, filtered bool, reusable bool, note string) {
	if addr == "" {
		return
	}
	var ip models.IPAddress
	err := o.DB.Where("address = ? AND provider_account_id = ?", addr, s.ProviderAccountID).First(&ip).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		ip = models.IPAddress{Address: addr, ProviderAccountID: s.ProviderAccountID, Region: s.Region}
	}
	now := time.Now()
	ip.Ref = ref
	ip.ServerID = nil
	ip.Reusable = kept && reusable
	ip.Note = truncate(note, 255)
	switch {
	case kept && filtered:
		ip.Status = models.IPFiltered
		ip.FilteredAt = &now
	case kept:
		ip.Status = models.IPKept
	default:
		ip.Status = models.IPReleased
	}
	if filtered && ip.FilteredAt == nil {
		ip.FilteredAt = &now
	}
	o.DB.Save(&ip)
}

// pickReusable returns a kept IP in the region whose filtering is old enough to
// be worth another try.
func (o *Orchestrator) pickReusable(accountID uint, region string) *models.IPAddress {
	hours := o.Settings.Int(settings.RotateRetryHours)
	cutoff := time.Now().Add(-time.Duration(hours) * time.Hour)
	var ip models.IPAddress
	err := o.DB.Where("provider_account_id = ? AND region = ? AND reusable = ? AND status IN ? AND (filtered_at IS NULL OR filtered_at < ?)",
		accountID, region, true, []string{models.IPFiltered, models.IPKept}, cutoff).
		Order("filtered_at").First(&ip).Error
	if err != nil {
		return nil
	}
	return &ip
}

func randomSuffix(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// sanitizeName keeps names valid as hostnames at every provider.
func sanitizeName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 50 {
		out = out[:50]
	}
	return out
}

// cloudInit installs our key for root (the default user differs per image) and
// leaves everything else to the SSH install step, where its output is visible.
func cloudInit(pubKey string) string {
	return fmt.Sprintf(`#cloud-config
disable_root: false
ssh_pwauth: false
runcmd:
  - mkdir -p /root/.ssh && chmod 700 /root/.ssh
  - grep -qF '%[1]s' /root/.ssh/authorized_keys 2>/dev/null || echo '%[1]s' >> /root/.ssh/authorized_keys
  - chmod 600 /root/.ssh/authorized_keys
  - sed -i 's/^#\?PermitRootLogin.*/PermitRootLogin prohibit-password/' /etc/ssh/sshd_config
  - systemctl reload ssh || systemctl reload sshd || true
`, strings.TrimSpace(pubKey))
}

func (o *Orchestrator) createRequest(s *models.Server) providers.CreateRequest {
	return providers.CreateRequest{
		Name: s.Name, Region: s.Region, Plan: s.Plan, Image: s.Image,
		SSHPublicKey: o.Key.PublicKey, UserData: cloudInit(o.Key.PublicKey),
	}
}

func instanceOf(s *models.Server) *providers.Instance {
	return &providers.Instance{Ref: s.ProviderRef, Name: s.Name, IPv4: s.IPv4, IPRef: s.IPRef, Region: s.Region, Status: "running"}
}

func sleep(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}
