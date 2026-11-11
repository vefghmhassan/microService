package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"microserver/internal/jobs"
	"microserver/internal/models"
	"microserver/internal/services/checker"
	"microserver/internal/services/cleanip"
	"microserver/internal/services/destination"
	"microserver/internal/services/providers"
	"microserver/internal/services/settings"
	"microserver/internal/services/sshx"
)

// errStillFiltered means every IP tried in this location was filtered.
var errStillFiltered = errors.New("still filtered")

type ProvisionParams struct {
	AccountID uint   `json:"account_id"`
	Name      string `json:"name"`
	Region    string `json:"region"`
	Plan      string `json:"plan"`
	Image     string `json:"image"`
	Country   string `json:"country"`
	Upload    *bool  `json:"upload"`
	BaseName  string `json:"base_name"`
	// IPFamily is "v4" (default) or "v6": what the server's configs use.
	IPFamily string `json:"ip_family"`
	// Attempt counts the servers already built and deleted for this request
	// because none of their configs worked.
	Attempt int `json:"attempt,omitempty"`
}

type RotateParams struct {
	KeepOld   *bool  `json:"keep_old"`
	ReuseIPID uint   `json:"reuse_ip_id"`
	Reason    string `json:"reason"`
}

type VersionParams struct {
	Version string `json:"version"`
}

// ---- provision ----

func (o *Orchestrator) provision(ctx context.Context, r *jobs.Run) error {
	var p ProvisionParams
	if err := r.Params(&p); err != nil {
		return err
	}
	var acc models.ProviderAccount
	if err := o.DB.First(&acc, p.AccountID).Error; err != nil {
		return fmt.Errorf("provider account %d: %w", p.AccountID, err)
	}
	prov, err := o.Provider(&acc)
	if err != nil {
		return err
	}

	// The server row is created once; a resumed job finds it via its id.
	var s models.Server
	if r.Job.ServerID != nil {
		if err := o.DB.Preload("ProviderAccount").First(&s, *r.Job.ServerID).Error; err != nil {
			return err
		}
	} else {
		region := firstNonEmpty(p.Region, acc.DefaultRegion)
		name := sanitizeName(p.Name)
		if name == "" {
			name = sanitizeName(fmt.Sprintf("ms-%s-%s", region, randomSuffix(2)))
		}
		s = models.Server{
			Name: name, ProviderAccountID: acc.ID, Region: region,
			Plan:    firstNonEmpty(p.Plan, acc.DefaultPlan),
			Image:   firstNonEmpty(p.Image, acc.DefaultImage, prov.Capabilities().DefaultImage),
			Country: strings.ToUpper(firstNonEmpty(p.Country, acc.Country)),
			Status:  models.ServerCreating, SyncStatus: models.SyncPending, XUIScheme: "http",
			IPFamily: models.NormFamily(p.IPFamily),
		}
		if s.Region == "" || s.Plan == "" {
			return errors.New("region and plan are required (set them on the request or as account defaults)")
		}
		if err := o.DB.Create(&s).Error; err != nil {
			return err
		}
		r.SetServer(s.ID)
		s.ProviderAccount = &acc
	}
	r.Info("server %s (#%d) at %s %s/%s, IP%s", s.Name, s.ID, acc.Type, s.Region, s.Plan, s.Family())

	if !r.Done("create") {
		r.Step("create server")
		req := o.createRequest(&s)
		if ip := o.pickReusable(acc.ID, s.Region); ip != nil && prov.Capabilities().CanKeepOldIP && s.Family() == models.FamilyV4 {
			req.ReuseIPRef = ip.Ref
			r.Info("retrying kept IP %s (filtered since %v)", ip.Address, ip.FilteredAt)
		}
		inst, err := prov.Create(ctx, req)
		if err != nil {
			return o.fail(r, &s, fmt.Errorf("create: %w", err))
		}
		s.ProviderRef = inst.Ref
		o.save(r, &s, "provider_ref")
		r.Mark("create")
	}

	if !r.Done("running") {
		r.Step("wait for boot")
		inst, err := providers.WaitRunning(ctx, prov, s.ProviderRef, 15*time.Minute)
		if err != nil {
			return o.fail(r, &s, fmt.Errorf("wait running: %w", err))
		}
		s.IPv4, s.IPRef, s.IPv6 = inst.IPv4, inst.IPRef, inst.IPv6
		if inst.Region != "" {
			s.Region = inst.Region
		}
		s.Status = models.ServerChecking
		o.save(r, &s, "ipv4", "ip_ref", "ipv6", "region", "status")
		o.poolUse(&s)
		if s.PublicIP() == "" {
			return o.fail(r, &s, fmt.Errorf("the provider gave the server no public IP%s", s.Family()))
		}
		r.Info("running at %s", ipLabel(&s))
		r.Mark("running")
	}

	if !r.Done("ssh") {
		r.Step("wait for SSH and cloud-init")
		if err := o.waitBooted(ctx, r, &s); err != nil {
			return o.fail(r, &s, err)
		}
		r.Mark("ssh")
	}

	if !r.Done("ip") {
		r.Step("check reachability from Iran")
		if err := o.ensureReachable(ctx, r, prov, &s); err != nil {
			return o.fail(r, &s, err)
		}
		r.Mark("ip")
	}

	if !r.Done("install") {
		if err := o.installAndApply(ctx, r, &s, ""); err != nil {
			return o.fail(r, &s, err)
		}
		r.Mark("install")
	}

	// Only configs that really work go to vpn-pannel; a server on which none
	// works is deleted and built again in the same place.
	g, err := o.gate(ctx, r, &s)
	if err != nil {
		return err
	}
	if g.none() && !r.Done("upload") {
		return o.rejectNewServer(r, &s, p.Attempt, 0, func(next int) error {
			np := p
			np.Attempt, np.Name = next, ""
			_, err := o.Engine.Enqueue(KindProvision, nil, np, r.Job.Source)
			return err
		})
	}

	upload := o.Settings.Bool(settings.VPNPanelAuto)
	if p.Upload != nil {
		upload = *p.Upload
	}
	if upload && !r.Done("upload") {
		if err := o.uploadLinks(ctx, r, &s, p.BaseName, g.only()); err != nil {
			// The server works; only the hand-off failed and can be retried.
			r.Logf("warn", "upload to vpn-pannel failed: %v", err)
		} else {
			r.Mark("upload")
		}
	}

	s.Status = models.ServerActive
	s.LastError = ""
	o.save(r, &s, "status", "last_error")
	o.startWatch(&s)
	o.notify("✅ سرور %s آماده شد\nIP: %s\n%s", s.Name, s.PublicIP(), s.LastCheckNote)
	return nil
}

