package orchestrator

import (
	"context"
	"encoding/base64"
	"net"
	"regexp"
	"strings"
	"testing"

	"microserver/internal/models"
)

// scriptRemote answers a script with canned output and keeps it.
type scriptRemote struct {
	out    string
	script string
}

func (r *scriptRemote) Run(_ context.Context, cmd string, _ func(string)) (string, error) {
	r.script = cmd
	if b64, ok := strings.CutPrefix(cmd, "echo "); ok {
		if dec, err := base64.StdEncoding.DecodeString(strings.Fields(b64)[0]); err == nil {
			r.script = string(dec)
		}
	}
	return r.out, nil
}
func (r *scriptRemote) ReadFile(context.Context, string) (string, error) { return "", nil }
func (r *scriptRemote) DialContext(context.Context, string, string) (net.Conn, error) {
	return nil, net.ErrClosed
}
func (r *scriptRemote) Close() error { return nil }

func proSnap() *Snapshot {
	vless := `{"clients":[{"id":"11111111-1111-1111-1111-111111111111","email":"a"}],"decryption":"none"}`
	cert := `"tlsSettings":{"certificates":[{"certificateFile":"/root/cert/fullchain.pem"}]}`
	return &Snapshot{
		Inbounds: []models.InboundTemplate{
			{Remark: "Reality", Protocol: "vless", Port: 443, Enabled: true, Upload: true, Settings: vless,
				StreamSettings: `{"network":"tcp","security":"reality","realitySettings":{"serverNames":["dl.google.com"],"shortIds":["ab"],"settings":{"publicKey":"pk"}}}`},
		},
		AddonInbounds: map[uint][]models.InboundTemplate{1: {
			{Remark: "TLS", Protocol: "vless", Port: 9443, Enabled: true, Upload: true, AddonID: 1, Route: models.RouteDirect, Fragment: true, Settings: vless,
				StreamSettings: `{"network":"tcp","security":"tls",` + cert + `}`},
			{Remark: "WS", Protocol: "vless", Port: 2053, Enabled: true, Upload: true, AddonID: 1, Route: models.RouteCDN, CleanIPs: true, Settings: vless,
				StreamSettings: `{"network":"ws","security":"tls","wsSettings":{"path":"/w"},` + cert + `}`},
		}},
		AddonHashes: map[uint]string{1: "h"},
	}
}

func testAddonServer() *models.Server {
	s := &models.Server{IPv4: "5.5.5.5", Hostname: "d1.example.com", CDNHostname: "c1.example.com", CDNAddonID: 1, XrayVersion: "26.10.31"}
	s.SSL = true
	return s
}

func TestConfigTestDialsDirectLocallyAndCDNThroughCloudflare(t *testing.T) {
	s := testAddonServer()
	ns := proSnap().For(s)
	ns.Clean = []models.CleanIP{{Address: "104.16.1.1"}}
	links := configTestLinks(ns, s)
	var names []string
	for _, l := range links {
		names = append(names, l.name+"/"+l.via)
	}
	want := "Reality/direct WS/cdn WS · 104.16.1.1/clean TLS/direct TLS · Fragment/direct"
	if strings.Join(names, " ") != want {
		t.Fatalf("links %v", names)
	}

	r := &scriptRemote{out: "R 0 204 0.12 \nR 1 000 10 \nR 2 204 0.4 \nR 3 204 0.2 \nR 4 204 0.5 \n"}
	res, err := TestConfigs(context.Background(), r, ns, s)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 5 || !res[0].OK || res[0].MS != 120 || res[1].OK || res[1].Error == "" || !res[2].OK || res[2].Via != ViaClean {
		t.Fatalf("results %+v", res)
	}
	cfg := regexp.MustCompile(`echo (\S+) \| base64 -d > "\$D/c.json"`).FindStringSubmatch(r.script)
	if cfg == nil {
		t.Fatalf("no config in script:\n%s", r.script)
	}
	js, _ := base64.StdEncoding.DecodeString(cfg[1])
	c := string(js)
	// Direct configs on the loopback, CDN ones by name and by clean IP, and
	// the fragment outbound the Fragment variant dials through.
	for _, want := range []string{`"address":"127.0.0.1"`, `"address":"c1.example.com"`, `"address":"104.16.1.1"`, `"dialerProxy":"fragment"`, `"tag":"fragment"`, `"serverName":"c1.example.com"`} {
		if !strings.Contains(c, want) {
			t.Fatalf("missing %s in %s", want, c)
		}
	}
	if strings.Contains(c, `"address":"d1.example.com"`) || strings.Contains(c, "5.5.5.5") {
		t.Fatalf("direct config dials the public address: %s", c)
	}
	if CDNPort(ns) != 2053 {
		t.Fatalf("cdn port %d", CDNPort(ns))
	}

	// Without SSL the add-on inbounds are not on the server, so not tested.
	s.SSL = false
	if got := configTestLinks(proSnap().For(s), s); len(got) != 1 || got[0].name != "Reality" {
		t.Fatalf("without SSL %+v", got)
	}
}

func TestCleanViaCDNScriptAndResults(t *testing.T) {
	s := &models.Server{CDNHostname: "c1.example.com"}
	r := &scriptRemote{out: "R 0 404 0.05\nR 1 000 0\n"}
	res, err := TestCleanViaCDN(context.Background(), r, s, 2053, []string{"104.16.1.1", "time.is", "2606:4700::1", "bad;rm"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 || !res[0].OK || res[0].MS != 50 || res[1].OK || res[1].Address != "time.is" {
		t.Fatalf("results %+v", res)
	}
	for _, want := range []string{"--resolve c1.example.com:2053:", "https://c1.example.com:2053/", "getent ahostsv4", "time.is"} {
		if !strings.Contains(r.script, want) {
			t.Fatalf("missing %q in\n%s", want, r.script)
		}
	}
	if strings.Contains(r.script, "rm") {
		t.Fatal("unsafe address reached the script")
	}
	if _, err := TestCleanViaCDN(context.Background(), r, &models.Server{}, 443, []string{"1.1.1.1"}); err == nil {
		t.Fatal("server without CDN hostname accepted")
	}
}

func TestHostTuningScript(t *testing.T) {
	snap := &Snapshot{Inbounds: []models.InboundTemplate{
		{Protocol: "vless", Port: 443, Enabled: true}, {Protocol: "hysteria", Port: 5443, Enabled: true},
		{Protocol: "shadowsocks", Port: 8080, Enabled: true}, {Protocol: "vless", Port: 1, Enabled: false},
	}}
	sc := hostTuningScript(snap)
	for _, want := range []string{"ufw allow 443/tcp", "ufw allow 5443/udp", "ufw allow 8080/tcp", "ufw allow 8080/udp", "tcp_congestion_control=bbr"} {
		if !strings.Contains(sc, want) {
			t.Fatalf("missing %q in\n%s", want, sc)
		}
	}
	if strings.Contains(sc, "5443/tcp") || strings.Contains(sc, " 1/tcp") {
		t.Fatalf("wrong ports:\n%s", sc)
	}
	if !strings.Contains(hostTuningScript(&Snapshot{}), "then\n  :\nfi") {
		t.Fatal("empty ufw block is not valid bash")
	}
}
