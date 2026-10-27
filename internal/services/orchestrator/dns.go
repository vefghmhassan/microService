package orchestrator

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"microserver/internal/jobs"
	"microserver/internal/models"
	"microserver/internal/services/cloudflare"
	"microserver/internal/services/sshx"
)

// Where a server keeps the zone certificate. Inbound templates that use TLS
// point at these paths, so one template serves every server.
const (
	CertFile = "/root/cert/fullchain.pem"
	KeyFile  = "/root/cert/privkey.pem"
)

// DNSAPI is the Cloudflare record surface; *cloudflare.Client satisfies it.
type DNSAPI interface {
	UpsertA(ctx context.Context, zoneID, name, ip string, proxied bool) (string, error)
	Records(ctx context.Context, zoneID, typ, name string) ([]cloudflare.Record, error)
	DeleteRecord(ctx context.Context, zoneID, id string) error
}

// CertSource hands out a zone's wildcard certificate; *certs.Manager
// satisfies it.
type CertSource interface {
	Ensure(ctx context.Context, accountID uint, zoneID, zoneName string, logf func(string, ...any)) (*models.Certificate, bool, error)
}

// LinkHost is the address a server's links point at: its hostname when it
// has one, else its IP.
func LinkHost(s *models.Server) string {
	return firstNonEmpty(s.Hostname, s.IPv4)
}

// HostnameFor turns a pattern like "fr{n}" into fr3.example.com.
func HostnameFor(pattern string, n int, zone string) string {
	label := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(pattern)), "{n}", strconv.Itoa(n))
	return label + "." + strings.TrimSuffix(strings.ToLower(zone), ".")
}

// ValidPattern checks a subdomain pattern: one DNS label holding {n}.
func ValidPattern(p string) error {
	p = strings.ToLower(strings.TrimSpace(p))
	if strings.Count(p, "{n}") != 1 {
		return errors.New("الگو باید دقیقاً یک {n} داشته باشد، مثل fr{n}")
	}
	label := strings.ReplaceAll(p, "{n}", "1")
	if len(label) > 60 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
		return errors.New("الگوی ساب‌دامین معتبر نیست")
	}
	for _, r := range label {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return errors.New("الگو فقط حروف انگلیسی، عدد، خط تیره و {n} می‌پذیرد (بدون نقطه)")
		}
	}
	return nil
}

// hostnameMu keeps two install jobs from picking the same free number.
var hostnameMu sync.Mutex

// assignHostname gives a server built in a DNS location its subdomain. A
// replacement in the same zone and pattern takes over the replaced server's
// name, so the record simply moves to the new IP; anything else gets the
// lowest number no live server of the zone holds.
func (o *Orchestrator) assignHostname(r *jobs.Run, s *models.Server, replaceID uint) {
	if !s.DNS.On() || s.Hostname != "" {
		return
	}
	hostnameMu.Lock()
	defer hostnameMu.Unlock()
	if replaceID != 0 {
		var old models.Server
		if o.DB.First(&old, replaceID).Error == nil && old.Hostname != "" &&
			old.CFZoneID == s.CFZoneID && old.DNSPattern == s.DNSPattern {
			s.Hostname = old.Hostname
			o.save(r, s, "hostname")
			r.Info("takes over %s from %s", s.Hostname, old.Name)
			return
		}
	}
	var taken []string
	o.DB.Model(&models.Server{}).Where("cf_zone_id = ? AND id <> ? AND status <> ? AND hostname <> ''", s.CFZoneID, s.ID, models.ServerDeleted).
		Pluck("hostname", &taken)
	used := map[string]bool{}
	for _, h := range taken {
		used[h] = true
	}
	for n := 1; ; n++ {
		if h := HostnameFor(s.DNSPattern, n, s.CFZoneName); !used[h] {
			s.Hostname = h
			break
		}
	}
	o.save(r, s, "hostname")
	r.Info("hostname %s", s.Hostname)
}

