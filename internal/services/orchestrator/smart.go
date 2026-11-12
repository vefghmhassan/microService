package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"microserver/internal/jobs"
	"microserver/internal/models"
	"microserver/internal/services/destination"
	"microserver/internal/services/linkgen"
	"microserver/internal/services/smart"
)

// KindSmartSync rebuilds every smart config and replaces it in vpn-pannel.
const KindSmartSync = "smart_sync"

// SmartDestination is the per-node surface smart configs need; a
// destination without it gets no smart configs.
type SmartDestination interface {
	FindByName(ctx context.Context, name string) (destination.PanelNode, error)
	UpsertJSON(ctx context.Context, n destination.SmartNode) (uint, error)
	SetNodeActive(ctx context.Context, id uint, active bool) error
	DeleteNode(ctx context.Context, id uint) error
}

// SmartTarget is one smart config a profile asks for: one server's, or
// (ServerID 0) every server's.
type SmartTarget struct {
	ServerID uint
	Name     string
	Country  string
	Items    []smart.Item
	// Off is why the config is kept switched off (server not active, …).
	Off string
}

// smartState debounces the triggers into one job.
type smartState struct {
	mu     sync.Mutex
	run    sync.Mutex // one smart sync at a time
	timer  *time.Timer
	verify bool
}

// SmartDelay is how long QueueSmartSync waits for more changes.
var SmartDelay = 20 * time.Second

// smartKinds are the jobs after which the set of servers or their links
// may have changed. A check only changes them when a status flipped, so it
// does not make the panel be read again.
var smartKinds = map[string]bool{
	KindProvision: true, KindInstall: true, KindRotate: true, KindDelete: true,
	KindSync: true, KindUpload: true, KindPurgeProvider: true, KindProvisionAuto: true,
	KindCheck: true, KindDNSSync: true,
}

// OnJobFinish queues a smart sync after jobs that change servers.
func (o *Orchestrator) OnJobFinish(j *models.Job) {
	if j.Status != models.JobDone || !smartKinds[j.Kind] {
		return
	}
	o.QueueSmartSync(j.Kind != KindCheck)
}

// QueueSmartSync schedules a smart sync after SmartDelay; calls in between
// fold into it. verify reads every node back from the panel even when its
// config did not change (to undo IP-based edits done to it there).
func (o *Orchestrator) QueueSmartSync(verify bool) {
	if o == nil || o.Engine == nil {
		return
	}
	var n int64
	o.DB.Model(&models.SmartProfile{}).Where("enabled = ?", true).Count(&n)
	if n == 0 {
		o.DB.Model(&models.SmartUpload{}).Count(&n)
		if n == 0 {
			return
		}
	}
	st := &o.smart
	st.mu.Lock()
	defer st.mu.Unlock()
	st.verify = st.verify || verify
	if st.timer != nil {
		return
	}
	st.timer = time.AfterFunc(SmartDelay, func() {
		st.mu.Lock()
		v := st.verify
		st.timer, st.verify = nil, false
		st.mu.Unlock()
		var queued int64
		o.DB.Model(&models.Job{}).Where("kind = ? AND status = ?", KindSmartSync, models.JobQueued).Count(&queued)
		if queued > 0 {
			return
		}
		if _, err := o.Engine.Enqueue(KindSmartSync, nil, SmartSyncParams{Verify: v}, "auto"); err != nil {
			o.notify("smart sync: %v", err)
		}
	})
}

// SmartSyncParams are the smart_sync job's parameters.
type SmartSyncParams struct {
	Verify bool `json:"verify"`
}

// ---- building ----

func csvSet(v string) map[string]bool {
	out := map[string]bool{}
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out[strings.ToUpper(p)] = true
		}
	}
	return out
}

// smartBase is the base name of a server's configs in vpn-pannel, without
// picking a new number (uploadTo does that).
func smartBase(s *models.Server) string {
	if s.PanelName != "" {
		return s.PanelName
	}
	return strings.ToLower(firstNonEmpty(s.Country, "xx")) + "-" + s.Name
}

func smartName(p *models.SmartProfile, s *models.Server) string {
	r := strings.NewReplacer("{name}", p.Name)
	if s != nil {
		r = strings.NewReplacer("{name}", p.Name, "{base}", smartBase(s), "{server}", s.Name, "{country}", strings.ToLower(s.Country))
	}
	n := strings.TrimSpace(r.Replace(p.NameTemplate))
	if len(n) > 90 {
		n = n[:90]
	}
	return n
}

