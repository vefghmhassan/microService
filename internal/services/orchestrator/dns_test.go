package orchestrator

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"microserver/internal/models"
	"microserver/internal/services/cloudflare"
	"microserver/internal/services/secret"
)

// fakeCF keeps A records by name, one address each.
type fakeCF struct {
	mu      sync.Mutex
	records map[string]string // name → ip
	proxied map[string]bool
	upserts []string
}

func (f *fakeCF) UpsertAddr(_ context.Context, _, name, ip string, proxied bool) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.records[name] = ip
	if f.proxied == nil {
		f.proxied = map[string]bool{}
	}
	f.proxied[name] = proxied
	f.upserts = append(f.upserts, name+"="+ip)
	return "rec-" + name, nil
}
func (f *fakeCF) Records(_ context.Context, _, typ, name string) ([]cloudflare.Record, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if ip, ok := f.records[name]; ok && cloudflare.AddrType(ip) == typ {
		return []cloudflare.Record{{ID: "rec-" + name, Name: name, Content: ip, Type: typ}}, nil
	}
	return nil, nil
}
func (f *fakeCF) DeleteRecord(_ context.Context, _, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.records, strings.TrimPrefix(id, "rec-"))
	return nil
}
func (f *fakeCF) get(name string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ip, ok := f.records[name]
	return ip, ok
}

type fakeCerts struct{ calls int }

func (f *fakeCerts) Ensure(_ context.Context, accountID uint, zoneID, zoneName string, _ func(string, ...any)) (*models.Certificate, bool, error) {
	f.calls++
	na := time.Now().Add(80 * 24 * time.Hour)
	return &models.Certificate{ZoneName: zoneName, CertPEM: "CERT", KeyPEM: secret.String("KEY"), NotAfter: &na}, false, nil
}

func setupDNS(t *testing.T) (*Orchestrator, *fakeCF, *fakeCerts, *fakeDest, *models.ProviderAccount, map[string]bool) {
	o, _, _, dest, acc, filtered := setup(t)
	cf := &fakeCF{records: map[string]string{}}
	certs := &fakeCerts{}
	o.CF = func(*models.CloudflareAccount) DNSAPI { return cf }
	o.Certs = certs
	cfAcc := &models.CloudflareAccount{Name: "cf", Token: "t", Enabled: true}
	o.DB.Create(cfAcc)
	o.DB.Create(&models.Location{Priority: 1, ProviderAccountID: acc.ID, Region: "fsn1", Plan: "p", Capacity: 1, Enabled: true,
		DNS: models.DNS{CFAccountID: cfAcc.ID, CFZoneID: "z1", CFZoneName: "example.com", DNSPattern: "fr{n}", SSL: true}})
	return o, cf, certs, dest, acc, filtered
}

// runAuto builds count servers through placement and waits for their installs.
func runAuto(t *testing.T, o *Orchestrator, p AutoParams) {
	t.Helper()
	var before int64
	o.DB.Model(&models.Job{}).Where("kind = ?", KindInstall).Count(&before)
	j, _ := o.Engine.Enqueue(KindProvisionAuto, nil, p, "test")
	if got := waitJob(t, o, j.ID); got.Status != models.JobDone {
		t.Fatalf("auto: %s", got.Error)
	}
	var insts []models.Job
	o.DB.Where("kind = ?", KindInstall).Order("id").Offset(int(before)).Find(&insts)
	for _, in := range insts {
		if got := waitJob(t, o, in.ID); got.Status != models.JobDone {
			t.Fatalf("install: %s", got.Error)
		}
	}
}

func TestDNSNumbersServersAndUsesHostnameInLinks(t *testing.T) {
	o, cf, certs, dest, _, _ := setupDNS(t)
	runAuto(t, o, AutoParams{Count: 2})

	var ss []models.Server
	o.DB.Where("status = ?", models.ServerActive).Order("id").Find(&ss)
	if len(ss) != 2 || ss[0].Hostname != "fr1.example.com" || ss[1].Hostname != "fr2.example.com" {
		t.Fatalf("hostnames %+v", ss)
	}
	for _, s := range ss {
		if ip, _ := cf.get(s.Hostname); ip != s.IPv4 || s.DNSRecordID == "" {
			t.Fatalf("record %s → %q, server ip %s", s.Hostname, ip, s.IPv4)
		}
		if s.UploadedIP != s.IPv4 || s.CertNotAfter == nil || s.XUIScheme != "https" {
			t.Fatalf("server %s uploaded=%s cert=%v scheme=%s", s.Name, s.UploadedIP, s.CertNotAfter, s.XUIScheme)
		}
	}
	if certs.calls == 0 {
		t.Fatal("certificate never requested")
	}
	if len(dest.uploads) != 2 || !strings.Contains(dest.uploads[0][0], "@"+ss[0].IPv4+":443") {
		t.Fatalf("uploads %v", dest.uploads)
	}
}