func (o *Orchestrator) dnsAPI(accountID uint) (DNSAPI, error) {
	var acc models.CloudflareAccount
	if err := o.DB.First(&acc, accountID).Error; err != nil {
		return nil, fmt.Errorf("cloudflare account %d: %w", accountID, err)
	}
	if !acc.Enabled {
		return nil, fmt.Errorf("cloudflare account %s is disabled", acc.Name)
	}
	return o.CF(&acc), nil
}

// syncDNS points the server's hostname at its current IP. It is safe to call
// any number of times and does nothing for a server without a hostname.
func (o *Orchestrator) syncDNS(ctx context.Context, r *jobs.Run, s *models.Server) error {
	if s.Hostname == "" || s.IPv4 == "" {
		return nil
	}
	err := func() error {
		api, err := o.dnsAPI(s.CFAccountID)
		if err != nil {
			return err
		}
		id, err := api.UpsertA(ctx, s.CFZoneID, s.Hostname, s.IPv4, s.DNSProxied)
		if id != "" {
			s.DNSRecordID = id
		}
		return err
	}()
	if err != nil {
		s.DNSError = truncate(err.Error(), 255)
		r.Logf("warn", "DNS %s → %s: %v", s.Hostname, s.IPv4, err)
	} else {
		s.DNSError = ""
		r.Info("DNS: %s → %s", s.Hostname, s.IPv4)
	}
	o.save(r, s, "dns_record_id", "dns_error")
	return err
}

// removeDNS deletes a deleted server's record, unless another live server
// has taken the name over or the record already points elsewhere.
func (o *Orchestrator) removeDNS(ctx context.Context, r *jobs.Run, s *models.Server) {
	if s.Hostname == "" || s.CFZoneID == "" {
		return
	}
	var n int64
	o.DB.Model(&models.Server{}).Where("hostname = ? AND id <> ? AND status <> ?", s.Hostname, s.ID, models.ServerDeleted).Count(&n)
	if n > 0 {
		r.Info("DNS: %s now belongs to another server; kept", s.Hostname)
		return
	}
	api, err := o.dnsAPI(s.CFAccountID)
	if err != nil {
		r.Logf("warn", "DNS: %v", err)
		return
	}
	recs, err := api.Records(ctx, s.CFZoneID, "A", s.Hostname)
	if err != nil {
		r.Logf("warn", "DNS %s: %v", s.Hostname, err)
		return
	}
	for _, rec := range recs {
		if rec.Content != s.IPv4 {
			continue
		}
		if err := api.DeleteRecord(ctx, s.CFZoneID, rec.ID); err != nil {
			r.Logf("warn", "DNS: delete %s: %v", s.Hostname, err)
		} else {
			r.Info("DNS: %s removed", s.Hostname)
		}
	}
}

// pushCert copies the zone's wildcard certificate to the server and serves
// the 3x-ui panel with it. Without SSL on the server it does nothing.
func (o *Orchestrator) pushCert(ctx context.Context, r *jobs.Run, s *models.Server, remote Remote) error {
	if !s.SSL || s.CFZoneName == "" || o.Certs == nil {
		return nil
	}
	r.Step("SSL certificate")
	cert, _, err := o.Certs.Ensure(ctx, s.CFAccountID, s.CFZoneID, s.CFZoneName, r.Info)
	if err != nil {
		return fmt.Errorf("certificate for %s: %w", s.CFZoneName, err)
	}
	b64 := base64.StdEncoding.EncodeToString
	script := strings.Join([]string{
		"set -e", "umask 077", "mkdir -p /root/cert",
		"echo " + b64([]byte(cert.CertPEM)) + " | base64 -d > " + CertFile + ".new",
		"echo " + b64([]byte(cert.KeyPEM)) + " | base64 -d > " + KeyFile + ".new",
		"mv -f " + CertFile + ".new " + CertFile, "mv -f " + KeyFile + ".new " + KeyFile,
	}, "\n")
	if _, err := remote.Run(ctx, "bash -c "+sshx.ShellQuote(script), nil); err != nil {
		return fmt.Errorf("copy certificate: %w", err)
	}
	s.CertNotAfter = cert.NotAfter
	o.save(r, s, "cert_not_after")
	r.Info("certificate copied to %s (valid until %s)", CertFile, cert.NotAfter.Format("2006-01-02"))
	if s.XUIPort == 0 {
		return nil
	}

	// The panel reads its certificate at start, so it is restarted either way.
	panel := o.PanelFor(s, remote)
	cmd := fmt.Sprintf("/usr/local/x-ui/x-ui cert -webCert %s -webCertKey %s >/dev/null 2>&1 && (x-ui restart >/dev/null 2>&1 || systemctl restart x-ui)", CertFile, KeyFile)
	if _, err := remote.Run(ctx, cmd, nil); err != nil {
		r.Logf("warn", "3x-ui panel certificate: %v", err)
		return nil
	}
	prev := s.XUIScheme
	s.XUIScheme = "https"
	_ = sleep(ctx, o.RetryDelay/6)
	if err := o.waitPanel(ctx, r, s, o.PanelFor(s, remote)); err != nil {
		// Never leave the panel unreachable over a certificate.
		r.Logf("warn", "3x-ui did not come up over https (%v); going back to http", err)
		_, _ = remote.Run(ctx, "/usr/local/x-ui/x-ui cert -reset >/dev/null 2>&1; x-ui restart >/dev/null 2>&1 || systemctl restart x-ui", nil)
		s.XUIScheme = firstNonEmpty(prev, "http")
		o.save(r, s, "xui_scheme")
		return o.waitPanel(ctx, r, s, panel)
	}
	o.save(r, s, "xui_scheme")
	r.Info("3x-ui panel serves https at %s", PanelURL(s))
	return nil
}

