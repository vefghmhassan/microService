package orchestrator

import (
	"context"
	"math"
	"strings"
	"testing"

	"microserver/internal/models"
	"microserver/internal/services/settings"
)

const ssSample = `0      0        5.9.1.2:443      2.144.10.20:51234
	 cubic wscale:8,7 rto:404 rtt:180.5/20.1 ato:40 mss:1380 cwnd:10 bytes_sent:2000000 bytes_acked:1900000 bytes_received:40000 segs_out:1500 segs_in:900 send 600Kbps delivery_rate 420Kbps retrans:0/150 rcv_space:14600
0      0        [::ffff:5.9.1.2]:443      [::ffff:5.120.3.4]:40000
	 bbr rtt:90/5 bytes_acked:900000 segs_out:700 delivery_rate 3.5Mbps
0      0        [2a01:4f8::1]:8443      [2a02:4540::9]:5555
	 cubic rtt:60/2 bytes_acked:5000000 segs_out:4000 delivery_rate 1.2Gbps retrans:1/40
0      0        5.9.1.2:443      2.144.10.21:5000
	 cubic rtt:30/2 bytes_acked:1000 segs_out:5 delivery_rate 950bps
0      36       5.9.1.2%eth0:443      2.144.10.22:5001
	 cubic rtt:300/9 bytes_acked:400000 segs_out:400
`

func TestParseSS(t *testing.T) {
	conns := parseSS(ssSample)
	if len(conns) != 5 {
		t.Fatalf("got %d conns: %+v", len(conns), conns)
	}
	c := conns[0]
	if c.LocalPort != 443 || c.Peer != "2.144.10.20" || c.RTTMs != 180.5 || c.Kbps != 420 || c.BytesAcked != 1900000 || c.Retrans != 150 || c.SegsOut != 1500 {
		t.Fatalf("first: %+v", c)
	}
	if conns[1].Peer != "5.120.3.4" || conns[1].Kbps != 3500 || conns[1].Retrans != 0 {
		t.Fatalf("mapped v4: %+v", conns[1])
	}
	if conns[2].LocalPort != 8443 || conns[2].Peer != "2a02:4540::9" || conns[2].Kbps != 1.2e6 {
		t.Fatalf("v6: %+v", conns[2])
	}
	if math.Abs(conns[3].Kbps-0.95) > 1e-9 {
		t.Fatalf("bps: %+v", conns[3])
	}
	if conns[4].Kbps != 0 || conns[4].LocalPort != 443 {
		t.Fatalf("no delivery_rate / iface: %+v", conns[4])
	}
}

func TestParseSSWithStateColumnAndSameLineInfo(t *testing.T) {
	out := "ESTAB 0 0 1.1.1.1:443 2.2.2.2:9 cubic rtt:10/1 bytes_acked:300000 segs_out:10 delivery_rate 2Mbps\n"
	conns := parseSS(out)
	if len(conns) != 1 || conns[0].Kbps != 2000 || conns[0].RTTMs != 10 {
		t.Fatalf("%+v", conns)
	}
	if got := parseSS("ss: command not found\n"); len(got) != 0 {
		t.Fatalf("junk parsed: %+v", got)
	}
}

func TestSummarize(t *testing.T) {
	q := summarize(parseSS(ssSample))
	// The two connections under 256 KB acked are left out (one has no rate).
	if q.Conns != 3 {
		t.Fatalf("conns %+v", q)
	}
	if q.RTTMs != 90 {
		t.Fatalf("median rtt %+v", q)
	}
	if q.Kbps != 1.2e6 { // p75 of 420, 3500, 1.2e6
		t.Fatalf("p75 %+v", q)
	}
	// (150+0+40+0) / (1500+700+4000+400)
	if q.RetransPct != 2.9 {
		t.Fatalf("retrans %+v", q)
	}
	if got := summarize(nil); got != (qualitySum{}) {
		t.Fatalf("empty %+v", got)
	}
}

func TestQualityCmd(t *testing.T) {
	got := qualityCmd([]int{443, 8443})
	if !strings.Contains(got, "'( sport = :443 or sport = :8443 )'") || !strings.HasPrefix(got, "ss -tinH state established") {
		t.Fatal(got)
	}
}

