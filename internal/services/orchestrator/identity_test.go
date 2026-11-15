package orchestrator

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"golang.org/x/crypto/curve25519"

	"microserver/internal/models"
	"microserver/internal/services/checker"
	"microserver/internal/services/xui"
)

const realityStream = `{"network":"xhttp","security":"reality","realitySettings":{"privateKey":"TPL","serverNames":["dl.google.com"],"target":"dl.google.com:443","shortIds":["5f","01c3","9d2bf75946abd0"],"settings":{"publicKey":"TPLPUB","fingerprint":"chrome","spiderX":"/tpl"}},"xhttpSettings":{"path":"/tpl","mode":"auto"}}`
const realityClients = `{"clients":[{"id":"11111111-1111-1111-1111-111111111111","email":"a","flow":""}],"decryption":"none"}`

func identitySnap() *Snapshot {
	return &Snapshot{Inbounds: []models.InboundTemplate{
		{ID: 7, Remark: "r", Protocol: "vless", Port: 443, Enabled: true, Upload: true, Settings: realityClients, StreamSettings: realityStream},
		{ID: 8, Remark: "plain", Protocol: "vless", Port: 80, Enabled: true, Upload: true, Settings: realityClients, StreamSettings: `{"network":"tcp","security":"none"}`},
	}}
}

func TestServerWithoutSeedKeepsTheTemplate(t *testing.T) {
	snap := identitySnap()
	got := snap.For(&models.Server{IPv4: "203.0.113.7"})
	if got.Inbounds[0].StreamSettings != realityStream || got.Inbounds[0].Settings != realityClients {
		t.Fatalf("template changed for a server without a seed: %s", got.Inbounds[0].StreamSettings)
	}
}

func TestIdentityIsPerServerAndStable(t *testing.T) {
	snap := identitySnap()
	loc := &models.Location{Profile: "a", RealitySNIs: "One.example.com:443, two.example.com\n# note\nthree.example.com"}
	a, b := &models.Server{IPv4: "203.0.113.7"}, &models.Server{IPv4: "203.0.113.8"}
	newIdentity(a, loc)
	newIdentity(b, loc)
	if a.IdentitySeed == "" || a.IdentitySeed == b.IdentitySeed || a.Profile != "a" {
		t.Fatalf("seeds %q %q profile %q", a.IdentitySeed, b.IdentitySeed, a.Profile)
	}
	if !strings.HasSuffix(a.RealitySNI, ".example.com") || strings.Contains(a.RealitySNI, ":") || a.RealitySNI != strings.ToLower(a.RealitySNI) {
		t.Fatalf("sni %q", a.RealitySNI)
	}

	linkOf := func(s *models.Server) *url.URL {
		t.Helper()
		ls, err := Links(snap.For(s), s.IPv4)
		if err != nil || len(ls) != 2 {
			t.Fatalf("%v %v", ls, err)
		}
		u, err := url.Parse(ls[0].URL)
		if err != nil {
			t.Fatal(err)
		}
		return u
	}
	la, la2, lb := linkOf(a), linkOf(a), linkOf(b)
	if la.String() != la2.String() {
		t.Fatal("the same server must always get the same link")
	}
	for _, k := range []string{"pbk", "sid", "spx", "path"} {
		if la.Query().Get(k) == "" || la.Query().Get(k) == lb.Query().Get(k) {
			t.Fatalf("%s: %q vs %q", k, la.Query().Get(k), lb.Query().Get(k))
		}
	}
	if la.User.Username() == lb.User.Username() || la.User.Username() == "11111111-1111-1111-1111-111111111111" {
		t.Fatalf("client id not per server: %s", la.User.Username())
	}
	if la.Query().Get("sni") != a.RealitySNI || len(la.Query().Get("sid")) != 2 {
		t.Fatalf("sni %q sid %q", la.Query().Get("sni"), la.Query().Get("sid"))
	}

	// What goes on the panel matches the link: the private key belongs to
	// the public one, and the target is the picked name.
	var stream struct {
		RealitySettings struct {
			PrivateKey  string   `json:"privateKey"`
			Target      string   `json:"target"`
			ServerNames []string `json:"serverNames"`
			ShortIds    []string `json:"shortIds"`
		} `json:"realitySettings"`
	}
	in := snap.For(a).Inbounds[0]
	if err := json.Unmarshal([]byte(in.StreamSettings), &stream); err != nil {
		t.Fatal(err)
	}
	priv, err := base64.RawURLEncoding.DecodeString(stream.RealitySettings.PrivateKey)
	if err != nil || len(priv) != 32 {
		t.Fatalf("private key %q: %v", stream.RealitySettings.PrivateKey, err)
	}
	pub, _ := curve25519.X25519(priv, curve25519.Basepoint)
	if base64.RawURLEncoding.EncodeToString(pub) != la.Query().Get("pbk") {
		t.Fatal("public key in the link does not belong to the panel's private key")
	}
	rs := stream.RealitySettings
	if rs.Target != a.RealitySNI+":443" || len(rs.ServerNames) != 1 || rs.ServerNames[0] != a.RealitySNI {
		t.Fatalf("target %q names %v", rs.Target, rs.ServerNames)
	}
	if len(rs.ShortIds) != 3 || len(rs.ShortIds[1]) != 4 || len(rs.ShortIds[2]) != 14 {
		t.Fatalf("short ids %v", rs.ShortIds)
	}
	// The template itself is never modified.
	if snap.Inbounds[0].StreamSettings != realityStream {
		t.Fatal("shared template was modified")
	}
}

