package orchestrator

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"microserver/internal/models"
)

const addonClients = `{"clients":[{"id":"11111111-2222-4333-8444-555555555555","email":"a1","flow":""}],"decryption":"none"}`

func addonSnap() *Snapshot {
	return &Snapshot{
		Hash: "h",
		Inbounds: []models.InboundTemplate{
			{Remark: "DE", Protocol: "vless", Port: 443, Enabled: true, Upload: true, Settings: addonClients,
				StreamSettings: `{"network":"tcp","security":"reality"}`},
		},
		AddonInbounds: map[uint][]models.InboundTemplate{7: {
			{AddonID: 7, Route: models.RouteDirect, Remark: "vless-tls", Protocol: "vless", Port: 9443, Enabled: true, Upload: true,
				Settings: addonClients, StreamSettings: `{"network":"tcp","security":"tls","tlsSettings":{"alpn":["h2"]}}`},
			{AddonID: 7, Route: models.RouteCDN, Remark: "vless-ws", Protocol: "vless", Port: 2053, Enabled: true, Upload: true,
				UploadJSON: true, Fragment: true, CleanIPs: true,
				Settings: addonClients, StreamSettings: `{"network":"ws","security":"tls","wsSettings":{"path":"/w"}}`},
			{AddonID: 7, Route: models.RouteDirect, Remark: "hysteria2", Protocol: "hysteria", Port: 8443, Enabled: true, Upload: true,
				Settings:       `{"version":2,"clients":[{"auth":"secret1","email":"h1"}]}`,
				StreamSettings: `{"network":"hysteria","security":"tls","tlsSettings":{"alpn":["h3"]},"finalmask":{"udp":[{"type":"salamander","settings":{"password":"obfs1"}}]}}`},
		}},
		AddonHashes: map[uint]string{7: "ah"},
	}
}

func addonServer() *models.Server {
	s := &models.Server{IPv4: "10.0.0.9", Hostname: "d1.example.com", CDNHostname: "c1.example.com", CDNAddonID: 7, XrayVersion: "26.1.13"}
	s.SSL = true
	return s
}

func TestForAddsAddonInboundsOnlyToItsServers(t *testing.T) {
	snap := addonSnap()
	plain := &models.Server{IPv4: "10.0.0.1"}
	if got := snap.For(plain); len(got.Inbounds) != 1 || got.AddonHash != "" {
		t.Fatalf("server without add-on: %+v", got.Inbounds)
	}
	s := addonServer()
	got := snap.For(s)
	if len(got.Inbounds) != 4 || got.AddonHash != "ah" || got.DirectHost != "d1.example.com" || got.CDNHost != "c1.example.com" {
		t.Fatalf("add-on server: %d inbounds, hash %q, hosts %q %q", len(got.Inbounds), got.AddonHash, got.DirectHost, got.CDNHost)
	}
	s.XrayVersion = "25.10.15"
	if got := snap.For(s); len(got.Inbounds) != 3 {
		t.Fatalf("hysteria2 must need xray 26: %d inbounds", len(got.Inbounds))
	}
	s.SSL = false
	if got := snap.For(s); len(got.Inbounds) != 1 {
		t.Fatalf("no add-on inbounds without SSL: %d", len(got.Inbounds))
	}
	if len(snap.Inbounds) != 1 {
		t.Fatal("For changed the shared snapshot")
	}
}

func TestUploadEntriesUseBothNamesCleanIPsAndFragment(t *testing.T) {
	snap := addonSnap().For(addonServer())
	snap.Clean = []models.CleanIP{{Address: "104.16.1.1", Operator: "MCI"}}
	entries, err := UploadEntries(snap, "d1.example.com")
	if err != nil {
		t.Fatal(err)
	}
	// reality (shared) + tls + ws JSON + ws clean JSON + hysteria2
	if len(entries) != 5 {
		t.Fatalf("%d entries: %v", len(entries), entries)
	}
	var reality, tls, ws, clean, hy string
	for _, e := range entries {
		switch {
		case strings.Contains(e, "security=reality"):
			reality = e
		case strings.HasPrefix(e, "hysteria2://"):
			hy = e
		case strings.HasPrefix(e, "vless://") && strings.Contains(e, ":9443?"):
			tls = e
		case strings.Contains(e, `"104.16.1.1"`):
			clean = e
		case strings.HasPrefix(e, "{"):
			ws = e
		}
	}
	if !strings.Contains(reality, "@d1.example.com:443") || !strings.Contains(reality, "#DE-a1") {
		t.Errorf("shared inbound: %s", reality)
	}
	if !strings.Contains(tls, "@d1.example.com:9443") || !strings.Contains(tls, "sni=d1.example.com") || !strings.Contains(tls, "#vless-tls-a1") {
		t.Errorf("direct TLS: %s", tls)
	}
	if !strings.Contains(hy, "@d1.example.com:8443") || !strings.Contains(hy, "obfs=salamander") || !strings.Contains(hy, "obfs-password=obfs1") ||
		!strings.HasPrefix(hy, "hysteria2://secret1@") || !strings.Contains(hy, "#hysteria2-h1") {
		t.Errorf("hysteria2: %s", hy)
	}
	for name, j := range map[string]string{"cdn": ws, "clean": clean} {
		var cfg map[string]any
		if err := json.Unmarshal([]byte(j), &cfg); err != nil {
			t.Fatalf("%s json: %v (%s)", name, err, j)
		}
		if !strings.Contains(j, `"serverName":"c1.example.com"`) ||
			!strings.Contains(j, `"dialerProxy":"fragment"`) || !strings.Contains(j, `"tag":"fragment"`) {
			t.Errorf("%s config: %s", name, j)
		}
	}
	// Through a clean IP the orange name must travel in the Host header.
	if !strings.Contains(ws, `"address":"c1.example.com"`) || !strings.Contains(clean, `"host":"c1.example.com"`) ||
		!strings.Contains(clean, `"remarks":"vless-ws MCI 104.16.1.1-a1"`) {
		t.Errorf("ws %s\nclean %s", ws, clean)
	}
}