func TestQualityAlertsOnceAfterBadRounds(t *testing.T) {
	o, _, _, _, acc, _ := setup(t)
	o.Settings.Set(map[string]string{settings.QualityAlert: "true", settings.QualityBadRounds: "3", settings.QualityMinKbps: "500", settings.QualityMinConns: "2"})
	var alerts int
	o.Alert = func(*models.Server, string) { alerts++ }
	s := models.Server{Name: "s1", Status: models.ServerActive, ProviderAccountID: acc.ID}
	o.DB.Create(&s)

	slow := qualitySum{Conns: 5, RTTMs: 200, Kbps: 300}
	for i := 0; i < 5; i++ {
		o.applyQuality(&s, slow, "")
	}
	if alerts != 1 || s.QualityBad != 5 {
		t.Fatalf("alerts %d bad %d", alerts, s.QualityBad)
	}
	// Too little data leaves the count alone; a good round resets it.
	o.applyQuality(&s, qualitySum{Conns: 1, Kbps: 10}, "")
	if s.QualityBad != 5 {
		t.Fatalf("thin round changed count: %d", s.QualityBad)
	}
	o.applyQuality(&s, qualitySum{Conns: 5, Kbps: 5000}, "")
	var got models.Server
	o.DB.First(&got, s.ID)
	if got.QualityBad != 0 || got.QualityKbps != 5000 || got.QualityAt == nil {
		t.Fatalf("stored %+v", got)
	}
	// High retransmits alone are bad too.
	o.Settings.Set(map[string]string{settings.QualityMaxRetrans: "10"})
	if bad, _ := o.qualityBad(qualitySum{Conns: 5, Kbps: 5000, RetransPct: 15}); !bad {
		t.Fatal("retransmits not judged bad")
	}
}

func TestQualityAlertOff(t *testing.T) {
	o, _, _, _, acc, _ := setup(t)
	o.Settings.Set(map[string]string{settings.QualityBadRounds: "1"})
	alerts := 0
	o.Alert = func(*models.Server, string) { alerts++ }
	s := models.Server{Name: "s1", Status: models.ServerActive, ProviderAccountID: acc.ID}
	o.DB.Create(&s)
	o.applyQuality(&s, qualitySum{Conns: 5, Kbps: 10}, "")
	if alerts != 0 {
		t.Fatal("alerted with quality.alert off")
	}
}

type ssRemote struct {
	fakeRemote
	out string
}

func (r *ssRemote) Run(ctx context.Context, cmd string, l func(string)) (string, error) {
	if strings.HasPrefix(cmd, "ss ") {
		r.cmds = append(r.cmds, cmd)
		return r.out, nil
	}
	return r.fakeRemote.Run(ctx, cmd, l)
}

func TestCollectQualityFiltersPeersAndHonorsSwitch(t *testing.T) {
	o, _, _, _, _, _ := setup(t)
	o.Geo = fakeGeo{"2.144.10.20": {Country: "IR"}, "5.120.3.4": {Country: "DE"}}
	r := &ssRemote{out: ssSample + "0 0 5.9.1.2:443 10.0.0.5:1\n\t rtt:1/1 bytes_acked:900000 segs_out:9 delivery_rate 1Mbps\n"}

	all, per, _, ok := o.collectQuality(context.Background(), r, []int{443, 8443})
	if !ok || len(r.cmds) != 1 {
		t.Fatalf("ok %v cmds %v", ok, r.cmds)
	}
	// The German peer and the private one are dropped; unknown peers stay.
	if all.Conns != 2 || per[443].Conns != 1 || per[8443].Conns != 1 {
		t.Fatalf("all %+v per %+v", all, per)
	}

	o.Settings.Set(map[string]string{settings.QualityEnabled: "false"})
	if _, _, _, ok := o.collectQuality(context.Background(), r, []int{443}); ok || len(r.cmds) != 1 {
		t.Fatalf("ran ss while off: %v", r.cmds)
	}
}