// waitBooted blocks until sshd accepts our key and cloud-init has finished.
// Nothing may check or power-cycle a server before this: a probe against a
// half-booted machine reads as "filtered", and a power-off during cloud-init
// can leave sshd without host keys.
func (o *Orchestrator) waitBooted(ctx context.Context, r *jobs.Run, s *models.Server) error {
	remote, hostKey, err := o.Connect(ctx, s)
	if err != nil {
		return fmt.Errorf("server never accepted SSH: %w", err)
	}
	defer remote.Close()
	if s.SSHHostKey == "" && hostKey != "" {
		s.SSHHostKey = hostKey
		o.save(r, s, "ssh_host_key")
	}
	waitCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	if _, err := remote.Run(waitCtx, "cloud-init status --wait >/dev/null 2>&1 || true", nil); err != nil {
		return fmt.Errorf("cloud-init: %w", err)
	}
	r.Info("SSH is up and cloud-init has finished")
	return nil
}

// waitServing waits until the server is booted and, once 3x-ui is configured,
// until xray is listening on the port being checked. Checking a machine that
// is still coming back from a power cycle reads as "filtered" and wastes IPs.
func (o *Orchestrator) waitServing(ctx context.Context, r *jobs.Run, s *models.Server) error {
	if err := o.waitBooted(ctx, r, s); err != nil {
		return err
	}
	if s.AppliedHash == "" {
		return nil
	}
	port := o.checkPort(s)
	remote, _, err := o.Connect(ctx, s)
	if err != nil {
		return fmt.Errorf("ssh: %w", err)
	}
	defer remote.Close()
	deadline := time.Now().Add(3 * time.Minute)
	for {
		conn, err := remote.DialContext(ctx, "tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			conn.Close()
			r.Info("xray is listening on %d", port)
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("nothing listens on port %d after boot: %w", port, err)
		}
		if err := sleep(ctx, 3*time.Second); err != nil {
			return err
		}
	}
}

// ensureReachable checks the server's IP from Iran and rotates until a healthy
// one is found or attempts run out.
func (o *Orchestrator) ensureReachable(ctx context.Context, r *jobs.Run, prov providers.Provider, s *models.Server) error {
	maxAttempts := o.Settings.Int(settings.RotateMaxAttempts)
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	// Filtered IPs are held for the rest of the loop: Hetzner, for one, hands a
	// just-released IP straight back, and the loop would bounce between the
	// same two blocked addresses. They are released (or kept, per settings)
	// once the loop ends either way.
	var held []heldIP
	defer func() { o.releaseHeld(r, prov, s, held) }()
	canHold := prov.Capabilities().CanKeepOldIP
	downRetries, unknownRetries := 0, 0
	for attempt := 1; ; {
		res := o.Check(ctx, s.PublicIP(), o.checkPort(s))
		o.recordCheck(r, s, res)
		switch res.Verdict {
		case checker.Healthy:
			return nil
		case checker.Down:
			// Usually sshd is still starting; nothing to do with filtering.
			if downRetries++; downRetries > 6 {
				return fmt.Errorf("%s does not answer from outside Iran either; giving up", s.PublicIP())
			}
			if err := sleep(ctx, o.RetryDelay); err != nil {
				return err
			}
			continue
		case checker.Unknown:
			if unknownRetries++; unknownRetries > 2 {
				r.Logf("warn", "not enough Iranian probes answered; continuing without a verdict")
				return nil
			}
			if err := sleep(ctx, o.RetryDelay); err != nil {
				return err
			}
			continue
		}
		// Filtered.
		if attempt >= maxAttempts {
			return fmt.Errorf("%w after %d IPs", errStillFiltered, attempt)
		}
		attempt++
		r.Info("%s is filtered; getting a new IP (attempt %d/%d)", s.PublicIP(), attempt, maxAttempts)
		keep := canHold
		rot, err := o.rotateOnce(ctx, r, prov, s, &keep, 0, true)
		if err != nil {
			return err
		}
		if rot.OldKept && rot.OldIPRef != "" {
			held = append(held, heldIP{addr: rot.OldIP, ref: rot.OldIPRef})
		}
		if err := o.waitServing(ctx, r, s); err != nil {
			return err
		}
	}
}

