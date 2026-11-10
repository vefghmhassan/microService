package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"microserver/internal/jobs"
	"microserver/internal/models"
	"microserver/internal/services/checker"
	"microserver/internal/services/providers"
	"microserver/internal/services/settings"
)

// AutoParams drive automatic placement: walk the location list in priority
// order, build as many candidates as a location allows at once, check them all
// from Iran in parallel, keep the healthy ones and only then delete the rest.
type AutoParams struct {
	Count    int    `json:"count"`
	Upload   *bool  `json:"upload"`
	BaseName string `json:"base_name"`
	// ReplaceServerID moves the configs of an existing (filtered) server to
	// the new one in vpn-pannel, then deletes the old server.
	ReplaceServerID uint `json:"replace_server_id"`
	// ExcludeAccountID/ExcludeRegion skip the location the replaced server
	// just failed in.
	ExcludeAccountID uint   `json:"exclude_account_id"`
	ExcludeRegion    string `json:"exclude_region"`
	// PreferServerLocation tries the replaced server's own location first (a
	// replacement for traffic, not for filtering, can stay where it was).
	PreferServerLocation bool   `json:"prefer_server_location"`
	Reason               string `json:"reason"`
	// IPFamily ("v4"/"v6") overrides the family each location builds with.
	IPFamily string `json:"ip_family"`
	// Attempt counts the servers already built and deleted for this request
	// because none of their configs worked.
	Attempt int `json:"attempt,omitempty"`
}

// InstallParams are the install job's parameters.
type InstallParams struct {
	Version         string `json:"version"`
	Upload          *bool  `json:"upload"`
	BaseName        string `json:"base_name"`
	ReplaceServerID uint   `json:"replace_server_id"`
	// Fresh marks a server built for this job: one on which no config works
	// is deleted and another is built. A reinstall never deletes its server.
	Fresh   bool `json:"fresh,omitempty"`
	Attempt int  `json:"attempt,omitempty"`
}

// maxConfigRetries is how many more servers are built after one on which no
// config worked.
const maxConfigRetries = 2

// rejectNewServer gives up on a new server on which no config works: it is
// deleted, and requeue builds the next one unless enough have been tried.
// Nothing of it reached vpn-pannel, and a server it was to replace is left
// as it is. The returned error fails the job.
func (o *Orchestrator) rejectNewServer(r *jobs.Run, s *models.Server, attempt int, replaceID uint, requeue func(next int) error) error {
	s.Status, s.LastError = models.ServerError, "no config worked on this server"
	o.save(r, s, "status", "last_error")
	id := s.ID
	// Queued behind this job, which holds the server until it returns.
	if _, err := o.Engine.Enqueue(KindDelete, &id, struct{}{}, r.Job.Source); err != nil {
		r.Logf("warn", "delete %s: %v", s.Name, err)
	}
	if attempt < maxConfigRetries {
		if err := requeue(attempt + 1); err != nil {
			return fmt.Errorf("no config worked on %s; it is being deleted, but the next server could not be queued: %w", s.Name, err)
		}
		o.notify("⚠️ روی سرور %s هیچ کانفیگی کار نکرد؛ حذف شد و سرور بعدی (%d از %d) ساخته می‌شود", s.Name, attempt+1, maxConfigRetries)
		return fmt.Errorf("no config worked on %s: it is being deleted and replacement %d of %d is queued", s.Name, attempt+1, maxConfigRetries)
	}
	if replaceID != 0 {
		// Let the server that was to be replaced be replaced again later.
		o.DB.Model(&models.Server{}).Where("id = ?", replaceID).Update("replacing_at", nil)
	}
	o.notify("❌ روی %d سرور پشت‌سرهم هیچ کانفیگی کار نکرد؛ ساخت متوقف شد. تمپلیت را بررسی کنید.", maxConfigRetries+1)
	return fmt.Errorf("no config worked on %d servers in a row; giving up (check the template)", maxConfigRetries+1)
}

// candidate is one server built while probing a location.
type candidate struct {
	s       *models.Server
	verdict string
	err     error
}

