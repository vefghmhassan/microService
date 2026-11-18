package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"microserver/internal/jobs"
	"microserver/internal/models"
	"microserver/internal/services/settings"
	"microserver/internal/services/sshx"
)

// A server without a domain serves its 3x-ui panel with a Let's Encrypt
// certificate for its bare IP: the "shortlived" profile, valid about six
// days. acme.sh issues it on the server itself over HTTP-01 (the way 3x-ui's
// own installer does) and renews it from its cron; the panel only watches the
// expiry and issues again when a renewal did not happen.

// KindIPCert issues (or renews) one server's IP certificate.
const KindIPCert = "ip_cert"

// Where a server keeps its IP certificate (3x-ui's own paths for it).
const (
	IPCertFile = "/root/cert/ip/fullchain.pem"
	IPKeyFile  = "/root/cert/ip/privkey.pem"
)

// IPCertLife is how long a short-lived certificate is valid; IPCertRenew is
// how close to its expiry the panel issues a new one itself.
const (
	IPCertLife  = 6 * 24 * time.Hour
	IPCertRenew = 48 * time.Hour
)

// AcmePort is the port Let's Encrypt validates an IP on. No inbound may
// hold it on a server that uses an IP certificate (see wantsIPCert).
const AcmePort = 80

// wantsIPCert reports whether s serves its panel with an IP certificate:
// it has one already, or it is a node (the master reaches it over verified
// https) without a zone certificate of its own.
func wantsIPCert(s *models.Server) bool {
	if s.IPCert {
		return true
	}
	return s.Role == models.RoleNode && !(s.SSL && s.CFZoneName != "")
}

// IPCertScript is the shell that gets ip a short-lived certificate with
// acme.sh in standalone mode and installs it to IPCertFile/IPKeyFile. Its
// last line is "NOTAFTER=<date>" for the caller.
func IPCertScript(ip, email string, staging bool) string {
	q := sshx.ShellQuote
	server := "letsencrypt"
	if staging {
		server = "letsencrypt_test"
	}
	acme := "~/.acme.sh/acme.sh"
	// Without a contact the argument is left out altogether: "email=" with
	// nothing after it reaches acme.sh as a bare --email, whose missing
	// value ends the installer (dash refuses the extra shift).
	installArgs := ""
	if email != "" {
		installArgs = " -s email=" + q(email)
	}
	lines := []string{
		"set -e",
		"export DEBIAN_FRONTEND=noninteractive",
		// The standalone listener of acme.sh is socat.
		"command -v socat >/dev/null 2>&1 || { apt-get update -qq >/dev/null 2>&1 || true; apt-get install -y -qq socat >/dev/null 2>&1 || yum install -y socat >/dev/null 2>&1 || true; }",
		"command -v socat >/dev/null 2>&1 || { echo 'socat is missing and could not be installed'; exit 3; }",
		// The installer's output is kept: it is the only clue when it fails.
		"[ -x " + acme + " ] || { cd ~ && curl -fsSL https://get.acme.sh | sh" + installArgs + " >/tmp/acme-install.log 2>&1 || true; }",
		"[ -x " + acme + " ] || { echo 'acme.sh could not be installed:'; tail -n 6 /tmp/acme-install.log 2>/dev/null; exit 3; }",
		fmt.Sprintf("if command -v ufw >/dev/null 2>&1 && ufw status | grep -q 'Status: active'; then ufw allow %d/tcp >/dev/null; fi", AcmePort),
		fmt.Sprintf("if ss -ltnH 2>/dev/null | awk '{print $4}' | grep -qE '[:.]%d$'; then echo 'port %d is in use on this server and the certificate validation needs it'; exit 4; fi", AcmePort, AcmePort),
		acme + " --set-default-ca --server " + server + " >/dev/null 2>&1 || true",
	}
	if email != "" {
		lines = append(lines, acme+" --register-account -m "+q(email)+" --server "+server+" >/dev/null 2>&1 || true")
	}
	lines = append(lines,
		fmt.Sprintf("%s --issue -d %s --standalone --server %s --certificate-profile shortlived --days 6 --httpport %d --force", acme, q(ip), server, AcmePort),
		"mkdir -p /root/cert/ip",
		// A failing reload command makes acme.sh exit non-zero although the
		// files are in place; the files are what is checked.
		fmt.Sprintf("%s --installcert --force -d %s --key-file %s --fullchain-file %s --reloadcmd %s >/dev/null 2>&1 || true",
			acme, q(ip), IPKeyFile, IPCertFile, q("systemctl restart x-ui 2>/dev/null || rc-service x-ui restart 2>/dev/null || true")),
		"[ -s "+IPCertFile+" ] && [ -s "+IPKeyFile+" ] || { echo 'certificate files are missing after the install'; exit 5; }",
		"chmod 600 "+IPKeyFile+"; chmod 644 "+IPCertFile,
		// Keeps acme.sh's cron (the renewal) in place.
		acme+" --upgrade --auto-upgrade >/dev/null 2>&1 || true",
		"echo NOTAFTER=$(openssl x509 -enddate -noout -in "+IPCertFile+" | cut -d= -f2)",
	)
	return strings.Join(lines, "\n")
}