// rotateOnce gives the server one new IP and records both addresses.
type heldIP struct{ addr, ref string }

// releaseHeld lets go of IPs held during a reachability loop, unless the
// settings say filtered IPs are kept for a later retry.
func (o *Orchestrator) releaseHeld(r *jobs.Run, prov providers.Provider, s *models.Server, held []heldIP) {
	if len(held) == 0 || o.Settings.Bool(settings.RotateKeepOld) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	for _, h := range held {
		if err := prov.ReleaseIP(ctx, h.ref); err != nil && !errors.Is(err, providers.ErrNotFound) {
			r.Logf("warn", "release filtered IP %s: %v", h.addr, err)
			continue
		}
		o.poolRetire(s, h.addr, h.ref, false, true, false, "filtered; released after the job")
		r.Info("released filtered IP %s", h.addr)
	}
}

// rotateOnce moves the server to a new IP. oldFiltered records whether the IP
// being left was filtered, as opposed to a manual or test rotation.
func (o *Orchestrator) rotateOnce(ctx context.Context, r *jobs.Run, prov providers.Provider, s *models.Server, keepOld *bool, reuseIPID uint, oldFiltered bool) (*providers.RotateResult, error) {
	keep := o.Settings.Bool(settings.RotateKeepOld)
	if keepOld != nil {
		keep = *keepOld
	}
	caps := prov.Capabilities()
	if o.prefixRotation(r, prov, s) {
		// Another address of the server's own /64: no rebuild, no new server.
		res, err := o.rotateInPrefix(ctx, r, s)
		if err == nil {
			return res, nil
		}
		r.Logf("warn", "rotating inside the /64 failed (%v); rotating through the provider", err)
	}
	opts := providers.RotateOptions{KeepOld: keep && caps.CanKeepOldIP, Create: o.createRequest(s), IPv6: s.Family() == models.FamilyV6}
	if opts.IPv6 {
		// A kept IPv4 would not change the address clients use.
		reuseIPID = 0
	}
	if reuseIPID != 0 {
		var ip models.IPAddress
		if err := o.DB.First(&ip, reuseIPID).Error; err != nil {
			return nil, fmt.Errorf("ip %d: %w", reuseIPID, err)
		}
		opts.ReuseIPRef = ip.Ref
	} else if caps.CanKeepOldIP && !opts.IPv6 {
		if ip := o.pickReusable(s.ProviderAccountID, s.Region); ip != nil {
			opts.ReuseIPRef = ip.Ref
			r.Info("retrying kept IP %s", ip.Address)
		}
	}

	prevStatus := s.Status
	o.setStatus(r, s, models.ServerRotating)
	r.Info("rotating via %s (keep old: %t)", caps.RotateMethod, opts.KeepOld)
	res, err := prov.RotateIP(ctx, instanceOf(s), opts)
	if err != nil {
		s.Status = prevStatus
		o.save(r, s, "status")
		return nil, fmt.Errorf("rotate: %w", err)
	}
	o.poolRetire(s, res.OldIP, res.OldIPRef, res.OldKept, oldFiltered, caps.CanKeepOldIP, "rotated away")

	oldPublic := s.PublicIP()
	s.IPv4, s.IPRef, s.IPv6 = res.Instance.IPv4, res.Instance.IPRef, res.Instance.IPv6
	if res.Recreated {
		s.ProviderRef = res.Instance.Ref
		s.SSHHostKey = ""
		s.AppliedHash = ""
		s.SyncStatus = models.SyncPending
		s.XUIPort = 0
		s.XUIToken = ""
	}
	s.RotateCount++
	s.Status = prevStatus
	o.save(r, s)
	o.poolUse(s)
	if s.Family() == models.FamilyV6 {
		r.Info("new IPv6 %s (old %s)", s.IPv6, oldPublic)
	} else {
		r.Info("new IP %s (old %s %s)", s.IPv4, res.OldIP, map[bool]string{true: "kept", false: "released"}[res.OldKept])
	}
	return res, nil
}

// ---- rotate ----

