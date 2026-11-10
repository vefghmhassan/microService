package orchestrator

import (
	"context"
	"strings"
	"testing"
	"time"

	"microserver/internal/models"
)

// addAltInbound gives the template a second uploaded inbound (port 8443),
// tested after the one setup creates (port 443).
func addAltInbound(o *Orchestrator) {
	o.DB.Create(&models.InboundTemplate{Remark: "ALT", Protocol: "vless", Port: 8443, Enabled: true, Upload: true,
		Settings:       `{"clients":[{"id":"11111111-2222-4333-8444-666666666666","email":"u2"}],"decryption":"none"}`,
		StreamSettings: `{"network":"tcp","security":"none"}`})
}

// breakConfigs makes the config test fail for the configs broken picks.
func breakConfigs(o *Orchestrator, broken func(i int) bool) {
	o.Connect = func(context.Context, *models.Server) (Remote, string, error) {
		return &fakeRemote{broken: broken}, "ssh-ed25519 HOSTKEY", nil
	}
}

func provisionOK(t *testing.T, o *Orchestrator, acc *models.ProviderAccount) models.Server {
	t.Helper()
	up := true
	j, _ := o.Engine.Enqueue(KindProvision, nil, ProvisionParams{AccountID: acc.ID, Upload: &up}, "test")
	got := waitJob(t, o, j.ID)
	if got.Status != models.JobDone {
		t.Fatalf("provision: %s", got.Error)
	}
	var s models.Server
	o.DB.First(&s, *got.ServerID)
	return s
}

// waitIdle waits until no job is queued or running.
func waitIdle(t *testing.T, o *Orchestrator) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var n int64
		o.DB.Model(&models.Job{}).Where("status IN ?", []string{models.JobQueued, models.JobRunning}).Count(&n)
		if n == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("jobs never settled")
}

func hasPort(links []string, port string) bool {
	for _, l := range links {
		if strings.Contains(l, ":"+port) {
			return true
		}
	}
	return false
}

func TestPassedInboundsIgnoresVariants(t *testing.T) {
	got := passedInbounds([]ConfigTest{
		{InboundID: 1, OK: true},
		{InboundID: 2, OK: false},
		{InboundID: 2, OK: true, Variant: VariantFragment},
		{InboundID: 3, OK: true},
		{InboundID: 3, OK: false, Variant: VariantClean},
	})
	if len(got) != 2 || !got[1] || !got[3] {
		t.Fatalf("passed %v", got)
	}
}

// Only the configs that work on a new server reach vpn-pannel.
func TestNewServerUploadsOnlyWorkingConfigs(t *testing.T) {
	o, _, _, dest, acc, _ := setup(t)
	addAltInbound(o)
	breakConfigs(o, func(i int) bool { return i == 1 })

	s := provisionOK(t, o, acc)
	if s.Status != models.ServerActive {
		t.Fatalf("server status %s", s.Status)
	}
	if len(dest.uploads) != 1 || !hasPort(dest.uploads[0], "443") || hasPort(dest.uploads[0], "8443") {
		t.Fatalf("uploads %v", dest.uploads)
	}
}

// A new server on which no config works is deleted and built again in the
// same place, and the search stops after two more servers.
func TestNewServerWithNoWorkingConfigIsRebuiltThenGivenUp(t *testing.T) {
	o, prov, _, dest, acc, _ := setup(t)
	breakConfigs(o, func(int) bool { return true })

	up := true
	first, _ := o.Engine.Enqueue(KindProvision, nil, ProvisionParams{AccountID: acc.ID, Name: "mine", Upload: &up}, "test")
	if got := waitJob(t, o, first.ID); got.Status != models.JobFailed || !strings.Contains(got.Error, "replacement 1 of 2") {
		t.Fatalf("first job: %s %q", got.Status, got.Error)
	}
	waitIdle(t, o)

	var jobs []models.Job
	o.DB.Where("kind = ?", KindProvision).Order("id").Find(&jobs)
	if len(jobs) != maxConfigRetries+1 {
		t.Fatalf("%d provision jobs, want %d", len(jobs), maxConfigRetries+1)
	}
	for _, j := range jobs {
		if j.Status != models.JobFailed {
			t.Fatalf("job %d ended %s", j.ID, j.Status)
		}
	}
	if !strings.Contains(jobs[len(jobs)-1].Error, "giving up") {
		t.Fatalf("last job: %q", jobs[len(jobs)-1].Error)
	}
	var servers []models.Server
	o.DB.Order("id").Find(&servers)
	if len(servers) != maxConfigRetries+1 {
		t.Fatalf("%d servers", len(servers))
	}
	for _, s := range servers {
		if s.Status != models.ServerDeleted || s.Region != servers[0].Region || s.Plan != servers[0].Plan {
			t.Fatalf("server %s: status %s at %s/%s", s.Name, s.Status, s.Region, s.Plan)
		}
	}
	if len(dest.uploads) != 0 {
		t.Fatalf("uploads %v", dest.uploads)
	}
	prov.mu.Lock()
	left := len(prov.servers)
	prov.mu.Unlock()
	if left != 0 {
		t.Fatalf("%d servers left at the provider", left)
	}
}