func TestDNSReplacementTakesOverTheRecord(t *testing.T) {
	o, cf, _, dest, _, _ := setupDNS(t)
	runAuto(t, o, AutoParams{Count: 1})
	var old models.Server
	o.DB.First(&old, "status = ?", models.ServerActive)

	if _, err := o.QueueReplace(&old, "test", "test"); err != nil {
		t.Fatal(err)
	}
	var auto models.Job
	o.DB.Where("kind = ?", KindProvisionAuto).Order("id desc").First(&auto)
	if got := waitJob(t, o, auto.ID); got.Status != models.JobDone {
		t.Fatalf("auto: %s", got.Error)
	}
	var inst models.Job
	o.DB.Where("kind = ?", KindInstall).Order("id desc").First(&inst)
	if got := waitJob(t, o, inst.ID); got.Status != models.JobDone {
		t.Fatalf("install: %s", got.Error)
	}
	var del models.Job
	o.DB.Where("kind = ? AND server_id = ?", KindDelete, old.ID).First(&del)
	if got := waitJob(t, o, del.ID); got.Status != models.JobDone {
		t.Fatalf("delete: %s", got.Error)
	}

	var repl models.Server
	o.DB.Where("id <> ? AND status = ?", old.ID, models.ServerActive).First(&repl)
	if repl.Hostname != "fr1.example.com" {
		t.Fatalf("replacement hostname %q", repl.Hostname)
	}
	// The record moved to the new IP and survived the old server's deletion.
	if ip, ok := cf.get("fr1.example.com"); !ok || ip != repl.IPv4 {
		t.Fatalf("record → %q (%t), want %s", ip, ok, repl.IPv4)
	}
	// Links point at the IP, so vpn-pannel's configs move to the new one.
	if len(dest.replaced) != 1 || dest.replaced[0][0] != old.IPv4 || dest.replaced[0][1] != repl.IPv4 ||
		len(dest.uploads) != 1 || repl.UploadedIP != repl.IPv4 {
		t.Fatalf("replaced=%v uploads=%d uploaded=%s", dest.replaced, len(dest.uploads), repl.UploadedIP)
	}
}

func TestDNSRotationMovesRecordAndDeleteRemovesIt(t *testing.T) {
	o, cf, _, dest, _, _ := setupDNS(t)
	runAuto(t, o, AutoParams{Count: 1})
	var s models.Server
	o.DB.First(&s, "status = ?", models.ServerActive)
	oldIP := s.IPv4

	j, _ := o.Engine.Enqueue(KindRotate, &s.ID, RotateParams{}, "test")
	if got := waitJob(t, o, j.ID); got.Status != models.JobDone {
		t.Fatalf("rotate: %s", got.Error)
	}
	o.DB.First(&s, s.ID)
	if s.IPv4 == oldIP {
		t.Fatal("ip did not change")
	}
	if ip, _ := cf.get(s.Hostname); ip != s.IPv4 {
		t.Fatalf("record → %s, want %s", ip, s.IPv4)
	}
	if len(dest.replaced) != 1 || dest.replaced[0][0] != oldIP || dest.replaced[0][1] != s.IPv4 {
		t.Fatalf("links on the IP should move to the new one: %v", dest.replaced)
	}

	j, _ = o.Engine.Enqueue(KindDelete, &s.ID, struct{}{}, "test")
	if got := waitJob(t, o, j.ID); got.Status != models.JobDone {
		t.Fatalf("delete: %s", got.Error)
	}
	if _, ok := cf.get(s.Hostname); ok {
		t.Fatal("record of a deleted server was kept")
	}
}

func TestHostnameForAndPattern(t *testing.T) {
	if got := HostnameFor("FR{n}", 3, "Example.com."); got != "fr3.example.com" {
		t.Fatal(got)
	}
	for _, bad := range []string{"fr", "fr{n}{n}", "a.b{n}", "-x{n}", "x_{n}"} {
		if ValidPattern(bad) == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
	if err := ValidPattern("de-{n}"); err != nil {
		t.Fatal(err)
	}
}

func TestSyncMovesHostnameConfigsToTheIP(t *testing.T) {
	o, _, _, dest, _, _ := setupDNS(t)
	runAuto(t, o, AutoParams{Count: 1})
	var s models.Server
	o.DB.First(&s, "status = ?", models.ServerActive)
	// Uploaded before links moved from the hostname to the IP.
	o.DB.Model(&s).Update("uploaded_ip", s.Hostname)

	j, _ := o.Engine.Enqueue(KindSync, &s.ID, struct{}{}, "test")
	if got := waitJob(t, o, j.ID); got.Status != models.JobDone {
		t.Fatalf("sync: %s", got.Error)
	}
	o.DB.First(&s, s.ID)
	if len(dest.replaced) != 1 || dest.replaced[0] != [2]string{s.Hostname, s.IPv4} || s.UploadedIP != s.IPv4 {
		t.Fatalf("replaced=%v uploaded=%s", dest.replaced, s.UploadedIP)
	}

	// A second sync has nothing left to move.
	j, _ = o.Engine.Enqueue(KindSync, &s.ID, struct{}{}, "test")
	waitJob(t, o, j.ID)
	if len(dest.replaced) != 1 {
		t.Fatalf("moved again: %v", dest.replaced)
	}
}
