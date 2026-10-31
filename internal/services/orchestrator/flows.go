package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"microserver/internal/jobs"
	"microserver/internal/models"
	"microserver/internal/services/checker"
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
	r.Info("server %s (#%d) at %s %s/%s", s.Name, s.ID, acc.Type, s.Region, s.Plan)

	if !r.Done("create") {
		r.Step("create server")
		req := o.createRequest(&s)
		if ip := o.pickReusable(acc.ID, s.Region); ip != nil && prov.Capabilities().CanKeepOldIP {
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
		s.IPv4, s.IPRef = inst.IPv4, inst.IPRef
		if inst.Region != "" {
			s.Region = inst.Region
		}
		s.Status = models.ServerChecking
		o.save(r, &s, "ipv4", "ip_ref", "region", "status")
		o.poolUse(&s)
		r.Info("running at %s", s.IPv4)
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

	upload := o.Settings.Bool(settings.VPNPanelAuto)
	if p.Upload != nil {
		upload = *p.Upload
	}
	if upload && !r.Done("upload") {
		if err := o.uploadLinks(ctx, r, &s, p.BaseName); err != nil {
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
	o.notify("✅ سرور %s آماده شد\nIP: %s\n%s", s.Name, s.IPv4, s.LastCheckNote)
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
		res := o.Check(ctx, s.IPv4, o.checkPort(s))
		o.recordCheck(r, s, res)
		switch res.Verdict {
		case checker.Healthy:
			return nil
		case checker.Down:
			// Usually sshd is still starting; nothing to do with filtering.
			if downRetries++; downRetries > 6 {
				return fmt.Errorf("%s does not answer from outside Iran either; giving up", s.IPv4)
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
		r.Info("%s is filtered; getting a new IP (attempt %d/%d)", s.IPv4, attempt, maxAttempts)
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
	opts := providers.RotateOptions{KeepOld: keep && caps.CanKeepOldIP, Create: o.createRequest(s)}
	if reuseIPID != 0 {
		var ip models.IPAddress
		if err := o.DB.First(&ip, reuseIPID).Error; err != nil {
			return nil, fmt.Errorf("ip %d: %w", reuseIPID, err)
		}
		opts.ReuseIPRef = ip.Ref
	} else if caps.CanKeepOldIP {
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

	s.IPv4, s.IPRef = res.Instance.IPv4, res.Instance.IPRef
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
	r.Info("new IP %s (old %s %s)", s.IPv4, res.OldIP, map[bool]string{true: "kept", false: "released"}[res.OldKept])
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
	oldUploaded := firstNonEmpty(s.UploadedIP, s.IPv4)
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
	o.notify("🔄 IP سرور %s عوض شد\n%s → %s", s.Name, oldUploaded, s.IPv4)
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
		Count: 1, Upload: &up, ReplaceServerID: s.ID,
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
		if err := o.uploadTo(ctx, r, s, "", fresh); err != nil {
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
	if !r.Done("finish") {
		o.finishInstall(ctx, r, s, p)
		r.Mark("finish")
	}
	s.Status = models.ServerActive
	s.LastError = ""
	o.save(r, s, "status", "last_error")
	o.startWatch(s)
	if p.ReplaceServerID == 0 && p.Upload != nil {
		o.notify("✅ سرور %s آماده شد\nIP: %s\n%s", s.Name, s.IPv4, s.LastCheckNote)
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
	if err := o.installPanel(ctx, r, s, remote, version); err != nil {
		return err
	}
	// Before the template: TLS inbounds need the files when xray starts.
	if err := o.pushCert(ctx, r, s, remote); err != nil {
		r.Logf("warn", "SSL: %v", err)
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
	if err := Apply(ctx, panel, snap, r.Info); err != nil {
		s.SyncStatus = models.SyncFailed
		o.save(r, s, "sync_status")
		return err
	}
	if st, err := panel.Status(ctx); err == nil && st.Xray.Version != "" {
		s.XrayVersion = st.Xray.Version
	}
	s.AppliedHash = snap.Hash
	s.SyncStatus = models.SyncSynced
	o.save(r, s, "applied_hash", "sync_status", "xray_version")
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
	return o.applyWith(ctx, r, s, remote)
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
	return o.uploadLinks(ctx, r, s, p.BaseName)
}

func (o *Orchestrator) uploadLinks(ctx context.Context, r *jobs.Run, s *models.Server, baseName string) error {
	r.Step("upload links to vpn-pannel")
	dests := o.Dests()
	if len(dests) == 0 {
		r.Info("vpn-pannel is not configured; skipped")
		return nil
	}
	return o.uploadTo(ctx, r, s, baseName, dests)
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
func (o *Orchestrator) uploadTo(ctx context.Context, r *jobs.Run, s *models.Server, baseName string, dests []NamedDest) error {
	snap, err := LoadSnapshot(o.DB)
	if err != nil {
		return err
	}
	urls, err := UploadEntries(snap, LinkHost(s))
	if err != nil {
		r.Logf("warn", "some links could not be built: %v", err)
	}
	if len(urls) == 0 {
		return errors.New("no inbound is marked for upload")
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
	o.save(r, s, "uploaded_ip", "uploaded_at")
	return nil
}

// ---- check ----

func (o *Orchestrator) check(ctx context.Context, r *jobs.Run) error {
	s, err := o.loadServer(r)
	if err != nil {
		return err
	}
	if s.IPv4 == "" {
		return errors.New("server has no IP")
	}
	r.Step("check reachability from Iran")
	res := o.Check(ctx, s.IPv4, o.checkPort(s))
	o.recordCheck(r, s, res)
	switch res.Verdict {
	case checker.Filtered:
		if s.Status != models.ServerFiltered {
			o.setStatus(r, s, models.ServerFiltered)
			o.notify("⛔️ سرور %s (%s) فیلتر شده است\n%s", s.Name, s.IPv4, res.Summary)
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
			o.notify("✅ سرور %s (%s) دوباره در دسترس است", s.Name, s.IPv4)
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
	r.Step("delete server")
	o.setStatus(r, s, models.ServerDeleting)
	if s.ProviderRef != "" {
		prov, err := o.Provider(s.ProviderAccount)
		if err != nil {
			return err
		}
		if err := prov.Delete(ctx, s.ProviderRef); err != nil && !errors.Is(err, providers.ErrNotFound) {
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
	o.DB.Model(&models.IPAddress{}).Where("server_id = ? AND status = ?", s.ID, models.IPInUse).
		Updates(map[string]any{"status": models.IPReleased, "server_id": nil})
	o.removeDNS(ctx, r, s)
	if s.UploadedIP != "" {
		for _, d := range o.Dests() {
			if err := d.SetActive(ctx, s.UploadedIP, false); err != nil {
				r.Logf("warn", "%s: could not deactivate configs on %s: %v", d.Name, s.UploadedIP, err)
			} else {
				r.Info("%s: configs on %s deactivated", d.Name, s.UploadedIP)
			}
		}
	}
	s.Status = models.ServerDeleted
	o.save(r, s, "status")
	o.notify("🗑 سرور %s حذف شد", s.Name)
	return nil
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
	if st, err := panel.Status(ctx); err == nil {
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