func TestInboundProfiles(t *testing.T) {
	st := `{"network":"tcp"}`
	snap := &Snapshot{Inbounds: []models.InboundTemplate{
		{Remark: "all", Protocol: "vless", Port: 1, Enabled: true, StreamSettings: st},
		{Remark: "a", Protocol: "vless", Port: 2, Enabled: true, StreamSettings: st, Profiles: "a"},
		{Remark: "ab", Protocol: "vless", Port: 3, Enabled: true, StreamSettings: st, Profiles: "a, B"},
	}}
	ports := func(profile string) (out []int) {
		for _, in := range snap.For(&models.Server{IPv4: "203.0.113.7", Profile: profile}).Inbounds {
			out = append(out, in.Port)
		}
		return
	}
	for profile, want := range map[string][]int{"": {1}, "a": {1, 2, 3}, "b": {1, 3}, "c": {1}} {
		got := ports(profile)
		if len(got) != len(want) {
			t.Fatalf("profile %q: %v, want %v", profile, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("profile %q: %v, want %v", profile, got, want)
			}
		}
	}
	// A template without profiles hashes exactly as before they existed.
	plain := &Snapshot{Inbounds: snap.Inbounds[:1]}
	before := plain.hash(models.FamilyV4)
	plain.Inbounds[0].Profiles = "a"
	if plain.hash(models.FamilyV4) == before {
		t.Fatal("limiting an inbound to a profile must change the hash")
	}
	if ValidProfile("Bad Name") == nil || ValidProfile("vision-1") != nil || ValidProfile("") != nil {
		t.Fatal("profile validation")
	}
}

func TestPortBreakdownAfterABlock(t *testing.T) {
	o, _, _, _, acc, _ := setup(t)
	st := `{"network":"tcp","security":"none"}`
	o.DB.Where("1 = 1").Delete(&models.InboundTemplate{}) // the harness seeds its own
	for _, in := range []models.InboundTemplate{
		{Remark: "main", Protocol: "vless", Port: 443, Enabled: true, StreamSettings: st},
		{Remark: "second", Protocol: "vless", Port: 3443, Enabled: true, StreamSettings: st},
		{Remark: "third", Protocol: "vless", Port: 8080, Enabled: true, StreamSettings: st},
		{Remark: "off", Protocol: "vless", Port: 9000, Enabled: true, StreamSettings: st},
		{Remark: "other profile", Protocol: "vless", Port: 9100, Enabled: true, StreamSettings: st, Profiles: "x"},
	} {
		in := in
		if err := o.DB.Create(&in).Error; err != nil {
			t.Fatal(err)
		}
	}
	o.DB.Model(&models.InboundTemplate{}).Where("port = ?", 9000).Update("enabled", false)
	s := &models.Server{Name: "s", ProviderAccountID: acc.ID, IPv4: "10.9.9.9", Status: models.ServerActive, AppliedHash: "h"}
	if err := o.DB.Create(s).Error; err != nil {
		t.Fatal(err)
	}
	blocked := map[int]bool{443: true, 8080: true}
	o.Check = func(_ context.Context, host string, port int) *checker.Result {
		res := &checker.Result{Host: host, Port: port}
		ok := !blocked[port]
		res.Probes = []checker.Probe{
			{Source: "t", Country: "IR", ASN: "AS1", OK: ok}, {Source: "t", Country: "IR", ASN: "AS2", OK: ok}, {Source: "local", OK: true},
		}
		checker.Decide(res, checker.Policy{MinOK: 2, MinASN: 2})
		return res
	}
	got := o.portBreakdown(context.Background(), nil, s, 443)
	if !strings.Contains(got, "443") || !strings.Contains(got, "8080") || !strings.Contains(got, "باز: 3443") || strings.Contains(got, "9000") || strings.Contains(got, "9100") {
		t.Fatalf("breakdown: %s", got)
	}
	var runs []models.CheckRun
	o.DB.Where("server_id = ?", s.ID).Order("port").Find(&runs)
	if len(runs) != 2 || runs[0].Port != 3443 || !runs[0].OK || runs[1].Port != 8080 || runs[1].OK {
		t.Fatalf("stored checks: %+v", runs)
	}
	var fresh models.Server
	o.DB.First(&fresh, s.ID)
	if fresh.LastCheckAt != nil {
		t.Fatal("the extra port checks must not touch the server's last check")
	}

	blocked[3443] = true
	if got := o.portBreakdown(context.Background(), nil, s, 443); !strings.Contains(got, "کل IP بسته است") {
		t.Fatalf("all closed: %s", got)
	}
}