// certPush re-copies the certificate to one server (after a renewal, or by hand).
func (o *Orchestrator) certPush(ctx context.Context, r *jobs.Run) error {
	s, err := o.loadServer(r)
	if err != nil {
		return err
	}
	if !s.SSL {
		return errors.New("SSL is off for this server")
	}
	remote, _, err := o.Connect(ctx, s)
	if err != nil {
		return fmt.Errorf("ssh: %w", err)
	}
	defer remote.Close()
	if err := o.pushCert(ctx, r, s, remote); err != nil {
		return err
	}
	if s.XUIPort != 0 {
		// Inbounds read the files when xray starts.
		if err := o.PanelFor(s, remote).RestartXray(ctx); err != nil {
			r.Logf("warn", "restart xray: %v", err)
		}
	}
	return nil
}

// dnsSync re-points one server's record at its IP (the "fix DNS" button).
func (o *Orchestrator) dnsSync(ctx context.Context, r *jobs.Run) error {
	s, err := o.loadServer(r)
	if err != nil {
		return err
	}
	if s.Hostname == "" {
		return errors.New("this server has no hostname")
	}
	r.Step("update DNS")
	return o.syncDNS(ctx, r, s)
}

// RenewCerts renews every zone certificate close to expiry and queues a copy
// to each live SSL server holding an older one.
func (o *Orchestrator) RenewCerts(ctx context.Context) error {
	if o.Certs == nil {
		return nil
	}
	type zone struct {
		CFAccountID uint   `gorm:"column:cf_account_id"`
		CFZoneID    string `gorm:"column:cf_zone_id"`
		CFZoneName  string `gorm:"column:cf_zone_name"`
	}
	var zones []zone
	o.DB.Model(&models.Server{}).Distinct("cf_account_id", "cf_zone_id", "cf_zone_name").
		Where("ssl = ? AND status IN ? AND cf_zone_name <> ''", true, []string{models.ServerActive, models.ServerFiltered}).
		Scan(&zones)
	var errs []error
	for _, z := range zones {
		cert, _, err := o.Certs.Ensure(ctx, z.CFAccountID, z.CFZoneID, z.CFZoneName, nil)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", z.CFZoneName, err))
			continue
		}
		var stale []models.Server
		o.DB.Where("ssl = ? AND cf_zone_name = ? AND status IN ? AND (cert_not_after IS NULL OR cert_not_after < ?)",
			true, z.CFZoneName, []string{models.ServerActive, models.ServerFiltered}, cert.NotAfter).Find(&stale)
		for i := range stale {
			id := stale[i].ID
			if _, err := o.Engine.Enqueue(KindCertPush, &id, struct{}{}, "schedule"); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}