// parseNotAfter reads the script's NOTAFTER line (openssl's date format).
func parseNotAfter(out string) (time.Time, bool) {
	for _, line := range strings.Split(out, "\n") {
		v, ok := strings.CutPrefix(strings.TrimSpace(line), "NOTAFTER=")
		if !ok {
			continue
		}
		for _, layout := range []string{"Jan _2 15:04:05 2006 MST", "Jan 2 15:04:05 2006 MST"} {
			if t, err := time.Parse(layout, strings.Join(strings.Fields(v), " ")); err == nil {
				return t, true
			}
		}
	}
	return time.Time{}, false
}

// ensureIPCert gives s its IP certificate and serves the panel with it. A
// server with a zone certificate keeps that one. When the panel does not
// come up over https it goes back to http (setPanelCertFiles) and the error
// says so.
func (o *Orchestrator) ensureIPCert(ctx context.Context, r *jobs.Run, s *models.Server, remote Remote) error {
	if s.SSL && s.CFZoneName != "" {
		return nil
	}
	ip := s.IPv4
	if net.ParseIP(ip) == nil {
		return errors.New("the server has no IPv4 address for a certificate")
	}
	r.Step("SSL certificate for " + ip)
	staging := o.Settings.Bool(settings.AcmeStaging)
	script := IPCertScript(ip, strings.TrimSpace(o.Settings.Get(settings.AcmeIPEmail)), staging)
	issueCtx, cancel := context.WithTimeout(ctx, 6*time.Minute)
	defer cancel()
	var tail []string
	out, err := remote.Run(issueCtx, "bash -c "+sshx.ShellQuote(script), func(line string) {
		if line = strings.TrimSpace(line); line != "" {
			r.Logf("out", "%s", line)
			if tail = append(tail, line); len(tail) > 3 {
				tail = tail[1:]
			}
		}
	})
	if err != nil {
		return fmt.Errorf("IP certificate for %s: %w (%s)", ip, err, truncate(strings.Join(tail, " | "), 200))
	}
	notAfter, ok := parseNotAfter(out)
	if !ok {
		notAfter = time.Now().Add(IPCertLife)
	}
	s.IPCert, s.CertNotAfter, s.CertIP = true, &notAfter, ip
	o.save(r, s, "ip_cert", "cert_not_after", "cert_ip")
	r.Info("IP certificate for %s is valid until %s", ip, notAfter.Format("2006-01-02 15:04"))
	if s.XUIPort == 0 {
		return nil
	}
	if err := o.setPanelCertFiles(ctx, r, s, remote, IPCertFile, IPKeyFile, false); err != nil {
		return err
	}
	if s.XUIScheme != "https" {
		return errors.New("the 3x-ui panel did not come up over https with the IP certificate")
	}
	return nil
}

// ipCert is the ip_cert job: issue the certificate again for one server.
func (o *Orchestrator) ipCert(ctx context.Context, r *jobs.Run) error {
	s, err := o.loadServer(r)
	if err != nil {
		return err
	}
	if s.SSL && s.CFZoneName != "" {
		return errors.New("this server uses its zone certificate; an IP certificate is not needed")
	}
	remote, _, err := o.Connect(ctx, s)
	if err != nil {
		return fmt.Errorf("ssh: %w", err)
	}
	defer remote.Close()
	// An inbound on the validation port has to go first.
	if s.XUIPort != 0 && !s.IPCert && s.Role != models.RoleMaster && o.holdsAcmePort(s) {
		s.IPCert = true
		o.save(r, s, "ip_cert")
		r.Step("free port 80")
		if err := o.applyWith(ctx, r, s, remote); err != nil {
			return err
		}
	}
	return o.ensureIPCert(ctx, r, s, remote)
}

// holdsAcmePort reports whether the template puts an inbound on the
// validation port of s.
func (o *Orchestrator) holdsAcmePort(s *models.Server) bool {
	snap, err := LoadSnapshot(o.DB)
	if err != nil {
		return false
	}
	plain := *s
	plain.IPCert, plain.Role = false, ""
	for _, t := range snap.For(&plain).Inbounds {
		if t.Enabled && t.Port == AcmePort {
			return true
		}
	}
	return false
}

// RenewIPCerts queues a new IP certificate for every live server whose
// certificate is about to run out (acme.sh's own renewal did not happen).
func (o *Orchestrator) RenewIPCerts(ctx context.Context) error {
	var servers []models.Server
	if err := o.DB.Where("ip_cert = ? AND status IN ? AND (cert_not_after IS NULL OR cert_not_after < ?)",
		true, []string{models.ServerActive, models.ServerFiltered}, time.Now().Add(IPCertRenew)).Find(&servers).Error; err != nil {
		return err
	}
	var errs []error
	for i := range servers {
		id := servers[i].ID
		var queued int64
		o.DB.Model(&models.Job{}).Where("kind = ? AND server_id = ? AND status IN ?", KindIPCert, id,
			[]string{models.JobQueued, models.JobRunning}).Count(&queued)
		if queued > 0 {
			continue
		}
		if _, err := o.Engine.Enqueue(KindIPCert, &id, struct{}{}, "schedule"); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
