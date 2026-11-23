package orchestrator

import (
	"context"
	"strings"
	"testing"

	"microserver/internal/models"
	"microserver/internal/services/xui"
)

func hostRemarks(hs []xui.HostGroup) string {
	var out []string
	for _, h := range hs {
		out = append(out, h.Remark+"@"+h.Hosts[0]+"|sni="+h.Sni+"|host="+h.HostHeader+"|frag="+map[bool]string{true: "y", false: "n"}[h.FinalMask != ""])
	}
	return strings.Join(out, "\n")
}

func TestPanelHostsLikeTheScript(t *testing.T) {
	s := testAddonServer()
	s.IPv6 = "2a01::5"
	ns := proSnap().For(s)
	ns.Clean = []models.CleanIP{{Address: "104.16.1.1"}, {Address: "104.16.1.2"}}
	ins := map[string]models.InboundTemplate{}
	for _, t := range ns.Inbounds {
		ins[t.Remark] = t
	}

	if got := hostRemarks(panelHosts(ns, s, ins["Reality"], 1, false)); got !=
		"⚡ Reality · Main@5.5.5.5|sni=|host=|frag=n\n6️⃣ Reality · IPv6@2a01::5|sni=|host=|frag=n" {
		t.Fatalf("reality:\n%s", got)
	}
	if got := hostRemarks(panelHosts(ns, s, ins["TLS"], 2, false)); got !=
		"🔐 TLS · Main@5.5.5.5|sni=d1.example.com|host=|frag=n\n"+
			"🧩 TLS · Fragment@5.5.5.5|sni=d1.example.com|host=|frag=y\n"+
			"6️⃣ TLS · IPv6@2a01::5|sni=d1.example.com|host=|frag=n" {
		t.Fatalf("tls:\n%s", got)
	}
	if got := hostRemarks(panelHosts(ns, s, ins["WS"], 3, true)); got !=
		"☁️ WS · CDN@c1.example.com|sni=c1.example.com|host=c1.example.com|frag=y\n"+
			"🧊 Clean 01 · 104.16.1.1@104.16.1.1|sni=c1.example.com|host=c1.example.com|frag=y\n"+
			"🧊 Clean 02 · 104.16.1.2@104.16.1.2|sni=c1.example.com|host=c1.example.com|frag=y" {
		t.Fatalf("cdn:\n%s", got)
	}
	for _, h := range panelHosts(ns, s, ins["WS"], 3, true) {
		if h.Tags[0] != HostTag || h.InboundIDs[0] != 3 || h.Security != "same" {
			t.Fatalf("row %+v", h)
		}
	}
}

func TestPanelHostRowsReplaceOnlyOurs(t *testing.T) {
	o, _, _, _, _, _ := setup(t)
	s := testAddonServer()
	ns := proSnap().For(s)
	p := &fakePanel{hosts: []xui.HostGroup{
		{GroupID: "mine", Remark: "operator", Tags: []string{"vip"}},
		{GroupID: "old", Remark: "stale", Tags: []string{HostTag}},
	}, nextHost: 10}
	n, err := o.panelHostRows(context.Background(), p, s, ns, map[int]int{443: 1, 9443: 2, 2053: 3})
	if err != nil || n == 0 {
		t.Fatalf("%d %v", n, err)
	}
	var remarks []string
	for _, h := range p.hosts {
		remarks = append(remarks, h.Remark)
	}
	all := strings.Join(remarks, ",")
	if !strings.Contains(all, "operator") || strings.Contains(all, "stale") || !strings.Contains(all, "☁️ WS · CDN") {
		t.Fatalf("hosts %s", all)
	}
	// A second sync gives the same rows, not twice as many.
	before := len(p.hosts)
	_, _ = o.panelHostRows(context.Background(), p, s, ns, map[int]int{443: 1, 9443: 2, 2053: 3})
	if len(p.hosts) != before {
		t.Fatalf("re-sync %d → %d rows", before, len(p.hosts))
	}
}

func TestPanelSubscriptionStaysOffWhenAnInboundHoldsItsPort(t *testing.T) {
	o, _, _, _, _, _ := setup(t)
	s := testAddonServer()
	snap := &Snapshot{Inbounds: []models.InboundTemplate{{Remark: "x", Port: 2096, Enabled: true}}}
	p := &fakePanel{}
	if err := o.panelSubscription(context.Background(), nil, s, p, p, snap); err == nil || p.settings != nil {
		t.Fatalf("subscription turned on over an inbound: %v %v", err, p.settings)
	}
}

func TestKnownXrayIgnoresUnknown(t *testing.T) {
	for v, want := range map[string]bool{"26.7.28": true, "v25.9.11": true, "Unknown": false, "": false} {
		if knownXray(v) != want {
			t.Fatalf("%q: %t", v, !want)
		}
	}
}

func TestPanelHostsHysteriaNamesItsCertAndIconsAreNotDoubled(t *testing.T) {
	s := testAddonServer()
	ns := proSnap().For(s)
	hy := models.InboundTemplate{Remark: "hysteria2", Protocol: "hysteria", Port: 14443, Enabled: true, AddonID: 1, Route: models.RouteDirect,
		StreamSettings: `{"network":"hysteria","security":"tls"}`}
	if got := hostRemarks(panelHosts(ns, s, hy, 4, false)); got != "🚄 hysteria2 · Main@d1.example.com|sni=d1.example.com|host=|frag=n" {
		t.Fatalf("hysteria:\n%s", got)
	}
	r := ns.Inbounds[0]
	r.Remark = "⚡ Reality Vision"
	if got := hostRemarks(panelHosts(ns, s, r, 1, false)); got != "⚡ Reality Vision · Main@5.5.5.5|sni=|host=|frag=n" {
		t.Fatalf("reality:\n%s", got)
	}
}