// fakeScanner answers Reality scans: near for a CIDR, builtin for "", and
// resolves[name] as the address a name is found at.
type fakeScanner struct {
	near, builtin []xui.RealityScan
	resolves      map[string]string
	unreachable   map[string]bool // resolves next door, but the server cannot reach it
	asked         []string
}

func (f *fakeScanner) ScanRealityTargets(_ context.Context, targets string) ([]xui.RealityScan, error) {
	f.asked = append(f.asked, targets)
	if targets == "" {
		return f.builtin, nil
	}
	return f.near, nil
}

// Like 3x-ui, a scan of a name does not say which address it reached.
func (f *fakeScanner) ScanRealityTarget(_ context.Context, target, sni string, _ int) (*xui.RealityScan, error) {
	_, ok := f.resolves[sni]
	return &xui.RealityScan{Target: target, Host: sni, Feasible: ok && !f.unreachable[sni]}, nil
}

func (f *fakeScanner) resolve(_ context.Context, name string) []string {
	if ip, ok := f.resolves[name]; ok {
		return []string{ip}
	}
	return nil
}

func TestChooseSNIPrefersATestedNeighbour(t *testing.T) {
	sc := &fakeScanner{
		near: []xui.RealityScan{
			{IP: "203.0.113.7", Feasible: true, ServerNames: []string{"self.example.com"}},                     // the server itself
			{IP: "203.0.113.20", Feasible: false, ServerNames: []string{"broken.example.com"}},                 // not usable
			{IP: "203.0.113.30", Feasible: true, ServerNames: []string{"www.google.com"}},                      // relays a certificate hosted elsewhere
			{IP: "203.0.113.40", Feasible: true, ServerNames: []string{"*.wild.example.com", "www.apple.com"}}, // wildcard, and a name Xray warns against
			{IP: "203.0.113.50", Feasible: true, Host: "203.0.113.50", ServerNames: []string{"Shop.Example.net"}},
			{IP: "203.0.113.60", Feasible: true, ServerNames: []string{"down.example.net"}}, // next door, but not reachable by name
		},
		unreachable: map[string]bool{"down.example.net": true},
		builtin:     []xui.RealityScan{{Host: "fallback.example.org", Feasible: true}},
		resolves:    map[string]string{"www.google.com": "142.250.1.1", "shop.example.net": "203.0.113.50", "www.apple.com": "203.0.113.40", "down.example.net": "203.0.113.60"},
	}
	for i := 0; i < 20; i++ { // the order is shuffled; the answer must not depend on it
		sni, source, _ := chooseSNI(context.Background(), sc, sc.resolve, "203.0.113.7", nil)
		if sni != "shop.example.net" || source == "" {
			t.Fatalf("picked %q (%s)", sni, source)
		}
	}
	if !strings.HasPrefix(sc.asked[0], "203.0.113.") || !strings.HasSuffix(sc.asked[0], "/26") {
		t.Fatalf("first scan was %q, want a quarter of the server's /24", sc.asked[0])
	}
}