func TestXrayAtLeast(t *testing.T) {
	for v, want := range map[string]bool{"26.1.13": true, "v26.0.0": true, "27": true, "25.10.15": false, "": false, "x": false} {
		if XrayAtLeast(v, 26) != want {
			t.Errorf("%q: want %t", v, want)
		}
	}
}

// recRemote records every command of every connection, in order.
type recRemote struct {
	fakeRemote
	mu  *sync.Mutex
	all *[]string
}

func (r *recRemote) Run(ctx context.Context, cmd string, f func(string)) (string, error) {
	r.mu.Lock()
	*r.all = append(*r.all, cmd)
	r.mu.Unlock()
	return r.fakeRemote.Run(ctx, cmd, f)
}

func TestAddonServerGetsTwoRecordsAndSSLBeforeTheInstall(t *testing.T) {
	o, cf, _, dest, acc, _ := setupDNS(t)
	var mu sync.Mutex
	var cmds []string
	o.Connect = func(context.Context, *models.Server) (Remote, string, error) {
		return &recRemote{mu: &mu, all: &cmds}, "ssh-ed25519 HOSTKEY", nil
	}
	var cfAcc models.CloudflareAccount
	o.DB.First(&cfAcc)
	addon := &models.CDNAddon{Name: "cdn", Enabled: true, CFAccountID: cfAcc.ID, CFZoneID: "z1", CFZoneName: "example.com",
		DirectPattern: "d{n}", CDNPattern: "c{n}"}
	o.DB.Create(addon)
	o.DB.Create(&models.InboundTemplate{AddonID: addon.ID, Route: models.RouteCDN, Remark: "vless-ws", Protocol: "vless", Port: 2053,
		Enabled: true, Upload: true, Settings: addonClients, StreamSettings: `{"network":"ws","security":"tls","wsSettings":{"path":"/w"}}`})
	o.DB.Model(&models.Location{}).Where("provider_account_id = ?", acc.ID).Update("cdn_addon_id", addon.ID)

	runAuto(t, o, AutoParams{Count: 1})

	var s models.Server
	o.DB.First(&s, "status = ?", models.ServerActive)
	if s.Hostname != "d1.example.com" || s.CDNHostname != "c1.example.com" || s.CDNAddonID != addon.ID {
		t.Fatalf("names %q %q addon %d", s.Hostname, s.CDNHostname, s.CDNAddonID)
	}
	cf.mu.Lock()
	grey, orange := cf.records["d1.example.com"], cf.records["c1.example.com"]
	pg, po := cf.proxied["d1.example.com"], cf.proxied["c1.example.com"]
	cf.mu.Unlock()
	if grey != s.IPv4 || orange != s.IPv4 || pg || !po {
		t.Fatalf("records grey=%s(%t) orange=%s(%t), ip %s", grey, pg, orange, po, s.IPv4)
	}
	if s.XUIScheme != "https" || s.AddonHash == "" {
		t.Fatalf("scheme %s addon hash %q", s.XUIScheme, s.AddonHash)
	}
	idx := func(sub string) int {
		for i, c := range cmds {
			if strings.Contains(c, sub) {
				return i
			}
		}
		return -1
	}
	certCopy, install, webCert := idx("/root/cert/fullchain.pem.new"), idx("x-ui-install.sh"), idx("cert -webCert")
	if certCopy < 0 || install < 0 || webCert < 0 || !(certCopy < install && install < webCert) {
		t.Fatalf("order cert=%d install=%d webCert=%d", certCopy, install, webCert)
	}
	if len(dest.uploads) != 1 {
		t.Fatalf("uploads %v", dest.uploads)
	}
	joined := strings.Join(dest.uploads[0], "\n")
	if !strings.Contains(joined, "@10.0.0.1:443") || !strings.Contains(joined, "@c1.example.com:2053") ||
		!strings.Contains(joined, "sni=c1.example.com") {
		t.Fatalf("uploaded %s", joined)
	}

	j, _ := o.Engine.Enqueue(KindDelete, &s.ID, struct{}{}, "test")
	if got := waitJob(t, o, j.ID); got.Status != models.JobDone {
		t.Fatalf("delete: %s", got.Error)
	}
	if _, ok := cf.get("c1.example.com"); ok {
		t.Fatal("orange record of a deleted server was kept")
	}
	if _, ok := cf.get("d1.example.com"); ok {
		t.Fatal("grey record of a deleted server was kept")
	}
}