// A reinstall never deletes its server, whatever the test says.
func TestReinstallKeepsServerWithNoWorkingConfig(t *testing.T) {
	o, _, _, _, acc, _ := setup(t)
	s := provisionOK(t, o, acc)
	breakConfigs(o, func(int) bool { return true })

	j, _ := o.Engine.Enqueue(KindInstall, &s.ID, InstallParams{}, "test")
	if got := waitJob(t, o, j.ID); got.Status != models.JobDone {
		t.Fatalf("install: %s %s", got.Status, got.Error)
	}
	waitIdle(t, o)
	o.DB.First(&s, s.ID)
	if s.Status != models.ServerActive {
		t.Fatalf("server status %s", s.Status)
	}
}

// Replacing a server with one where only some configs work: the working ones
// are uploaded and the old address's configs removed, instead of moving all.
func TestReplacementKeepsOnlyWorkingConfigs(t *testing.T) {
	o, _, _, dest, acc, _ := setup(t)
	addAltInbound(o)
	old := provisionOK(t, o, acc)
	repl := provisionOK(t, o, acc)
	before := len(dest.uploads)
	breakConfigs(o, func(i int) bool { return i == 1 })

	up := true
	j, _ := o.Engine.Enqueue(KindInstall, &repl.ID, InstallParams{ReplaceServerID: old.ID, Upload: &up, Fresh: true}, "test")
	if got := waitJob(t, o, j.ID); got.Status != models.JobDone {
		t.Fatalf("install: %s %s", got.Status, got.Error)
	}
	waitIdle(t, o)

	if len(dest.replaced) != 0 {
		t.Fatalf("every config was moved: %v", dest.replaced)
	}
	if len(dest.uploads) != before+1 {
		t.Fatalf("%d uploads, want %d", len(dest.uploads), before+1)
	}
	if last := dest.uploads[len(dest.uploads)-1]; !hasPort(last, "443") || hasPort(last, "8443") {
		t.Fatalf("uploaded %v", last)
	}
	if len(dest.removed) != 1 || dest.removed[0] != old.UploadedIP {
		t.Fatalf("removed %v, want %s", dest.removed, old.UploadedIP)
	}
	o.DB.First(&old, old.ID)
	o.DB.First(&repl, repl.ID)
	if old.Status != models.ServerDeleted || repl.Status != models.ServerActive {
		t.Fatalf("old %s, replacement %s", old.Status, repl.Status)
	}
}

// A replacement on which nothing works is deleted and another is queued; the
// server being replaced and its configs are left alone.
func TestReplacementWithNoWorkingConfigLeavesOldServer(t *testing.T) {
	o, _, _, dest, acc, _ := setup(t)
	old := provisionOK(t, o, acc)
	repl := provisionOK(t, o, acc)
	now := time.Now()
	o.DB.Model(&old).Update("replacing_at", now)
	breakConfigs(o, func(int) bool { return true })

	run := func(attempt int) models.Job {
		j, _ := o.Engine.Enqueue(KindInstall, &repl.ID, InstallParams{ReplaceServerID: old.ID, Fresh: true, Attempt: attempt}, "test")
		got := waitJob(t, o, j.ID)
		if got.Status != models.JobFailed {
			t.Fatalf("install: %s %s", got.Status, got.Error)
		}
		waitIdle(t, o)
		return got
	}

	run(0)
	var next []models.Job
	o.DB.Where("kind = ?", KindProvisionAuto).Find(&next)
	if len(next) != 1 || !strings.Contains(next[0].Params, `"attempt":1`) || !strings.Contains(next[0].Params, `"replace_server_id":`) {
		t.Fatalf("next jobs %+v", next)
	}
	o.DB.First(&repl, repl.ID)
	o.DB.First(&old, old.ID)
	if repl.Status != models.ServerDeleted || old.Status != models.ServerActive {
		t.Fatalf("replacement %s, old %s", repl.Status, old.Status)
	}
	if len(dest.replaced) != 0 || len(dest.removed) != 0 {
		t.Fatalf("vpn-pannel was touched: replaced %v removed %v", dest.replaced, dest.removed)
	}

	// The last allowed attempt queues nothing more.
	repl = provisionOK2(t, o, acc)
	o.DB.Model(&old).Update("replacing_at", now)
	if got := run(maxConfigRetries); !strings.Contains(got.Error, "giving up") {
		t.Fatalf("error %q", got.Error)
	}
	var n int64
	o.DB.Model(&models.Job{}).Where("kind = ?", KindProvisionAuto).Count(&n)
	var after models.Server
	o.DB.First(&after, old.ID)
	if n != 1 || after.ReplacingAt != nil || after.Status != models.ServerActive {
		t.Fatalf("auto jobs %d, replacing_at %v, old %s", n, after.ReplacingAt, after.Status)
	}
}

// provisionOK2 builds one more working server while configs are set to fail.
func provisionOK2(t *testing.T, o *Orchestrator, acc *models.ProviderAccount) models.Server {
	t.Helper()
	breakConfigs(o, nil)
	s := provisionOK(t, o, acc)
	breakConfigs(o, func(int) bool { return true })
	return s
}