func (o *Orchestrator) rotate(ctx context.Context, r *jobs.Run) error {
	var p RotateParams
	if err := r.Params(&p); err != nil {
		return err
	}
	s, err := o.loadServer(r)
	if err != nil {
		return err
	}
	prov, err := o.Provider(s.ProviderAccount)
	if err != nil {
		return err
	}
	oldUploaded := firstNonEmpty(s.UploadedIP, s.PublicIP())
	if p.Reason != "" {
		r.Info("reason: %s", p.Reason)
	}

	if !r.Done("rotate") {
		r.Step("rotate IP")
		// The old IP counts as filtered only if the last check said so.
		oldFiltered := s.Status == models.ServerFiltered || (s.LastCheckOK != nil && !*s.LastCheckOK)
		res, err := o.rotateOnce(ctx, r, prov, s, p.KeepOld, p.ReuseIPID, oldFiltered)
		if err != nil {
			return o.fail(r, s, err)
		}
		if res.Recreated {
			r.Set("recreated", "true")
		}
		r.Mark("rotate")
	}

	if !r.Done("ssh") {
		r.Step("wait for the server to come back")
		// A recreated server has no 3x-ui yet, so only boot can be awaited.
		wait := o.waitServing
		if r.GetString("recreated") == "true" {
			wait = o.waitBooted
		}
		if err := wait(ctx, r, s); err != nil {
			return o.fail(r, s, err)
		}
		r.Mark("ssh")
	}

	if !r.Done("ip") {
		r.Step("check new IP from Iran")
		if r.GetString("recreated") == "true" {
			// Nothing but sshd listens yet; check there.
			s.AppliedHash = ""
		}
		if err := o.ensureReachable(ctx, r, prov, s); err != nil {
			if errors.Is(err, errStillFiltered) && o.fallbackToNextLocation(r, s) {
				return nil
			}
			return o.fail(r, s, err)
		}
		r.Mark("ip")
	}

	if r.GetString("recreated") == "true" && !r.Done("install") {
		if err := o.installAndApply(ctx, r, s, ""); err != nil {
			return o.fail(r, s, err)
		}
		r.Mark("install")
	}

	if !r.Done("dns") {
		_ = o.syncDNS(ctx, r, s)
		r.Mark("dns")
	}

	if !r.Done("dest") {
		r.Step("update vpn-pannel")
		if err := o.moveDestination(ctx, r, s, oldUploaded); err != nil {
			r.Logf("warn", "vpn-pannel: %v", err)
		}
		r.Mark("dest")
	}

	s.Status = models.ServerActive
	s.LastError = ""
	o.save(r, s, "status", "last_error")
	o.startWatch(s)
	o.notify("🔄 IP سرور %s عوض شد\n%s → %s", s.Name, oldUploaded, s.PublicIP())
	return nil
}

// fallbackToNextLocation hands a server whose location ran out of clean IPs
// to automatic placement, which builds its replacement elsewhere and moves
// the configs over. It reports whether it took over.
func (o *Orchestrator) fallbackToNextLocation(r *jobs.Run, s *models.Server) bool {
	if !o.Settings.Bool(settings.RotateFallback) {
		return false
	}
	var n int64
	o.DB.Model(&models.Location{}).Where("enabled = ? AND NOT (provider_account_id = ? AND region = ?)", true, s.ProviderAccountID, s.Region).Count(&n)
	if n == 0 {
		r.Logf("warn", "no other location in the priority list to fall back to")
		return false
	}
	up := o.Settings.Bool(settings.VPNPanelAuto)
	j, err := o.Engine.Enqueue(KindProvisionAuto, nil, AutoParams{
		Count: 1, Upload: &up, ReplaceServerID: s.ID, IPFamily: s.Family(),
		ExcludeAccountID: s.ProviderAccountID, ExcludeRegion: s.Region,
	}, r.Job.Source)
	if err != nil {
		r.Logf("warn", "fallback: %v", err)
		return false
	}
	o.setStatus(r, s, models.ServerFiltered)
	r.Info("no clean IP in %s; replacement is being built in the next location (job #%d)", s.Region, j.ID)
	o.notify("🌍 همه IPهای %s برای سرور %s فیلتر بودند؛ ساخت جایگزین در لوکیشن بعدی شروع شد", s.Region, s.Name)
	return true
}

// moveDestination points every vpn-pannel's configs at the server's link
// host. A panel that had none on the old address (never uploaded, or removed
// by hand) gets the links uploaded fresh. It fails only when no panel could
// be updated. A hostname that stays the same needs no move: DNS did it.
func (o *Orchestrator) moveDestination(ctx context.Context, r *jobs.Run, s *models.Server, oldIP string) error {
	host := LinkHost(s)
	if oldIP != "" && oldIP == host {
		if s.UploadedIP != host {
			now := time.Now()
			s.UploadedIP, s.UploadedAt = host, &now
			o.save(r, s, "uploaded_ip", "uploaded_at")
		}
		if s.Hostname != "" {
			r.Info("links point at %s; vpn-pannel needs no change", host)
		}
		return nil
	}
	dests := o.Dests()
	if len(dests) == 0 {
		r.Info("vpn-pannel is not configured; skipped")
		return nil
	}
	if oldIP == "" {
		return nil
	}
	var fresh []NamedDest
	var errs []error
	moved := 0
	for _, d := range dests {
		res, err := d.ReplaceAddress(ctx, oldIP, host, s.Country)
		if errors.Is(err, destination.ErrNoNodes) {
			r.Info("%s: no configs on %s; uploading", d.Name, oldIP)
			fresh = append(fresh, d)
			continue
		}
		if err != nil {
			r.Logf("warn", "%s: %v", d.Name, err)
			errs = append(errs, fmt.Errorf("%s: %w", d.Name, err))
			continue
		}
		moved++
		r.Info("%s: %d configs moved, %d skipped", d.Name, res.Updated, res.Skipped)
		for _, e := range res.Errors {
			r.Logf("warn", "%s: %s", d.Name, e)
		}
	}
	if len(fresh) > 0 {
		if err := o.uploadTo(ctx, r, s, "", fresh, nil); err != nil {
			errs = append(errs, err)
		} else {
			moved += len(fresh)
		}
	}
	if moved == 0 {
		return errors.Join(errs...)
	}
	now := time.Now()
	s.UploadedIP, s.UploadedAt = host, &now
	o.save(r, s, "uploaded_ip", "uploaded_at")
	return nil
}

// ---- install / sync ----