func (o *Orchestrator) provisionAuto(ctx context.Context, r *jobs.Run) error {
	var p AutoParams
	if err := r.Params(&p); err != nil {
		return err
	}
	if p.Count < 1 {
		p.Count = 1
	}
	if p.ReplaceServerID != 0 {
		// If no replacement is found, let the server be replaced again later.
		defer func() {
			if r.Job.Status != "" && o.keptSoFar(r) == 0 {
				o.DB.Model(&models.Server{}).Where("id = ?", p.ReplaceServerID).Update("replacing_at", nil)
			}
		}()
	}
	if p.Reason != "" {
		r.Info("reason: %s", p.Reason)
	}
	var locs []models.Location
	if err := o.DB.Preload("ProviderAccount").Where("enabled = ?", true).Order("priority, id").Find(&locs).Error; err != nil {
		return err
	}
	if p.PreferServerLocation && p.ReplaceServerID != 0 {
		var old models.Server
		if err := o.DB.Preload("ProviderAccount").First(&old, p.ReplaceServerID).Error; err == nil && old.ProviderAccount != nil {
			// A synthetic entry (ID 0) for the old server's own location.
			own := models.Location{
				ProviderAccountID: old.ProviderAccountID, ProviderAccount: old.ProviderAccount,
				Region: old.Region, Plan: old.Plan, Image: old.Image, Country: old.Country,
				Capacity: 1, Enabled: true, DNS: old.DNS, IPFamily: old.Family(),
			}
			locs = append([]models.Location{own}, locs...)
		}
	}
	if len(locs) == 0 {
		return errors.New("no enabled location in the priority list")
	}

	need := p.Count - o.keptSoFar(r)
	for i := range locs {
		if need <= 0 {
			break
		}
		loc := &locs[i]
		key := "loc:" + strconv.Itoa(int(loc.ID))
		if r.Done(key) {
			continue
		}
		if loc.ProviderAccount == nil || !loc.ProviderAccount.Enabled {
			continue
		}
		if loc.ProviderAccountID == p.ExcludeAccountID && loc.Region == p.ExcludeRegion {
			r.Info("skipping %s/%s: the server being replaced failed there", loc.ProviderAccount.Name, loc.Region)
			continue
		}
		r.Step(fmt.Sprintf("location %d: %s %s/%s", i+1, loc.ProviderAccount.Name, loc.Region, loc.Plan))
		kept, err := o.tryLocation(ctx, r, loc, need, firstNonEmpty(p.IPFamily, loc.IPFamily))
		if err != nil && ctx.Err() != nil {
			return err
		}
		if err != nil {
			r.Logf("warn", "%s/%s: %v", loc.ProviderAccount.Name, loc.Region, err)
		}
		for _, s := range kept {
			ip := InstallParams{Upload: p.Upload, BaseName: p.BaseName, ReplaceServerID: p.ReplaceServerID, Fresh: true, Attempt: p.Attempt}
			if ip.Upload == nil {
				up := o.Settings.Bool(settings.VPNPanelAuto)
				ip.Upload = &up
			}
			id := s.ID
			if _, err := o.Engine.Enqueue(KindInstall, &id, ip, r.Job.Source); err != nil {
				return err
			}
			r.Info("keeping %s (%s); installing 3x-ui in job of its own", s.Name, s.PublicIP())
			// Only the first kept server takes over a replaced one.
			p.ReplaceServerID = 0
		}
		r.Set("kept", strconv.Itoa(o.keptSoFar(r)+len(kept)))
		r.Mark(key)
		need -= len(kept)
	}
	if need > 0 {
		return fmt.Errorf("found %d of %d healthy servers after trying every location", p.Count-need, p.Count)
	}
	return nil
}

func (o *Orchestrator) keptSoFar(r *jobs.Run) int {
	n, _ := strconv.Atoi(r.GetString("kept"))
	return n
}

