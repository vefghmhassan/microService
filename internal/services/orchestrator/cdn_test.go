package orchestrator

import (
	"strings"
	"testing"

	"microserver/internal/models"
)

func TestSnapshotForSkipsCertInboundsWithoutSSL(t *testing.T) {
	snap := &Snapshot{Hash: "h", Inbounds: []models.InboundTemplate{
		{Port: 1, StreamSettings: `{"network":"tcp","security":"reality"}`},
		{Port: 443, CDN: true, StreamSettings: `{"network":"ws","security":"tls"}`},
		{Port: 8080, CDN: true, StreamSettings: `{"network":"ws","security":"none"}`},
	}}
	got := snap.For(&models.Server{Hostname: "fr1.example.com"})
	if len(got.Inbounds) != 2 || got.Inbounds[1].Port != 8080 || got.Hash != "h" {
		t.Fatalf("without SSL: %+v", got.Inbounds)
	}
	if len(snap.Inbounds) != 3 {
		t.Fatal("For changed the shared snapshot")
	}
	s := &models.Server{Hostname: "fr1.example.com"}
	s.SSL = true
	if got := snap.For(s); len(got.Inbounds) != 3 {
		t.Fatalf("with SSL: %d inbounds", len(got.Inbounds))
	}
}

func TestCleanLinksOnlyForProxiedCDN(t *testing.T) {
	settings := `{"clients":[{"id":"11111111-1111-1111-1111-111111111111","email":"a"}]}`
	snap := &Snapshot{Inbounds: []models.InboundTemplate{
		{Remark: "reality", Protocol: "vless", Port: 1, Enabled: true, Upload: true, Settings: settings, StreamSettings: `{"network":"tcp","security":"reality"}`},
		{Remark: "ws", Protocol: "vless", Port: 443, Enabled: true, Upload: true, CDN: true, Settings: settings, StreamSettings: `{"network":"ws","security":"tls","wsSettings":{"path":"/w"}}`},
	}}
	ips := []models.CleanIP{{Address: "104.16.1.1", Operator: "MCI"}, {Address: "104.16.1.2"}}
	s := &models.Server{Hostname: "fr1.example.com"}
	s.SSL = true
	if ls, _ := CleanLinks(snap, s, ips); len(ls) != 0 {
		t.Fatalf("not proxied: %d links", len(ls))
	}
	s.DNSProxied = true
	ls, err := CleanLinks(snap, s, ips)
	if err != nil || len(ls) != 2 {
		t.Fatalf("%v %v", ls, err)
	}
	if !strings.Contains(ls[0].URL, "@104.16.1.1:443?") || !strings.Contains(ls[0].URL, "sni=fr1.example.com") ||
		!strings.Contains(ls[0].URL, "host=fr1.example.com") || !strings.Contains(ls[0].URL, "#ws%20MCI%20104.16.1.1-a") {
		t.Errorf("link %s", ls[0].URL)
	}
	s.SSL = false
	if ls, _ := CleanLinks(snap, s, ips); len(ls) != 0 {
		t.Fatalf("TLS CDN inbound without SSL: %d links", len(ls))
	}
}