func (o *Orchestrator) install(ctx context.Context, r *jobs.Run) error {
	var p InstallParams
	_ = r.Params(&p)
	s, err := o.loadServer(r)
	if err != nil {
		return err
	}
	o.assignHostname(r, s, p.ReplaceServerID)
	if !r.Done("install") {
		if err := o.installAndApply(ctx, r, s, p.Version); err != nil {
			return o.fail(r, s, err)
		}
		r.Mark("install")
	}
	// Jobs started before the config test moved ahead of the upload have
	// already handed their configs over; they are only tested.
	handed := r.Done("finish")
	if !r.Done("dns") && !handed {
		if s.Hostname != "" {
			r.Step("DNS " + s.Hostname)
			_ = o.syncDNS(ctx, r, s)
		}
		r.Mark("dns")
	}
	// Only configs that really work go to vpn-pannel, and a new server on
	// which none works is not kept.
	g, err := o.gate(ctx, r, s)
	if err != nil {
		return err
	}
	if p.Fresh && g.none() && !handed {
		return o.rejectNewServer(r, s, p.Attempt, p.ReplaceServerID, func(next int) error {
			_, err := o.Engine.Enqueue(KindProvisionAuto, nil, AutoParams{
				Count: 1, Upload: p.Upload, BaseName: p.BaseName, ReplaceServerID: p.ReplaceServerID,
				IPFamily: s.Family(), Reason: "no config worked on " + s.Name, Attempt: next,
			}, r.Job.Source)
			return err
		})
	}
	if !handed {
		o.handOver(ctx, r, s, p, g)
		r.Mark("finish")
	}
	s.Status = models.ServerActive
	s.LastError = ""
	o.save(r, s, "status", "last_error")
	o.startWatch(s)
	if p.ReplaceServerID == 0 && p.Upload != nil {
		o.notify("✅ سرور %s آماده شد\nIP: %s\n%s", s.Name, s.PublicIP(), s.LastCheckNote)
	}
	return nil
}

func (o *Orchestrator) installAndApply(ctx context.Context, r *jobs.Run, s *models.Server, version string) error {
	r.Step("install 3x-ui")
	o.setStatus(r, s, models.ServerInstalling)
	remote, hostKey, err := o.Connect(ctx, s)
	if err != nil {
		return fmt.Errorf("ssh: %w", err)
	}
	defer remote.Close()
	if s.SSHHostKey == "" && hostKey != "" {
		s.SSHHostKey = hostKey
		o.save(r, s, "ssh_host_key")
	}
	if s.CDNAddonID != 0 {
		// CDN add-on: both records and the certificate come first, so the
		// panel is served over SSL on its grey name from the moment it opens.
		if s.Hostname != "" {
			r.Step("DNS " + s.Hostname)
			_ = o.syncDNS(ctx, r, s)
		}
		if err := o.pushCert(ctx, r, s, remote); err != nil {
			r.Logf("warn", "SSL: %v", err)
		}
		fresh := s.XUIPort == 0
		if err := o.installPanelOpts(ctx, r, s, remote, version, fresh); err != nil {
			return err
		}
		if s.CertNotAfter != nil {
			if err := o.setPanelCert(ctx, r, s, remote); err != nil {
				r.Logf("warn", "SSL: %v", err)
			}
		}
		if fresh {
			if err := o.setPanelListen(ctx, r, s, remote, o.Settings.Bool(settings.XUIPublic)); err != nil {
				r.Logf("warn", "%v", err)
			}
		}
		if err := o.ensureAddonXray(ctx, r, s, remote); err != nil {
			r.Logf("warn", "%v", err)
		}
	} else {
		if err := o.installPanel(ctx, r, s, remote, version); err != nil {
			return err
		}
		// Before the template: TLS inbounds need the files when xray starts.
		if err := o.pushCert(ctx, r, s, remote); err != nil {
			r.Logf("warn", "SSL: %v", err)
		}
	}

	r.Step("apply template")
	o.setStatus(r, s, models.ServerConfiguring)
	return o.applyWith(ctx, r, s, remote)
}

func (o *Orchestrator) applyWith(ctx context.Context, r *jobs.Run, s *models.Server, remote Remote) error {
	snap, err := LoadSnapshot(o.DB)
	if err != nil {
		return err
	}
	panel := o.PanelFor(s, remote)
	// What goes on the server depends on its xray (Hysteria2 needs 26+):
	// learn the version first when none is known yet.
	if !knownXray(s.XrayVersion) {
		if st, err := panel.Status(ctx); err == nil && knownXray(st.Xray.Version) {
			s.XrayVersion = st.Xray.Version
		}
	}
	if err := Apply(ctx, panel, snap.For(s), r.Info); err != nil {
		s.SyncStatus = models.SyncFailed
		o.save(r, s, "sync_status")
		return err
	}
	if st, err := panel.Status(ctx); err == nil && knownXray(st.Xray.Version) {
		s.XrayVersion = st.Xray.Version
	}
	if _, err := remote.Run(ctx, "bash -c "+sshx.ShellQuote(hostTuningScript(snap.For(s))), nil); err != nil {
		r.Logf("warn", "firewall/BBR: %v", err)
	}
	ns := o.forUpload(snap, s)
	if len(ns.Clean) == 0 && s.DNSProxied && s.Hostname != "" {
		ns.Clean, _ = cleanip.Pick(o.DB, o.Settings.Int(settings.CleanIPPerLink))
	}
	o.syncPanelPro(ctx, r, s, panel, ns)
	s.AppliedHash = snap.HashFor(s.Family())
	s.AddonHash = snap.For(s).AddonHash
	s.SyncStatus = models.SyncSynced
	o.save(r, s, "applied_hash", "addon_hash", "sync_status", "xray_version")
	return nil
}