// tryLocation builds up to the location's capacity at once, checks them in
// parallel and returns at most need healthy servers. Every other candidate is
// deleted afterwards, so a filtered IP is never handed back to us mid-search.
func (o *Orchestrator) tryLocation(ctx context.Context, r *jobs.Run, loc *models.Location, need int, family string) ([]*models.Server, error) {
	acc := loc.ProviderAccount
	prov, err := o.Provider(acc)
	if err != nil {
		return nil, err
	}
	max := loc.Capacity
	if max <= 0 {
		max = o.Settings.Int(settings.AutoMaxPerLoc)
	}
	if max < need {
		max = need
	}

	// Creation is a quick API call; do it in sequence so a "limit" error
	// cleanly marks where the location ran out.
	var cands []*candidate
	for i := 0; i < max; i++ {
		s := &models.Server{
			Name:              sanitizeName(fmt.Sprintf("ms-%s-%s", loc.Region, randomSuffix(2))),
			ProviderAccountID: acc.ID, ProviderAccount: acc,
			Region: loc.Region, Plan: loc.Plan,
			Image:   firstNonEmpty(loc.Image, acc.DefaultImage, prov.Capabilities().DefaultImage),
			Country: strings.ToUpper(firstNonEmpty(loc.Country, acc.Country)),
			Status:  models.ServerCreating, SyncStatus: models.SyncPending, XUIScheme: "http",
			LocationID: loc.ID, IPFamily: models.NormFamily(family),
		}
		if a := o.locationAddon(loc); a != nil {
			ApplyAddon(s, a)
		} else if loc.DNS.On() {
			s.DNS = loc.DNS
		}
		if err := o.DB.Create(s).Error; err != nil {
			return nil, err
		}
		inst, err := prov.Create(ctx, o.createRequest(s))
		if err != nil {
			s.Status, s.LastError = models.ServerDeleted, truncate(err.Error(), 500)
			o.save(r, s, "status", "last_error")
			if providers.IsCapacityErr(err) {
				r.Info("location full after %d servers (%v)", i, truncate(err.Error(), 160))
				break
			}
			if i == 0 {
				return nil, fmt.Errorf("create: %w", err)
			}
			r.Logf("warn", "create #%d: %v", i+1, err)
			break
		}
		s.ProviderRef = inst.Ref
		o.save(r, s, "provider_ref")
		cands = append(cands, &candidate{s: s})
	}
	if len(cands) == 0 {
		return nil, errors.New("no server could be created here")
	}
	r.Info("built %d candidates; checking them from Iran in parallel", len(cands))

	var wg sync.WaitGroup
	for _, c := range cands {
		wg.Add(1)
		go func(c *candidate) {
			defer wg.Done()
			c.verdict, c.err = o.probeCandidate(ctx, r, prov, c.s)
		}(c)
	}
	wg.Wait()

	// Healthy first; when no source could decide at all, an unknown one is
	// better than nothing.
	var kept []*models.Server
	for _, want := range []string{checker.Healthy, checker.Unknown} {
		for _, c := range cands {
			if len(kept) < need && c.err == nil && c.verdict == want {
				if want == checker.Unknown {
					r.Logf("warn", "%s kept without a verdict (no Iranian probe answered)", c.s.PublicIP())
				}
				kept = append(kept, c.s)
			}
		}
		if len(kept) > 0 {
			break
		}
	}
	keptSet := map[uint]bool{}
	for _, s := range kept {
		keptSet[s.ID] = true
		s.Status = models.ServerInstalling
		o.save(r, s, "status")
	}

	// Only now are the rest deleted.
	for _, c := range cands {
		if keptSet[c.s.ID] {
			continue
		}
		reason := c.verdict
		if c.err != nil {
			reason = c.err.Error()
		}
		o.discardCandidate(ctx, r, prov, c.s, c.verdict == checker.Filtered, reason)
	}
	return kept, nil
}

// probeCandidate waits for a candidate to boot and checks its IP from Iran.
func (o *Orchestrator) probeCandidate(ctx context.Context, r *jobs.Run, prov providers.Provider, s *models.Server) (string, error) {
	inst, err := providers.WaitRunning(ctx, prov, s.ProviderRef, 15*time.Minute)
	if err != nil {
		return "", fmt.Errorf("wait running: %w", err)
	}
	s.IPv4, s.IPRef, s.IPv6 = inst.IPv4, inst.IPRef, inst.IPv6
	s.Status = models.ServerChecking
	o.save(r, s, "ipv4", "ip_ref", "ipv6", "status")
	o.poolUse(s)
	if s.PublicIP() == "" {
		return "", fmt.Errorf("the provider gave the server no public IP%s", s.Family())
	}
	if err := o.waitBooted(ctx, r, s); err != nil {
		return "", err
	}
	for attempt := 0; ; attempt++ {
		res := o.Check(ctx, s.PublicIP(), o.checkPort(s))
		o.recordCheck(r, s, res)
		if res.Verdict != checker.Down || attempt >= 3 {
			return res.Verdict, nil
		}
		if err := sleep(ctx, o.RetryDelay); err != nil {
			return "", err
		}
	}
}

func (o *Orchestrator) discardCandidate(ctx context.Context, r *jobs.Run, prov providers.Provider, s *models.Server, filtered bool, reason string) {
	dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	defer cancel()
	if err := prov.Delete(dctx, s.ProviderRef); err != nil && !errors.Is(err, providers.ErrNotFound) {
		r.Logf("warn", "delete candidate %s: %v", s.Name, err)
		s.Status, s.LastError = models.ServerError, "candidate could not be deleted: "+err.Error()
		o.save(r, s, "status", "last_error")
		return
	}
	if s.IPv4 != "" {
		o.poolRetire(s, s.IPv4, s.IPRef, false, filtered, false, "auto placement: "+reason)
	}
	s.Status = models.ServerDeleted
	s.LastError = truncate("discarded: "+reason, 500)
	o.save(r, s, "status", "last_error")
	r.Info("deleted candidate %s (%s): %s", s.Name, s.PublicIP(), reason)
}

