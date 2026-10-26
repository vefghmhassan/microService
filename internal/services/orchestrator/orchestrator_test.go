package orchestrator

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"microserver/internal/database"
	"microserver/internal/jobs"
	"microserver/internal/models"
	"microserver/internal/services/checker"
	"microserver/internal/services/destination"
	"microserver/internal/services/providers"
	"microserver/internal/services/secret"
	"microserver/internal/services/settings"
	"microserver/internal/services/sshx"
	"microserver/internal/services/xui"
)

// ---- fakes ----

type fakeProvider struct {
	mu        sync.Mutex
	nextIP    int
	rotates   int
	honorKeep bool
	released  []string
	servers   map[string]*providers.Instance
	deleted   []string
	// capacity limits how many live servers a region holds (0 = unlimited).
	capacity map[string]int
}

func (f *fakeProvider) ip() string { f.nextIP++; return fmt.Sprintf("10.0.0.%d", f.nextIP) }

func (f *fakeProvider) Type() string { return "fake" }
func (f *fakeProvider) Capabilities() providers.Capabilities {
	return providers.Capabilities{RotateMethod: providers.RotatePrimaryIPSwap, CanKeepOldIP: true}
}
func (f *fakeProvider) Validate(context.Context) error                            { return nil }
func (f *fakeProvider) Regions(context.Context) ([]providers.Option, error)       { return nil, nil }
func (f *fakeProvider) Plans(context.Context, string) ([]providers.Option, error) { return nil, nil }
func (f *fakeProvider) Create(_ context.Context, req providers.CreateRequest) (*providers.Instance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !strings.Contains(req.UserData, "ssh-ed25519") {
		return nil, fmt.Errorf("cloud-init lacks our key")
	}
	if f.servers == nil {
		f.servers = map[string]*providers.Instance{}
	}
	if c := f.capacity[req.Region]; c > 0 {
		live := 0
		for _, in := range f.servers {
			if in.Region == req.Region {
				live++
			}
		}
		if live >= c {
			return nil, &providers.APIError{Status: 403, Body: `{"error":{"code":"resource_limit_exceeded"}}`}
		}
	}
	ip := f.ip()
	in := &providers.Instance{Ref: "srv-" + ip, Name: req.Name, Status: "running", IPv4: ip, IPRef: "ref-" + ip, Region: req.Region}
	f.servers[in.Ref] = in
	cp := *in
	return &cp, nil
}
func (f *fakeProvider) Get(_ context.Context, ref string) (*providers.Instance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	in, ok := f.servers[ref]
	if !ok {
		return nil, providers.ErrNotFound
	}
	cp := *in
	return &cp, nil
}
func (f *fakeProvider) Delete(_ context.Context, ref string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.servers, ref)
	f.deleted = append(f.deleted, ref)
	return nil
}
func (f *fakeProvider) RotateIP(_ context.Context, inst *providers.Instance, opts providers.RotateOptions) (*providers.RotateResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rotates++
	ip := f.ip()
	if in, ok := f.servers[inst.Ref]; ok {
		in.IPv4, in.IPRef = ip, "ref-"+ip
	}
	return &providers.RotateResult{
		Instance: &providers.Instance{Ref: inst.Ref, Status: "running", IPv4: ip, IPRef: "ref-" + ip, Region: inst.Region},
		OldIP:    inst.IPv4, OldIPRef: inst.IPRef, OldKept: f.honorKeep && opts.KeepOld,
	}, nil
}
func (f *fakeProvider) ReleaseIP(_ context.Context, ref string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.released = append(f.released, ref)
	return nil
}

type fakeRemote struct{ cmds []string }

func (r *fakeRemote) Run(_ context.Context, cmd string, _ func(string)) (string, error) {
	r.cmds = append(r.cmds, cmd)
	return "", nil
}
func (r *fakeRemote) ReadFile(_ context.Context, path string) (string, error) {
	if path != installResultPath {
		return "", fmt.Errorf("unexpected read %s", path)
	}
	return "XUI_USERNAME=u\nXUI_PASSWORD='p'\nXUI_PANEL_PORT=31000\nXUI_WEB_BASE_PATH=/bp/\nXUI_API_TOKEN=tok123\n", nil
}