func (o *Orchestrator) sync(ctx context.Context, r *jobs.Run) error {
	s, err := o.loadServer(r)
	if err != nil {
		return err
	}
	if s.XUIPort == 0 {
		return errors.New("3x-ui is not installed on this server")
	}
	r.Step("sync template")
	remote, _, err := o.Connect(ctx, s)
	if err != nil {
		return fmt.Errorf("ssh: %w", err)
	}
	defer remote.Close()
	if err := o.ensureAddonXray(ctx, r, s, remote); err != nil {
		r.Logf("warn", "%v", err)
	}
	if err := o.applyWith(ctx, r, s, remote); err != nil {
		return err
	}
	// Configs uploaded while links still pointed at the hostname move to
	// the IP the links use now.
	if s.UploadedIP != "" && s.UploadedIP != LinkHost(s) {
		r.Step("move vpn-pannel configs")
		if err := o.moveDestination(ctx, r, s, s.UploadedIP); err != nil {
			r.Logf("warn", "vpn-pannel: %v", err)
		}
	}
	if err := o.reconcileUploads(ctx, r, s); err != nil {
		s.SyncStatus = models.SyncFailed
		o.save(r, s, "sync_status")
		return fmt.Errorf("vpn-pannel: %w", err)
	}
	return nil
}

func (o *Orchestrator) syncAll(ctx context.Context, r *jobs.Run) error {
	var servers []models.Server
	o.DB.Where("status IN ? AND xui_port > 0", []string{models.ServerActive, models.ServerFiltered}).Find(&servers)
	for i := range servers {
		id := servers[i].ID
		if _, err := o.Engine.Enqueue(KindSync, &id, struct{}{}, r.Job.Source); err != nil {
			return err
		}
	}
	r.Info("queued sync for %d servers", len(servers))
	return nil
}

// ---- upload ----

func (o *Orchestrator) upload(ctx context.Context, r *jobs.Run) error {
	var p struct {
		BaseName string `json:"base_name"`
	}
	_ = r.Params(&p)
	s, err := o.loadServer(r)
	if err != nil {
		return err
	}
	return o.uploadLinks(ctx, r, s, p.BaseName, nil)
}

// uploadLinks sends the server's links to every vpn-pannel. only narrows
// them to the given inbounds (those a config test passed); nil sends all.
func (o *Orchestrator) uploadLinks(ctx context.Context, r *jobs.Run, s *models.Server, baseName string, only map[uint]bool) error {
	r.Step("upload links to vpn-pannel")
	dests := o.Dests()
	if len(dests) == 0 {
		r.Info("vpn-pannel is not configured; skipped")
		return nil
	}
	return o.uploadTo(ctx, r, s, baseName, dests, only)
}

// AdsBaseName tells the two sets of a "both" panel apart: the ads set gets
// an "-ads" suffix so its names never collide with the normal set.
func AdsBaseName(base string, ads, both bool) string {
	if both && ads {
		return base + "-ads"
	}
	return base
}

// uploadTo sends the server's links to each of dests. It fails only when
// none of them took the upload.
func (o *Orchestrator) uploadTo(ctx context.Context, r *jobs.Run, s *models.Server, baseName string, dests []NamedDest, only map[uint]bool) error {
	snap, err := LoadSnapshot(o.DB)
	if err != nil {
		return err
	}
	us := o.forUpload(snap, s)
	if only != nil {
		var keep []models.InboundTemplate
		for _, t := range us.Inbounds {
			if only[t.ID] {
				keep = append(keep, t)
			} else if t.Enabled && t.Upload {
				r.Logf("warn", "%s did not pass the config test; left out of vpn-pannel", t.Remark)
			}
		}
		us.Inbounds = keep
	}
	var urls []string
	if o.locationPanelJSON(s) {
		// The location sends the panel's own JSON configs; no fallback to
		// links, which are what do not work there.
		urls, err = o.panelJSONEntries(ctx, us, s)
		if err != nil {
			return fmt.Errorf("panel JSON configs: %w", err)
		}
	} else {
		urls, err = UploadEntries(us, LinkHost(s))
		if err != nil {
			r.Logf("warn", "some links could not be built: %v", err)
		}
	}
	if len(urls) == 0 {
		if only != nil {
			return errors.New("no inbound marked for upload passed the config test")
		}
		return errors.New("no inbound is marked for upload")
	}
	if baseName == "" {
		baseName = o.panelBaseName(s)
	}
	if baseName == "" {
		baseName = strings.ToLower(firstNonEmpty(s.Country, "xx")) + "-" + s.Name
	}
	var errs []error
	ok := 0
	for _, d := range dests {
		ads := d.Ads
		if len(ads) == 0 {
			ads = []bool{false}
		}
		for _, a := range ads {
			name := AdsBaseName(baseName, a, len(ads) > 1)
			err := d.Upload(ctx, destination.UploadRequest{Links: urls, BaseName: name, Country: s.Country, Ads: a})
			if err != nil {
				r.Logf("warn", "%s: %v", d.Name, err)
				errs = append(errs, fmt.Errorf("%s: %w", d.Name, err))
				continue
			}
			ok++
			r.Info("%s: uploaded %d links as %s", d.Name, len(urls), name)
		}
	}
	if ok == 0 {
		return errors.Join(errs...)
	}
	now := time.Now()
	s.UploadedIP, s.UploadedAt = LinkHost(s), &now
	s.UploadedInbounds = joinIDs(uploadIDs(us))
	o.save(r, s, "uploaded_ip", "uploaded_at", "uploaded_inbounds")
	return nil
}

