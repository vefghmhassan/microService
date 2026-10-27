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
}

// InstallParams are the install job's parameters.
type InstallParams struct {
	Version         string `json:"version"`
	Upload          *bool  `json:"upload"`
	BaseName        string `json:"base_name"`
	ReplaceServerID uint   `json:"replace_server_id"`
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
				Capacity: 1, Enabled: true, DNS: old.DNS,
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
		kept, err := o.tryLocation(ctx, r, loc, need)
		if err != nil && ctx.Err() != nil {
			return err
		}
		if err != nil {
			r.Logf("warn", "%s/%s: %v", loc.ProviderAccount.Name, loc.Region, err)
		}
		for _, s := range kept {
			ip := InstallParams{Upload: p.Upload, BaseName: p.BaseName, ReplaceServerID: p.ReplaceServerID}
			if ip.Upload == nil {
				up := o.Settings.Bool(settings.VPNPanelAuto)
				ip.Upload = &up
			}
			id := s.ID
			if _, err := o.Engine.Enqueue(KindInstall, &id, ip, r.Job.Source); err != nil {
				return err
			}
			r.Info("keeping %s (%s); installing 3x-ui in job of its own", s.Name, s.IPv4)
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
func (o *Orchestrator) tryLocation(ctx context.Context, r *jobs.Run, loc *models.Location, need int) ([]*models.Server, error) {
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
			LocationID: loc.ID,
		}
		if loc.DNS.On() {
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
					r.Logf("warn", "%s kept without a verdict (no Iranian probe answered)", c.s.IPv4)
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
	s.IPv4, s.IPRef = inst.IPv4, inst.IPRef
	s.Status = models.ServerChecking
	o.save(r, s, "ipv4", "ip_ref", "status")
	o.poolUse(s)
	if err := o.waitBooted(ctx, r, s); err != nil {
		return "", err
	}
	for attempt := 0; ; attempt++ {
		res := o.Check(ctx, s.IPv4, o.checkPort(s))
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
	r.Info("deleted candidate %s (%s): %s", s.Name, s.IPv4, reason)
}

// finishInstall runs after an install job's 3x-ui install: either hand over a
// replaced server's configs in vpn-pannel, or upload fresh links.
func (o *Orchestrator) finishInstall(ctx context.Context, r *jobs.Run, s *models.Server, p InstallParams) {
	if s.Hostname != "" {
		r.Step("DNS " + s.Hostname)
		_ = o.syncDNS(ctx, r, s)
	}
	if p.ReplaceServerID != 0 {
		var old models.Server
		if err := o.DB.First(&old, p.ReplaceServerID).Error; err != nil {
			r.Logf("warn", "server to replace (%d): %v", p.ReplaceServerID, err)
			return
		}
		if old.UploadedIP != "" {
			r.Step("move vpn-pannel configs from " + old.Name)
			if err := o.moveDestination(ctx, r, s, old.UploadedIP); err != nil {
				r.Logf("warn", "vpn-pannel: %v", err)
			}
		} else if p.Upload != nil && *p.Upload {
			if err := o.uploadLinks(ctx, r, s, p.BaseName); err != nil {
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
		if err := o.uploadLinks(ctx, r, s, p.BaseName); err != nil {
			r.Logf("warn", "upload to vpn-pannel failed: %v", err)
		}
	}
}