// DialContext succeeds like a server where xray is listening.
func (r *fakeRemote) DialContext(context.Context, string, string) (net.Conn, error) {
	c, _ := net.Pipe()
	return c, nil
}
func (r *fakeRemote) Close() error { return nil }

type fakePanel struct {
	online   []string
	subOff   bool
	mu       sync.Mutex
	inbounds []xui.Inbound
	setting  map[string]any
	restarts int
}

func (p *fakePanel) Onlines(context.Context) ([]string, error) { return p.online, nil }
func (p *fakePanel) DisableSubscription(context.Context) (bool, error) {
	p.subOff = true
	return true, nil
}
func (p *fakePanel) RestartPanel(context.Context) error { return nil }
func (p *fakePanel) ListInbounds(context.Context) ([]xui.Inbound, error) {
	return append([]xui.Inbound{}, p.inbounds...), nil
}
func (p *fakePanel) AddInbound(_ context.Context, in xui.Inbound) error {
	in.ID = len(p.inbounds) + 1
	p.inbounds = append(p.inbounds, in)
	return nil
}
func (p *fakePanel) UpdateInbound(_ context.Context, id int, in xui.Inbound) error {
	for i := range p.inbounds {
		if p.inbounds[i].ID == id {
			in.ID = id
			p.inbounds[i] = in
		}
	}
	return nil
}
func (p *fakePanel) DeleteInbound(_ context.Context, id int) error {
	out := p.inbounds[:0]
	for _, in := range p.inbounds {
		if in.ID != id {
			out = append(out, in)
		}
	}
	p.inbounds = out
	return nil
}
func (p *fakePanel) XraySetting(context.Context) (map[string]any, error) {
	return map[string]any{"api": map[string]any{"tag": "api"}, "outbounds": []any{}}, nil
}
func (p *fakePanel) UpdateXraySetting(_ context.Context, s map[string]any) error {
	p.setting = s
	return nil
}
func (p *fakePanel) RestartXray(context.Context) error              { p.restarts++; return nil }
func (p *fakePanel) XrayVersions(context.Context) ([]string, error) { return []string{"v25.1.1"}, nil }
func (p *fakePanel) InstallXray(context.Context, string) error      { return nil }
func (p *fakePanel) Status(context.Context) (*xui.Status, error) {
	s := &xui.Status{}
	s.Xray.Version = "25.1.1"
	return s, nil
}

type fakeDest struct {
	mu       sync.Mutex
	uploads  [][]string
	replaced [][2]string
}

func (d *fakeDest) Upload(_ context.Context, in destination.UploadRequest) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.uploads = append(d.uploads, in.Links)
	return nil
}
func (d *fakeDest) ReplaceAddress(_ context.Context, oldIP, newIP, _ string) (*destination.ReplaceResult, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.replaced = append(d.replaced, [2]string{oldIP, newIP})
	return &destination.ReplaceResult{Updated: 1}, nil
}
func (d *fakeDest) SetActive(context.Context, string, bool) error { return nil }

// ---- harness ----