// uploadIDs are the IDs of the inbounds whose links go to vpn-pannel.
func uploadIDs(snap *Snapshot) map[uint]bool {
	ids := map[uint]bool{}
	for _, t := range snap.Inbounds {
		if t.Enabled && t.Upload {
			ids[t.ID] = true
		}
	}
	return ids
}

func joinIDs(ids map[uint]bool) string {
	list := make([]int, 0, len(ids))
	for id := range ids {
		list = append(list, int(id))
	}
	sort.Ints(list)
	parts := make([]string, len(list))
	for i, id := range list {
		parts[i] = strconv.Itoa(id)
	}
	return strings.Join(parts, ",")
}

func splitIDs(v string) map[uint]bool {
	ids := map[uint]bool{}
	for _, p := range strings.Split(v, ",") {
		if n, err := strconv.Atoi(strings.TrimSpace(p)); err == nil && n > 0 {
			ids[uint(n)] = true
		}
	}
	return ids
}

// reconcileUploads takes inbounds that were removed from the template (or
// disabled, or unmarked for upload) out of vpn-pannel. vpn-pannel cannot
// delete one config, so the server's address is cleared and what is still
// wanted goes up again. A server whose uploaded set is unknown (uploaded
// before this was tracked) is left alone.
func (o *Orchestrator) reconcileUploads(ctx context.Context, r *jobs.Run, s *models.Server) error {
	if s.UploadedAt == nil || s.UploadedIP == "" || s.UploadedInbounds == "" {
		return nil
	}
	dests := o.Dests()
	if len(dests) == 0 {
		return nil
	}
	snap, err := LoadSnapshot(o.DB)
	if err != nil {
		return err
	}
	wanted := uploadIDs(o.forUpload(snap, s))
	keep := map[uint]bool{}
	removed := 0
	for id := range splitIDs(s.UploadedInbounds) {
		if wanted[id] {
			keep[id] = true
		} else {
			removed++
		}
	}
	if removed == 0 {
		return nil
	}
	r.Step("remove dropped inbounds from vpn-pannel")
	r.Info("%d inbound(s) left the template; clearing %s in vpn-pannel", removed, s.UploadedIP)
	var errs []error
	for _, d := range dests {
		if err := d.DeleteAddress(ctx, s.UploadedIP); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", d.Name, err))
			continue
		}
		if hd, ok := d.Destination.(HostDestination); ok && s.CDNHostname != "" {
			if err := hd.DeleteHost(ctx, s.CDNHostname); err != nil && !errors.Is(err, destination.ErrNoHostRoute) {
				r.Logf("warn", "%s: clean-IP configs: %v", d.Name, err)
			}
		}
	}
	if len(errs) > 0 {
		// The old set may still be there; the stored list stays, so the
		// next sync tries again.
		return errors.Join(errs...)
	}
	if len(keep) == 0 {
		s.UploadedInbounds = ""
		o.save(r, s, "uploaded_inbounds")
		r.Info("no inbound is marked for upload any more; vpn-pannel cleared")
		return nil
	}
	if err := o.uploadTo(ctx, r, s, "", dests, keep); err != nil {
		// Cleared but not re-sent: keep the old list so the next sync
		// uploads again instead of thinking nothing changed.
		return fmt.Errorf("re-upload after removal: %w", err)
	}
	return nil
}

// ---- check ----

func (o *Orchestrator) check(ctx context.Context, r *jobs.Run) error {
	s, err := o.loadServer(r)
	if err != nil {
		return err
	}
	if s.PublicIP() == "" {
		return fmt.Errorf("server has no IP%s", s.Family())
	}
	r.Step("check reachability from Iran")
	res := o.Check(ctx, s.PublicIP(), o.checkPort(s))
	o.recordCheck(r, s, res)
	switch res.Verdict {
	case checker.Filtered:
		if s.Status != models.ServerFiltered {
			o.setStatus(r, s, models.ServerFiltered)
			o.notify("⛔️ سرور %s (%s) فیلتر شده است\n%s", s.Name, s.PublicIP(), res.Summary)
		}
		if o.Settings.Bool(settings.AutoRotate) {
			id := s.ID
			if _, err := o.Engine.Enqueue(KindRotate, &id, RotateParams{Reason: "auto: filtered"}, "auto"); err != nil {
				return err
			}
			r.Info("auto-rotate queued")
		}
	case checker.Healthy:
		if s.Status == models.ServerFiltered {
			o.setStatus(r, s, models.ServerActive)
			o.notify("✅ سرور %s (%s) دوباره در دسترس است", s.Name, s.PublicIP())
		}
	}
	return nil
}

// checkAll queues a check for every live server.
func (o *Orchestrator) checkAll(ctx context.Context, r *jobs.Run) error {
	var servers []models.Server
	o.DB.Where("status IN ?", []string{models.ServerActive, models.ServerFiltered}).Find(&servers)
	for i := range servers {
		id := servers[i].ID
		if _, err := o.Engine.Enqueue(KindCheck, &id, struct{}{}, r.Job.Source); err != nil {
			return err
		}
	}
	r.Info("queued checks for %d servers", len(servers))
	return nil
}

// ---- delete ----