func TestChooseSNIFallsBackToTheBuiltInList(t *testing.T) {
	sc := &fakeScanner{
		near: []xui.RealityScan{{IP: "203.0.113.30", Feasible: true, ServerNames: []string{"www.google.com"}}},
		builtin: []xui.RealityScan{
			{Host: "bad.example.org", Feasible: false},
			{Host: "www.icloud.com", Feasible: true},
			{Host: "www.cloudflare.com", IP: "198.51.100.1", Feasible: true},  // by name
			{Host: "fronted.example.org", IP: "104.18.32.47", Feasible: true}, // by Cloudflare address
			{Host: "one.example.org", IP: "198.51.100.2", Feasible: true}, {Host: "two.example.org", Feasible: true},
		},
		resolves: map[string]string{"www.google.com": "142.250.1.1"},
	}
	got := map[string]bool{}
	for i := 0; i < 60; i++ {
		sni, _, notes := chooseSNI(context.Background(), sc, sc.resolve, "203.0.113.7", nil)
		got[sni] = true
		if i == 0 && !strings.Contains(strings.Join(notes, "\n"), "left out 1 targets served by Cloudflare") {
			t.Fatalf("notes: %v", notes)
		}
	}
	if len(got) != 2 || !got["one.example.org"] || !got["two.example.org"] {
		t.Fatalf("picks %v", got)
	}
	// A target that was tried and did not work is not picked again.
	for i := 0; i < 20; i++ {
		if sni, _, _ := chooseSNI(context.Background(), sc, sc.resolve, "203.0.113.7", map[string]bool{"one.example.org": true}); sni != "two.example.org" {
			t.Fatalf("picked %q although one.example.org was tried", sni)
		}
	}
	if sni, _, _ := chooseSNI(context.Background(), &fakeScanner{}, (&fakeScanner{}).resolve, "203.0.113.7", nil); sni != "" {
		t.Fatalf("nothing usable must give no SNI, got %q", sni)
	}
}

func TestIdentityCoversEveryProtocolFromVersion2(t *testing.T) {
	ss := `{"method":"2022-blake3-aes-256-gcm","password":"U0VSVkVSS0VZU0VSVkVSS0VZU0VSVkVSS0VZU0VSVkU=","network":"tcp,udp","clients":[{"email":"u","password":"Q0xJRU5US0VZQ0xJRU5US0VZQ0xJRU5US0VZQ0xJRU4="}]}`
	snap := &Snapshot{Inbounds: []models.InboundTemplate{
		{ID: 1, Remark: "plain", Protocol: "vless", Port: 80, Enabled: true, Upload: true, Settings: realityClients, StreamSettings: `{"network":"tcp","security":"none"}`},
		{ID: 2, Remark: "ss", Protocol: "shadowsocks", Port: 8388, Enabled: true, Upload: true, Settings: ss, StreamSettings: `{"network":"tcp","security":"none"}`},
	}}
	a, b := &models.Server{IPv4: "203.0.113.7"}, &models.Server{IPv4: "203.0.113.8"}
	newIdentity(a, nil)
	newIdentity(b, nil)
	if a.IdentityVer != identityAll {
		t.Fatalf("version %d", a.IdentityVer)
	}
	la, err := Links(snap.For(a), a.IPv4)
	lb, err2 := Links(snap.For(b), b.IPv4)
	if err != nil || err2 != nil || len(la) != 2 || len(lb) != 2 {
		t.Fatalf("%v %v %v %v", la, lb, err, err2)
	}
	cut := func(u string) string { return u[:strings.Index(u, "@")] } // scheme and credentials
	for i := range la {
		if cut(la[i].URL) == cut(lb[i].URL) {
			t.Fatalf("inbound %d has the same credentials on both servers: %s", i, cut(la[i].URL))
		}
	}
	// Shadowsocks 2022 keys stay valid: base64 of 32 bytes, server and client.
	var got struct {
		Password string `json:"password"`
		Method   string `json:"method"`
		Clients  []struct {
			Password string `json:"password"`
		} `json:"clients"`
	}
	if err := json.Unmarshal([]byte(snap.For(a).Inbounds[1].Settings), &got); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{got.Password, got.Clients[0].Password} {
		raw, err := base64.StdEncoding.DecodeString(k)
		if err != nil || len(raw) != 32 || strings.HasPrefix(k, "U0VS") || strings.HasPrefix(k, "Q0xJ") {
			t.Fatalf("key %q: %v", k, err)
		}
	}
	if got.Password == got.Clients[0].Password || got.Method != "2022-blake3-aes-256-gcm" {
		t.Fatalf("settings %+v", got)
	}

	// A server built with the first identity version keeps the template's
	// values outside Reality, as its panel still has them.
	old := &models.Server{IPv4: "203.0.113.9", IdentitySeed: a.IdentitySeed}
	if in := snap.For(old).Inbounds; in[0].Settings != realityClients || in[1].Settings != ss {
		t.Fatal("version 1 server must not change outside Reality")
	}
}

func TestRealityDead(t *testing.T) {
	snap := identitySnap() // inbound 7 is Reality, 8 is not
	s := &models.Server{IPv4: "203.0.113.7"}
	if !realityDead(snap, s, []uint{8}) {
		t.Fatal("only the plain inbound passed: Reality is dead")
	}
	if realityDead(snap, s, []uint{7}) {
		t.Fatal("the Reality inbound passed")
	}
	if realityDead(&Snapshot{Inbounds: snap.Inbounds[1:]}, s, nil) {
		t.Fatal("a server without Reality inbounds has nothing to retry")
	}
}