func setup(t *testing.T) (*Orchestrator, *fakeProvider, *fakePanel, *fakeDest, *models.ProviderAccount, map[string]bool) {
	t.Helper()
	if err := secret.Init(make([]byte, 32)); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(db); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			sqlDB.Close()
		}
	})
	st, err := settings.New(db)
	if err != nil {
		t.Fatal(err)
	}
	key, err := sshx.LoadOrCreateKey(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	engine := jobs.NewEngine(db, 2)
	o := New(db, st, key, engine)
	o.RetryDelay = time.Millisecond
	prov, panel, dest := &fakeProvider{}, &fakePanel{}, &fakeDest{}
	filtered := map[string]bool{}
	o.Provider = func(*models.ProviderAccount) (providers.Provider, error) { return prov, nil }
	o.Connect = func(context.Context, *models.Server) (Remote, string, error) {
		return &fakeRemote{}, "ssh-ed25519 HOSTKEY", nil
	}
	o.PanelFor = func(*models.Server, Remote) Panel { return panel }
	var mu sync.Mutex
	o.Check = func(_ context.Context, host string, port int) *checker.Result {
		mu.Lock()
		defer mu.Unlock()
		res := &checker.Result{Host: host, Port: port}
		ok := !filtered[host]
		res.Probes = []checker.Probe{
			{Source: "t", Country: "IR", ASN: "AS1", OK: ok},
			{Source: "t", Country: "IR", ASN: "AS2", OK: ok},
			{Source: "local", OK: true},
		}
		checker.Decide(res, checker.Policy{MinOK: 2, MinASN: 2})
		return res
	}
	o.Dests = func() []NamedDest { return []NamedDest{{Name: "test", Destination: dest}} }
	o.Register()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := engine.Start(ctx); err != nil {
		t.Fatal(err)
	}

	acc := &models.ProviderAccount{Name: "fake", Type: "fake", Enabled: true, DefaultRegion: "fsn1", DefaultPlan: "cx22", Country: "DE"}
	db.Create(acc)
	db.Create(&models.InboundTemplate{Remark: "DE", Protocol: "vless", Port: 443, Enabled: true, Upload: true,
		Settings:       `{"clients":[{"id":"11111111-2222-4333-8444-555555555555","email":"u1"}],"decryption":"none"}`,
		StreamSettings: `{"network":"tcp","security":"none"}`})
	db.Create(&models.XrayTemplate{ID: 1, Outbounds: `[{"protocol":"freedom","tag":"direct"}]`, Routing: `{"rules":[]}`})
	return o, prov, panel, dest, acc, filtered
}