// smartItems are a server's working links: the inbounds that passed the
// config test and went to vpn-pannel (UploadedInbounds), narrowed to want.
func (o *Orchestrator) smartItems(snap *Snapshot, s *models.Server, want map[uint]bool) ([]smart.Item, error) {
	us := o.forUpload(snap, s)
	working := splitIDs(s.UploadedInbounds)
	host := LinkHost(s)
	var items []smart.Item
	var errs []error
	for _, t := range us.Inbounds {
		if !t.Enabled || !t.Upload || !working[t.ID] || (len(want) > 0 && !want[t.ID]) {
			continue
		}
		addr, sni := us.target(t, host)
		ls, err := linkgen.LinksVia(t.Protocol, t.Settings, t.StreamSettings, addr, sni, t.Port, t.Remark)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", t.Remark, err))
			continue
		}
		cl, err := us.cleanLinks(t)
		if err != nil {
			errs = append(errs, err)
		}
		for _, l := range append(ls, cl...) {
			items = append(items, smart.Item{URL: l.URL, Label: s.Name + " · " + t.Remark})
		}
	}
	return items, errors.Join(errs...)
}

// SmartTargets lists the configs profile p asks for. Servers come oldest
// first, so proxy-1 (whose address vpn-pannel files the node under) is the
// server least likely to move.
func (o *Orchestrator) SmartTargets(snap *Snapshot, p *models.SmartProfile) ([]SmartTarget, error) {
	var servers []models.Server
	if err := o.DB.Where("status NOT IN ?", []string{models.ServerDeleted, models.ServerDeleting}).Order("id").Find(&servers).Error; err != nil {
		return nil, err
	}
	countries := csvSet(p.Countries)
	want := map[uint]bool{}
	for id := range splitIDs(p.InboundIDs) {
		want[id] = true
	}
	usable := func(s *models.Server) string {
		switch {
		case s.Status != models.ServerActive:
			return "server is " + s.Status
		case s.UploadedAt == nil || s.UploadedInbounds == "":
			return "nothing of this server is in vpn-pannel yet"
		}
		return ""
	}
	if p.Scope == models.SmartScopeAll {
		t := SmartTarget{Name: smartName(p, nil), Country: p.Country}
		for i := range servers {
			s := &servers[i]
			if len(countries) > 0 && !countries[strings.ToUpper(s.Country)] {
				continue
			}
			if p.Family != models.FamilyBoth && s.Family() != p.Family {
				continue
			}
			if usable(s) != "" {
				continue
			}
			items, _ := o.smartItems(snap, s, want)
			t.Items = append(t.Items, items...)
		}
		if len(t.Items) == 0 {
			t.Off = "no active server with working configs"
		}
		return []SmartTarget{t}, nil
	}
	var out []SmartTarget
	for i := range servers {
		s := &servers[i]
		if len(countries) > 0 && !countries[strings.ToUpper(s.Country)] {
			continue
		}
		t := SmartTarget{ServerID: s.ID, Name: smartName(p, s), Country: s.Country, Off: usable(s)}
		if t.Off == "" {
			t.Items, _ = o.smartItems(snap, s, want)
			if len(t.Items) == 0 {
				t.Off = "no working config on this server"
			}
		}
		out = append(out, t)
	}
	return out, nil
}

// SmartRulesFor returns the enabled rules a profile lists, in its order.
func (o *Orchestrator) SmartRulesFor(p *models.SmartProfile) []models.SmartRule {
	ids := strings.Split(p.RuleIDs, ",")
	var rows []models.SmartRule
	o.DB.Where("enabled = ?", true).Find(&rows)
	byID := map[string]models.SmartRule{}
	for _, r := range rows {
		byID[fmt.Sprint(r.ID)] = r
	}
	var out []models.SmartRule
	for _, id := range ids {
		if r, ok := byID[strings.TrimSpace(id)]; ok {
			out = append(out, r)
		}
	}
	return out
}

// SmartPreview is one built config, for the dashboard.
type SmartPreview struct {
	ServerID  uint     `json:"server_id"`
	Name      string   `json:"name"`
	Country   string   `json:"country"`
	Outbounds int      `json:"outbounds"`
	Skipped   []string `json:"skipped"`
	Off       string   `json:"off"`
	Error     string   `json:"error"`
	JSON      string   `json:"json"`
}

