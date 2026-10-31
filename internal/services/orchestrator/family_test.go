package orchestrator

import (
	"strings"
	"testing"

	"microserver/internal/models"
)

func TestSnapshotForPicksIPFamily(t *testing.T) {
	settings := `{"clients":[{"id":"11111111-1111-1111-1111-111111111111","email":"a"}]}`
	snap := &Snapshot{
		Xray:   models.XrayTemplate{ID: 1, Routing: `{"rules":[]}`},
		XrayV6: models.XrayTemplate{ID: 2},
		Inbounds: []models.InboundTemplate{
			{Remark: "all", Protocol: "vless", Port: 1, Enabled: true, Upload: true, Settings: settings, StreamSettings: `{"network":"tcp"}`},
			{Remark: "v4", Protocol: "vless", Port: 2, Enabled: true, Upload: true, Family: "v4", Settings: settings, StreamSettings: `{"network":"tcp"}`},
			{Remark: "v6", Protocol: "vless", Port: 3, Enabled: true, Upload: true, Family: "v6", Settings: settings, StreamSettings: `{"network":"tcp"}`},
		},
	}
	snap.Hash, snap.HashV6 = snap.hash(models.FamilyV4), snap.hash(models.FamilyV6)
	if snap.Hash == snap.HashV6 {
		t.Fatal("v4 and v6 hashes should differ")
	}

	ports := func(s *Snapshot) (out []int) {
		for _, in := range s.Inbounds {
			out = append(out, in.Port)
		}
		return
	}
	v4 := snap.For(&models.Server{IPv4: "203.0.113.7"})
	if p := ports(v4); len(p) != 2 || p[0] != 1 || p[1] != 2 || v4.Hash != snap.Hash {
		t.Fatalf("v4 server: %v hash %s", p, v4.Hash)
	}
	s6 := &models.Server{IPv4: "203.0.113.7", IPv6: "2a01:4f8::1", IPFamily: "v6"}
	v6 := snap.For(s6)
	if p := ports(v6); len(p) != 2 || p[0] != 1 || p[1] != 3 || v6.Hash != snap.HashV6 {
		t.Fatalf("v6 server: %v hash %s", p, v6.Hash)
	}
	// An empty v6 xray row falls back to the v4 one.
	if v6.Xray.Routing != `{"rules":[]}` {
		t.Fatalf("v6 xray %+v", v6.Xray)
	}

	// Links of a v6 server point at the bracketed IPv6.
	if LinkHost(s6) != "2a01:4f8::1" {
		t.Fatalf("link host %s", LinkHost(s6))
	}
	urls, err := UploadEntries(v6, LinkHost(s6))
	if err != nil || len(urls) != 2 || !strings.Contains(urls[0], "@[2a01:4f8::1]:1") {
		t.Fatalf("%v %v", urls, err)
	}
}