func waitJob(t *testing.T, o *Orchestrator, id uint) models.Job {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var j models.Job
		o.DB.First(&j, id)
		if j.Status == models.JobDone || j.Status == models.JobFailed || j.Status == models.JobCanceled {
			if j.Status != models.JobDone {
				var logs []models.JobLog
				o.DB.Where("job_id = ?", id).Find(&logs)
				for _, l := range logs {
					t.Logf("[%s] %s", l.Level, l.Message)
				}
			}
			return j
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("job %d did not finish", id)
	return models.Job{}
}

func TestProvisionRotatesAwayFromFilteredIPThenInstallsAndUploads(t *testing.T) {
	o, prov, panel, dest, acc, filtered := setup(t)
	filtered["10.0.0.1"] = true // the first IP the provider hands out is blocked

	j, err := o.Engine.Enqueue(KindProvision, nil, ProvisionParams{AccountID: acc.ID}, "test")
	if err != nil {
		t.Fatal(err)
	}
	if got := waitJob(t, o, j.ID); got.Status != models.JobDone {
		t.Fatalf("provision %s: %s", got.Status, got.Error)
	}

	var s models.Server
	o.DB.First(&s)
	if s.Status != models.ServerActive || s.IPv4 != "10.0.0.2" || prov.rotates != 1 {
		t.Fatalf("server %+v rotates=%d", s, prov.rotates)
	}
	if s.XUIPort != 31000 || s.XUIBasePath != "bp" || string(s.XUIToken) != "tok123" || s.SSHHostKey == "" {
		t.Fatalf("3x-ui credentials not stored: %+v", s)
	}
	if s.SyncStatus != models.SyncSynced || s.AppliedHash == "" {
		t.Fatalf("template not applied: %s %s", s.SyncStatus, s.AppliedHash)
	}
	if !panel.subOff {
		t.Fatal("subscription was not turned off")
	}
	if len(panel.inbounds) != 1 || panel.inbounds[0].Remark != ManagedPrefix+"DE" || panel.restarts == 0 {
		t.Fatalf("panel %+v", panel.inbounds)
	}
	if _, ok := panel.setting["api"]; !ok || panel.setting["routing"] == nil {
		t.Fatalf("xray setting not merged: %v", panel.setting)
	}
	if len(dest.uploads) != 1 || !strings.Contains(dest.uploads[0][0], "@10.0.0.2:443") {
		t.Fatalf("uploads %v", dest.uploads)
	}

	var ips []models.IPAddress
	o.DB.Order("id").Find(&ips)
	if len(ips) != 2 || ips[0].Status != models.IPReleased || ips[0].FilteredAt == nil || ips[1].Status != models.IPInUse {
		t.Fatalf("ip pool %+v", ips)
	}

	// Rotate on demand: vpn-pannel must be moved from the uploaded IP to the new one.
	j2, _ := o.Engine.Enqueue(KindRotate, &s.ID, RotateParams{Reason: "test"}, "test")
	if got := waitJob(t, o, j2.ID); got.Status != models.JobDone {
		t.Fatalf("rotate %s: %s", got.Status, got.Error)
	}
	o.DB.First(&s, s.ID)
	if s.IPv4 != "10.0.0.3" || len(dest.replaced) != 1 || dest.replaced[0] != [2]string{"10.0.0.2", "10.0.0.3"} || s.UploadedIP != "10.0.0.3" {
		t.Fatalf("after rotate: ip=%s replaced=%v uploaded=%s", s.IPv4, dest.replaced, s.UploadedIP)
	}
}

func TestTemplateChangeMarksDriftAndSyncRemovesStaleInbounds(t *testing.T) {
	o, _, panel, _, acc, _ := setup(t)
	j, _ := o.Engine.Enqueue(KindProvision, nil, ProvisionParams{AccountID: acc.ID}, "test")
	if got := waitJob(t, o, j.ID); got.Status != models.JobDone {
		t.Fatalf("provision: %s", got.Error)
	}
	// Replace the 443 inbound by one on 8443.
	o.DB.Where("port = ?", 443).Delete(&models.InboundTemplate{})
	o.DB.Create(&models.InboundTemplate{Remark: "NEW", Protocol: "trojan", Port: 8443, Enabled: true,
		Settings: `{"clients":[{"password":"x","email":"e"}]}`})
	// A hand-made inbound must survive the sync.
	panel.inbounds = append(panel.inbounds, xui.Inbound{ID: 99, Remark: "manual", Port: 1080, Protocol: "socks"})

	if _, err := BumpTemplate(o.DB); err != nil {
		t.Fatal(err)
	}
	var s models.Server
	o.DB.First(&s)
	if s.SyncStatus != models.SyncDrift {
		t.Fatalf("sync status %s", s.SyncStatus)
	}
	j2, _ := o.Engine.Enqueue(KindSync, &s.ID, struct{}{}, "test")
	if got := waitJob(t, o, j2.ID); got.Status != models.JobDone {
		t.Fatalf("sync: %s", got.Error)
	}
	ports := map[int]bool{}
	for _, in := range panel.inbounds {
		ports[in.Port] = true
	}
	if ports[443] || !ports[8443] || !ports[1080] {
		t.Fatalf("inbounds after sync: %+v", panel.inbounds)
	}
	o.DB.First(&s, s.ID)
	if s.SyncStatus != models.SyncSynced {
		t.Fatalf("sync status %s", s.SyncStatus)
	}
}

func TestInstallScriptIsNonInteractive(t *testing.T) {
	s := InstallScript("https://x/install.sh", "v3.7.0", "u", "p'q", 31000, "bp")
	for _, want := range []string{"XUI_NONINTERACTIVE=1", "XUI_PANEL_PORT=31000", `'p'\''q'`, "'v3.7.0' </dev/null"} {
		if !strings.Contains(s, want) {
			t.Errorf("script lacks %q:\n%s", want, s)
		}
	}
}

func TestFilteredIPsAreHeldDuringTheJobAndReleasedAfter(t *testing.T) {
	o, prov, _, _, acc, filtered := setup(t)
	prov.honorKeep = true
	filtered["10.0.0.1"] = true
	filtered["10.0.0.2"] = true

	j, _ := o.Engine.Enqueue(KindProvision, nil, ProvisionParams{AccountID: acc.ID}, "test")
	if got := waitJob(t, o, j.ID); got.Status != models.JobDone {
		t.Fatalf("provision: %s", got.Error)
	}
	var s models.Server
	o.DB.First(&s)
	if s.IPv4 != "10.0.0.3" {
		t.Fatalf("ip %s", s.IPv4)
	}
	// Both blocked IPs were kept while rotating (so they could not come
	// back) and released once a healthy one was found.
	if len(prov.released) != 2 || prov.released[0] != "ref-10.0.0.1" || prov.released[1] != "ref-10.0.0.2" {
		t.Fatalf("released %v", prov.released)
	}
	var n int64
	o.DB.Model(&models.IPAddress{}).Where("status = ? AND filtered_at IS NOT NULL", models.IPReleased).Count(&n)
	if n != 2 {
		t.Fatalf("pool should show 2 filtered+released IPs, got %d", n)
	}
}

func TestAutoPlacementTriesWholeLocationThenFallsBack(t *testing.T) {
	o, prov, panel, dest, acc, filtered := setup(t)
	prov.capacity = map[string]int{"sin": 3}
	// Singapore: every IP filtered. Helsinki: the second one is clean.
	for _, ip := range []string{"10.0.0.1", "10.0.0.2", "10.0.0.3", "10.0.0.4"} {
		filtered[ip] = true
	}
	o.DB.Create(&models.Location{Priority: 1, ProviderAccountID: acc.ID, Region: "sin", Plan: "p", Enabled: true})
	o.DB.Create(&models.Location{Priority: 2, ProviderAccountID: acc.ID, Region: "hel1", Plan: "p", Capacity: 2, Enabled: true})

	j, _ := o.Engine.Enqueue(KindProvisionAuto, nil, AutoParams{Count: 1}, "test")
	if got := waitJob(t, o, j.ID); got.Status != models.JobDone {
		t.Fatalf("auto: %s", got.Error)
	}
	// Singapore was filled to its limit (3) before anything was deleted,
	// then Helsinki got two candidates at once.
	if len(prov.deleted) != 4 {
		t.Fatalf("deleted %v", prov.deleted)
	}
	var kept []models.Server
	o.DB.Where("status <> ?", models.ServerDeleted).Find(&kept)
	if len(kept) != 1 || kept[0].Region != "hel1" || kept[0].IPv4 != "10.0.0.5" {
		t.Fatalf("kept %+v", kept)
	}
	// The kept one is installed and uploaded by its own job.
	var inst models.Job
	o.DB.Where("kind = ?", KindInstall).First(&inst)
	if got := waitJob(t, o, inst.ID); got.Status != models.JobDone {
		t.Fatalf("install: %s", got.Error)
	}
	if len(panel.inbounds) == 0 || len(dest.uploads) != 1 {
		t.Fatalf("inbounds=%d uploads=%d", len(panel.inbounds), len(dest.uploads))
	}
	var filteredIPs int64
	o.DB.Model(&models.IPAddress{}).Where("filtered_at IS NOT NULL AND status = ?", models.IPReleased).Count(&filteredIPs)
	if filteredIPs != 4 {
		t.Fatalf("filtered+released IPs = %d", filteredIPs)
	}
}

func TestRotateFallsBackToNextLocationAndHandsOverConfigs(t *testing.T) {
	o, _, _, dest, acc, filtered := setup(t)
	o.Settings.Set(map[string]string{settings.RotateMaxAttempts: "2"})
	o.DB.Create(&models.Location{Priority: 1, ProviderAccountID: acc.ID, Region: "fsn1", Plan: "cx22", Enabled: true})
	o.DB.Create(&models.Location{Priority: 2, ProviderAccountID: acc.ID, Region: "hel1", Plan: "cx22", Capacity: 1, Enabled: true})

	j, _ := o.Engine.Enqueue(KindProvision, nil, ProvisionParams{AccountID: acc.ID}, "test")
	if got := waitJob(t, o, j.ID); got.Status != models.JobDone {
		t.Fatalf("provision: %s", got.Error)
	}
	var old models.Server
	o.DB.First(&old)
	// Every IP fsn1 can give from now on is filtered.
	for i := 2; i <= 3; i++ {
		filtered[fmt.Sprintf("10.0.0.%d", i)] = true
	}
	j2, _ := o.Engine.Enqueue(KindRotate, &old.ID, RotateParams{}, "test")
	if got := waitJob(t, o, j2.ID); got.Status != models.JobDone {
		t.Fatalf("rotate: %s", got.Error)
	}
	var auto, inst, del models.Job
	o.DB.Where("kind = ?", KindProvisionAuto).First(&auto)
	if got := waitJob(t, o, auto.ID); got.Status != models.JobDone {
		t.Fatalf("auto: %s", got.Error)
	}
	o.DB.Where("kind = ?", KindInstall).First(&inst)
	if got := waitJob(t, o, inst.ID); got.Status != models.JobDone {
		t.Fatalf("install: %s", got.Error)
	}
	o.DB.Where("kind = ? AND server_id = ?", KindDelete, old.ID).First(&del)
	if got := waitJob(t, o, del.ID); got.Status != models.JobDone {
		t.Fatalf("delete old: %s", got.Error)
	}
	var repl models.Server
	o.DB.Where("region = ? AND status = ?", "hel1", models.ServerActive).First(&repl)
	if repl.ID == 0 {
		t.Fatal("no replacement in hel1")
	}
	// vpn-pannel configs moved from the old server's uploaded IP to the new one.
	last := dest.replaced[len(dest.replaced)-1]
	if last[0] != "10.0.0.1" || last[1] != repl.IPv4 {
		t.Fatalf("replaced %v, new ip %s", dest.replaced, repl.IPv4)
	}
	o.DB.First(&old, old.ID)
	if old.Status != models.ServerDeleted {
		t.Fatalf("old server %s", old.Status)
	}
}

func TestTrafficCapReplacesServerOnce(t *testing.T) {
	o, _, panel, dest, acc, _ := setup(t)
	o.Settings.Set(map[string]string{settings.TrafficLimitGB: "390", settings.TrafficAuto: "true"})
	j, _ := o.Engine.Enqueue(KindProvision, nil, ProvisionParams{AccountID: acc.ID}, "test")
	if got := waitJob(t, o, j.ID); got.Status != models.JobDone {
		t.Fatalf("provision: %s", got.Error)
	}
	var old models.Server
	o.DB.First(&old)

	// 200 GB up + 191 GB down = 391 GB > 390 GB; one client online.
	panel.mu.Lock()
	panel.inbounds[0].Up, panel.inbounds[0].Down = 200*gb, 191*gb
	panel.inbounds[0].ClientStats = []xui.ClientStat{{Email: "u1"}, {Email: "u2"}}
	panel.online = []string{"u1"}
	panel.mu.Unlock()

	o.CollectAll(context.Background())
	o.CollectAll(context.Background()) // must not queue a second replacement

	o.DB.First(&old, old.ID)
	if old.TrafficUp+old.TrafficDown != 391*gb || old.OnlineCount != 1 || old.ReplacingAt == nil {
		t.Fatalf("stats up=%d down=%d online=%d replacing=%v", old.TrafficUp, old.TrafficDown, old.OnlineCount, old.ReplacingAt)
	}
	var st models.InboundStat
	o.DB.Where("server_id = ?", old.ID).First(&st)
	if st.Online != 1 || st.Clients != 2 || st.Remark != "DE" {
		t.Fatalf("inbound stat %+v", st)
	}
	var autos []models.Job
	o.DB.Where("kind = ?", KindProvisionAuto).Find(&autos)
	if len(autos) != 1 {
		t.Fatalf("%d replacement jobs", len(autos))
	}
	// No location list: the replacement is built in the old server's own location.
	if got := waitJob(t, o, autos[0].ID); got.Status != models.JobDone {
		t.Fatalf("auto: %s", got.Error)
	}
	var inst models.Job
	o.DB.Where("kind = ?", KindInstall).First(&inst)
	if got := waitJob(t, o, inst.ID); got.Status != models.JobDone {
		t.Fatalf("install: %s", got.Error)
	}
	var repl models.Server
	o.DB.Where("id <> ? AND status = ?", old.ID, models.ServerActive).First(&repl)
	if repl.ID == 0 || repl.Region != old.Region {
		t.Fatalf("replacement %+v", repl)
	}
	last := dest.replaced[len(dest.replaced)-1]
	if last[0] != old.UploadedIP || last[1] != repl.IPv4 {
		t.Fatalf("replaced %v", dest.replaced)
	}
}