// PreviewSmart builds a profile's configs without uploading them
// (serverID narrows a per-server profile to one server).
func (o *Orchestrator) PreviewSmart(p *models.SmartProfile, serverID uint) ([]SmartPreview, error) {
	snap, err := LoadSnapshot(o.DB)
	if err != nil {
		return nil, err
	}
	smart.Normalize(p)
	targets, err := o.SmartTargets(snap, p)
	if err != nil {
		return nil, err
	}
	rules := o.SmartRulesFor(p)
	var out []SmartPreview
	for _, t := range targets {
		if serverID != 0 && t.ServerID != serverID {
			continue
		}
		pv := SmartPreview{ServerID: t.ServerID, Name: t.Name, Country: t.Country, Off: t.Off}
		if len(t.Items) > 0 {
			res, err := smart.Build(*p, rules, t.Items, t.Name)
			if res != nil {
				pv.Outbounds, pv.Skipped, pv.JSON = res.Outbounds, res.Skipped, res.JSON
			}
			if err != nil {
				pv.Error = err.Error()
			}
		}
		out = append(out, pv)
	}
	return out, nil
}

// ---- sync ----

type smartKey struct {
	Profile, Server, Dest uint
	Ads                   bool
}

type smartWant struct {
	key       smartKey
	name      string
	json      string
	country   string
	outbounds int
	off       string
	dest      NamedDest
}

func hashOf(parts ...string) string {
	h := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(h[:])
}

func (o *Orchestrator) smartSync(ctx context.Context, r *jobs.Run) error {
	var params SmartSyncParams
	_ = r.Params(&params)
	o.smart.run.Lock()
	defer o.smart.run.Unlock()

	r.Step("build smart configs")
	snap, err := LoadSnapshot(o.DB)
	if err != nil {
		return err
	}
	var profiles []models.SmartProfile
	o.DB.Where("enabled = ?", true).Order("id").Find(&profiles)
	dests := o.Dests()

	var wants []smartWant
	for i := range profiles {
		p := &profiles[i]
		smart.Normalize(p)
		targets, err := o.SmartTargets(snap, p)
		if err != nil {
			return err
		}
		rules := o.SmartRulesFor(p)
		destIDs := csvSet(p.DestinationIDs)
		for _, t := range targets {
			w := smartWant{name: t.Name, country: t.Country, off: t.Off}
			logged := false
			for _, d := range dests {
				if len(destIDs) > 0 && !destIDs[fmt.Sprint(d.ID)] {
					continue
				}
				if _, ok := d.Destination.(SmartDestination); !ok {
					continue
				}
				ads := d.Ads
				if len(ads) == 0 {
					ads = []bool{false}
				}
				for _, a := range ads {
					x := w
					x.key = smartKey{Profile: p.ID, Server: t.ServerID, Dest: d.ID, Ads: a}
					x.name = AdsBaseName(t.Name, a, len(ads) > 1)
					x.dest = d
					if x.off == "" {
						// remarks is the node's name in vpn-pannel, so each
						// name gets its own build.
						res, err := smart.Build(*p, rules, t.Items, x.name)
						if err != nil {
							x.off = err.Error()
						} else {
							x.json, x.outbounds = res.JSON, res.Outbounds
							if !logged {
								logged = true
								for _, s := range res.Skipped {
									r.Logf("warn", "%s: %s", t.Name, s)
								}
							}
						}
					}
					wants = append(wants, x)
				}
			}
		}
	}

	var rows []models.SmartUpload
	o.DB.Find(&rows)
	have := map[smartKey]*models.SmartUpload{}
	for i := range rows {
		x := &rows[i]
		have[smartKey{x.ProfileID, x.ServerID, x.DestinationID, x.Ads}] = x
	}
	destByID := map[uint]NamedDest{}
	for _, d := range dests {
		destByID[d.ID] = d
	}

	r.Step("push smart configs")
	changed, failed := 0, 0
	for _, w := range wants {
		row := have[w.key]
		delete(have, w.key)
		if row == nil {
			row = &models.SmartUpload{ProfileID: w.key.Profile, ServerID: w.key.Server, DestinationID: w.key.Dest, Ads: w.key.Ads}
		}
		did, err := o.pushSmart(ctx, r, w, row, params.Verify)
		if did {
			changed++
		}
		row.Error = ""
		if err != nil {
			failed++
			row.Error = err.Error()
			r.Logf("warn", "%s (%s): %v", w.name, w.dest.Name, err)
		}
		if row.Error == "" && w.off != "" {
			row.Error = w.off
		}
		o.DB.Save(row)
	}

	r.Step("remove smart configs no longer wanted")
	for _, row := range have {
		d, ok := destByID[row.DestinationID]
		if ok && row.Name != "" {
			sd := d.Destination.(SmartDestination)
			n, err := sd.FindByName(ctx, row.Name)
			if err == nil && n.ID != 0 {
				err = sd.DeleteNode(ctx, n.ID)
			}
			if err != nil {
				failed++
				row.Error = err.Error()
				o.DB.Save(row)
				r.Logf("warn", "%s (%s): delete: %v", row.Name, d.Name, err)
				continue
			}
			r.Info("%s: removed %s", d.Name, row.Name)
		}
		o.DB.Delete(row)
	}
	r.Info("smart configs: %d wanted, %d pushed or changed, %d failed", len(wants), changed, failed)
	if failed > 0 && changed == 0 && failed == len(wants) {
		return fmt.Errorf("every smart config push failed")
	}
	return nil
}