// handOver runs after an install job's config test: either hand over a
// replaced server's configs in vpn-pannel, or upload fresh links. Only the
// inbounds the test passed (g) end up there.
func (o *Orchestrator) handOver(ctx context.Context, r *jobs.Run, s *models.Server, p InstallParams, g gateResult) {
	if len(g.Failed) > 0 {
		o.notify("⚠️ سرور %s: %d از %d کانفیگ در تست کار کرد و فقط همان‌ها روی پنل می‌روند.\nردشده: %s",
			s.Name, len(g.Passed), g.Total, strings.Join(g.Failed, "، "))
	}
	if p.ReplaceServerID != 0 {
		var old models.Server
		if err := o.DB.First(&old, p.ReplaceServerID).Error; err != nil {
			r.Logf("warn", "server to replace (%d): %v", p.ReplaceServerID, err)
			return
		}
		if old.PanelName != "" && s.PanelName == "" {
			// The configs keep their names in vpn-pannel; so does the server.
			s.PanelName = old.PanelName
			o.save(r, s, "panel_name")
		}
		switch {
		case old.UploadedIP != "" && len(g.Failed) == 0:
			r.Step("move vpn-pannel configs from " + old.Name)
			if err := o.moveDestination(ctx, r, s, old.UploadedIP); err != nil {
				r.Logf("warn", "vpn-pannel: %v", err)
			}
		case old.UploadedIP != "":
			// vpn-pannel can only move an address as a whole, which would
			// carry the broken configs along.
			if err := o.replaceWithWorking(ctx, r, s, &old, p.BaseName, g.only()); err != nil {
				r.Logf("error", "vpn-pannel: %v; %s is kept", err, old.Name)
				o.DB.Model(&old).Update("replacing_at", nil)
				o.notify("❌ کانفیگ‌های سالم %s روی پنل نرفت (%s)؛ سرور قبلی %s حذف نشد.", s.Name, truncate(err.Error(), 200), old.Name)
				return
			}
		case p.Upload != nil && *p.Upload:
			if err := o.uploadLinks(ctx, r, s, p.BaseName, g.only()); err != nil {
				r.Logf("warn", "upload to vpn-pannel failed: %v", err)
			}
		}
		// The old server's configs now point here; delete it without
		// deactivating anything in vpn-pannel.
		o.DB.Model(&old).Updates(map[string]any{"uploaded_ip": ""})
		id := old.ID
		if _, err := o.Engine.Enqueue(KindDelete, &id, struct{}{}, r.Job.Source); err != nil {
			r.Logf("warn", "delete %s: %v", old.Name, err)
		}
		o.notify("🌍 سرور %s فیلتر بود و با %s (%s) جایگزین شد", old.Name, s.Name, LinkHost(s))
		return
	}
	if p.Upload != nil && *p.Upload {
		if err := o.uploadLinks(ctx, r, s, p.BaseName, g.only()); err != nil {
			r.Logf("warn", "upload to vpn-pannel failed: %v", err)
		}
	}
}

// replaceWithWorking is the hand-over of a replaced server when some of the
// new server's configs do not work: the old address's configs are removed
// from vpn-pannel and only the working ones are uploaded for the new server.
// When both servers share a name the old configs must go first, or the delete
// would take the new ones with it; otherwise they go only after the upload
// succeeded.
func (o *Orchestrator) replaceWithWorking(ctx context.Context, r *jobs.Run, s *models.Server, old *models.Server, baseName string, only map[uint]bool) error {
	dests := o.Dests()
	if len(dests) == 0 {
		r.Info("vpn-pannel is not configured; skipped")
		return nil
	}
	oldAddr := old.UploadedIP
	r.Step("replace vpn-pannel configs on " + oldAddr + " with the working ones")
	if old.CDNHostname != "" {
		// Clean-IP configs are matched by the orange name, which the new
		// server may share: they go before anything new is uploaded.
		o.retireCleanConfigs(ctx, r, old.CDNHostname, true)
	}
	drop := func() {
		for _, d := range dests {
			if err := d.DeleteAddress(ctx, oldAddr); err != nil {
				r.Logf("warn", "%s: could not delete configs on %s: %v", d.Name, oldAddr, err)
			} else {
				r.Info("%s: configs on %s deleted", d.Name, oldAddr)
			}
		}
	}
	same := oldAddr == LinkHost(s)
	if same {
		drop()
	}
	if err := o.uploadTo(ctx, r, s, baseName, dests, only); err != nil {
		return err
	}
	if !same {
		drop()
	}
	return nil
}