func (o *Orchestrator) delete(ctx context.Context, r *jobs.Run) error {
	s, err := o.loadServer(r)
	if err != nil {
		return err
	}
	if s.Status == models.ServerDeleted {
		r.Info("server is already deleted")
		return nil
	}
	r.Step("delete server")
	o.setStatus(r, s, models.ServerDeleting)
	if s.ProviderRef != "" {
		prov, err := o.Provider(s.ProviderAccount)
		if err != nil {
			return err
		}
		err = prov.Delete(ctx, s.ProviderRef)
		switch {
		case errors.Is(err, providers.ErrNotFound):
			// Deleted in the provider's console: only the panel's side is left.
			r.Logf("warn", "%s no longer exists at %s; removing it from the panel only", s.ProviderRef, s.ProviderAccount.Name)
		case err != nil:
			return o.fail(r, s, fmt.Errorf("delete: %w", err))
		}
		// IPs that belonged to the account separately (DigitalOcean reserved
		// IPs) are released too; Hetzner primary IPs go with the server.
		if s.ProviderAccount.Type == "digitalocean" && s.IPRef != "" {
			if err := prov.ReleaseIP(ctx, s.IPRef); err != nil && !errors.Is(err, providers.ErrNotFound) {
				r.Logf("warn", "release %s: %v", s.IPRef, err)
			}
		}
	}
	o.retire(ctx, r, s, false)
	o.notify("🗑 سرور %s حذف شد", s.Name)
	return nil
}

// retire does everything after the provider let go of a server: its IP rows
// are released, its DNS record removed, its vpn-pannel configs deactivated (or
// deleted outright when purge is set) and the row marked deleted.
func (o *Orchestrator) retire(ctx context.Context, r *jobs.Run, s *models.Server, purge bool) {
	o.DB.Model(&models.IPAddress{}).Where("server_id = ? AND status = ?", s.ID, models.IPInUse).
		Updates(map[string]any{"status": models.IPReleased, "server_id": nil})
	o.removeDNS(ctx, r, s)
	addrs := []string{s.UploadedIP}
	if ip := s.PublicIP(); purge && ip != "" && ip != s.UploadedIP {
		addrs = append(addrs, ip)
	}
	// The CDN add-on's orange name carries the CDN configs; it is gone with
	// the server unless a replacement took it over (UploadedIP is then
	// cleared too, and the configs stay).
	if s.CDNHostname != "" && s.UploadedIP != "" {
		addrs = append(addrs, s.CDNHostname)
	}
	for _, addr := range addrs {
		if addr == "" {
			continue
		}
		for _, d := range o.Dests() {
			if purge {
				if err := d.DeleteAddress(ctx, addr); err != nil {
					r.Logf("warn", "%s: could not delete configs on %s: %v", d.Name, addr, err)
				} else {
					r.Info("%s: configs on %s deleted", d.Name, addr)
				}
				continue
			}
			if err := d.SetActive(ctx, addr, false); err != nil {
				r.Logf("warn", "%s: could not deactivate configs on %s: %v", d.Name, addr, err)
			} else {
				r.Info("%s: configs on %s deactivated", d.Name, addr)
			}
		}
	}
	if s.CDNHostname != "" && s.UploadedIP != "" {
		o.retireCleanConfigs(ctx, r, s.CDNHostname, purge)
	}
	s.Status = models.ServerDeleted
	o.save(r, s, "status")
}

// ---- versions ----

func (o *Orchestrator) xrayVersion(ctx context.Context, r *jobs.Run) error {
	var p VersionParams
	_ = r.Params(&p)
	s, err := o.loadServer(r)
	if err != nil {
		return err
	}
	version := firstNonEmpty(p.Version, s.WantXrayVer, o.Settings.Get(settings.XrayVersion))
	if version == "" {
		return errors.New("no xray version given")
	}
	r.Step("install xray " + version)
	remote, _, err := o.Connect(ctx, s)
	if err != nil {
		return fmt.Errorf("ssh: %w", err)
	}
	defer remote.Close()
	panel := o.PanelFor(s, remote)
	if err := panel.InstallXray(ctx, version); err != nil {
		return err
	}
	if st, err := panel.Status(ctx); err == nil && knownXray(st.Xray.Version) {
		s.XrayVersion = st.Xray.Version
	} else {
		s.XrayVersion = strings.TrimPrefix(version, "v")
	}
	o.save(r, s, "xray_version")
	r.Info("xray is now %s", s.XrayVersion)
	return nil
}

func (o *Orchestrator) panelVersion(ctx context.Context, r *jobs.Run) error {
	var p VersionParams
	_ = r.Params(&p)
	s, err := o.loadServer(r)
	if err != nil {
		return err
	}
	remote, _, err := o.Connect(ctx, s)
	if err != nil {
		return fmt.Errorf("ssh: %w", err)
	}
	defer remote.Close()
	if err := o.installPanel(ctx, r, s, remote, p.Version); err != nil {
		return err
	}
	return nil
}

// RemoteFor exposes an SSH session for callers outside jobs (e.g. reading
// live xray versions for the UI).
func (o *Orchestrator) RemoteFor(ctx context.Context, s *models.Server) (Remote, Panel, error) {
	remote, _, err := o.Connect(ctx, s)
	if err != nil {
		return nil, nil, err
	}
	return remote, o.PanelFor(s, remote), nil
}

var _ Remote = (*sshx.Client)(nil)

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}