// pushSmart brings one node in line with w; it reports whether the panel
// was changed.
func (o *Orchestrator) pushSmart(ctx context.Context, r *jobs.Run, w smartWant, row *models.SmartUpload, verify bool) (bool, error) {
	sd := w.dest.Destination.(SmartDestination)
	if w.off != "" {
		// Kept, but switched off, until the config can be built again.
		row.Outbounds = 0
		if row.Name == "" {
			row.Name = w.name
		}
		if row.PanelNodeID == 0 || (!row.Active && !verify) {
			row.Active = false
			return false, nil
		}
		n, err := sd.FindByName(ctx, row.Name)
		if err != nil {
			return false, err
		}
		row.Active = false
		if n.ID == 0 || !n.Active {
			return false, nil
		}
		r.Info("%s: switched off %s (%s)", w.dest.Name, row.Name, w.off)
		return true, sd.SetNodeActive(ctx, n.ID, false)
	}
	hash := hashOf(w.json, w.country, fmt.Sprint(w.key.Ads))
	if row.Name == w.name && row.Hash == hash && row.Active && !verify {
		return false, nil
	}
	// A renamed config: the node under the old name goes.
	if row.Name != "" && row.Name != w.name {
		if n, err := sd.FindByName(ctx, row.Name); err == nil && n.ID != 0 {
			if err := sd.DeleteNode(ctx, n.ID); err != nil {
				return false, err
			}
		}
	}
	n, err := sd.FindByName(ctx, w.name)
	if err != nil {
		return false, err
	}
	did := false
	if n.ID == 0 || row.Hash != hash || row.Name != w.name {
		id, err := sd.UpsertJSON(ctx, destination.SmartNode{Name: w.name, JSON: w.json, Country: w.country, Ads: w.key.Ads})
		if err != nil {
			return false, err
		}
		if n.ID == 0 {
			n.Active = true // new nodes start active
		}
		n.ID, did = id, true
		now := time.Now()
		row.PushedAt = &now
		r.Info("%s: pushed %s (%d proxies)", w.dest.Name, w.name, w.outbounds)
	}
	if !n.Active {
		if err := sd.SetNodeActive(ctx, n.ID, true); err != nil {
			return did, err
		}
		did = true
	}
	row.Name, row.PanelNodeID, row.Hash, row.Outbounds, row.Active = w.name, n.ID, hash, w.outbounds, true
	return did, nil
}

// SeedSmartRules adds the built-in routing rules that are missing (by
// key); edits to existing ones are kept. Profiles choose which to use.
func SeedSmartRules(db *gorm.DB) error {
	for i, p := range smart.Presets {
		var n int64
		db.Model(&models.SmartRule{}).Where("key = ?", p.Key).Count(&n)
		if n > 0 {
			continue
		}
		r := models.SmartRule{Key: p.Key, Name: p.Name, Builtin: true, Enabled: true, Domains: p.Domains, IPs: p.IPs, Order: (i + 1) * 10}
		if err := db.Create(&r).Error; err != nil {
			return err
		}
	}
	return nil
}
