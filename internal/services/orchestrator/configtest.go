package orchestrator

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"strconv"
	"strings"
	"time"

	"microserver/internal/jobs"
	"microserver/internal/models"
	"microserver/internal/services/cleanip"
	"microserver/internal/services/linkgen"
	"microserver/internal/services/sshx"
)

// Ways a config reaches the server in a config test.
const (
	ViaDirect = "direct" // straight to the server (dialled on 127.0.0.1 from the server itself)
	ViaCDN    = "cdn"    // through Cloudflare, by the server's proxied hostname
	ViaClean  = "clean"  // through Cloudflare, by a clean edge IP
)

// ConfigTest is the result for one client config of a server.
type ConfigTest struct {
	Name  string `json:"name"`
	Via   string `json:"via"`
	OK    bool   `json:"ok"`
	MS    int    `json:"ms"`
	Error string `json:"error,omitempty"`
	// InboundID is the template inbound the config belongs to. Variant is
	// empty for the inbound's own config, or names an extra one built from it.
	InboundID uint   `json:"inbound_id,omitempty"`
	Variant   string `json:"variant,omitempty"`
}

// Extra configs tested for an inbound besides its own.
const (
	VariantFragment = "fragment"
	VariantClean    = "clean"
)

// testLink is one client config to test.
type testLink struct {
	name, url, via string
	fragment       bool
	inbound        uint
	variant        string
}

func (l testLink) result() ConfigTest {
	return ConfigTest{Name: l.name, Via: l.via, InboundID: l.inbound, Variant: l.variant}
}

// passedInbounds are the inbounds whose own config works. The Fragment and
// clean-IP variants do not count either way: they are extras of an inbound,
// and a clean address failing says nothing about the server.
func passedInbounds(res []ConfigTest) map[uint]bool {
	out := map[uint]bool{}
	for _, t := range res {
		if t.OK && t.Variant == "" && t.InboundID != 0 {
			out[t.InboundID] = true
		}
	}
	return out
}

// configTestLinks are the links a config test tries on a server: the first
// client of every inbound it has (snap is already narrowed by For), the
// Fragment variant of those that ask for one, and the first clean-IP CDN
// inbound once per address in snap.Clean.
func configTestLinks(snap *Snapshot, s *models.Server) []testLink {
	var out []testLink
	cleanDone := false
	for _, t := range snap.Inbounds {
		if !t.Enabled {
			continue
		}
		addr, sni := snap.target(t, LinkHost(s))
		ls, err := linkgen.LinksVia(t.Protocol, t.Settings, t.StreamSettings, addr, sni, t.Port, t.Remark)
		if err != nil || len(ls) == 0 {
			continue
		}
		via := ViaDirect
		if (t.AddonID != 0 && t.Route == models.RouteCDN) || (t.AddonID == 0 && t.CDN && s.DNSProxied) {
			via = ViaCDN
		}
		out = append(out, testLink{name: t.Remark, url: ls[0].URL, via: via, inbound: t.ID})
		if t.Fragment && linkgen.Security(t.StreamSettings) == "tls" && t.Protocol != "hysteria" {
			out = append(out, testLink{name: t.Remark + " · Fragment", url: ls[0].URL, via: via, fragment: true, inbound: t.ID, variant: VariantFragment})
		}
		if cleanDone {
			continue
		}
		cl, err := snap.cleanLinks(t)
		if err != nil || len(cl) == 0 {
			continue
		}
		cleanDone = true
		for i, l := range cl {
			out = append(out, testLink{name: t.Remark + " · " + snap.Clean[i%len(snap.Clean)].Address, url: l.URL, via: ViaClean, inbound: t.ID, variant: VariantClean})
		}
	}
	return out
}

// clientOutbound turns a link into the client's proxy outbound (and the
// fragment outbound it dials through, if any). A direct config dials
// 127.0.0.1: the test runs on the server itself, and many hosts cannot reach
// their own public address. CDN configs keep their address so they really
// cross Cloudflare.
func clientOutbound(l testLink, tag string) (proxy, fragment map[string]any, err error) {
	o, frag, _, err := linkgen.OutboundFromLink(l.url, tag, linkgen.JSONOptions{Fragment: l.fragment})
	if err != nil {
		return nil, nil, err
	}
	if frag {
		fragment = linkgen.FragmentOutbound()
	}
	delete(o, "mux")
	if l.via == ViaDirect {
		st, _ := o["settings"].(map[string]any)
		for _, k := range []string{"vnext", "servers"} {
			if arr, _ := st[k].([]any); len(arr) > 0 {
				if srv, _ := arr[0].(map[string]any); srv != nil {
					srv["address"] = "127.0.0.1"
				}
			}
		}
		if _, ok := st["address"]; ok {
			st["address"] = "127.0.0.1"
		}
	}
	return o, fragment, nil
}

// TestConfigs runs every client config of s from the server itself, the way
// the reference script checks its work: xray with each config as an
// outbound behind its own socks port, and a request to generate_204 through
// each. snap is narrowed to s by For, with Clean set to the clean IPs to try
// the first clean-IP CDN inbound through.
func TestConfigs(ctx context.Context, remote Remote, snap *Snapshot, s *models.Server) ([]ConfigTest, error) {
	links := configTestLinks(snap, s)
	if len(links) == 0 {
		return nil, errors.New("this server has no config to test")
	}
	var res []ConfigTest
	var targets, all []map[string]any
	var tested []testLink
	var frag map[string]any
	for _, l := range links {
		o, f, err := clientOutbound(l, fmt.Sprintf("c%d", len(targets)))
		if err != nil {
			ct := l.result()
			ct.Error = err.Error()
			res = append(res, ct)
			continue
		}
		if f != nil {
			frag = f
		}
		targets = append(targets, o)
		tested = append(tested, l)
	}
	if len(targets) == 0 {
		return res, nil
	}
	all = append(all, targets...)
	if frag != nil {
		all = append(all, frag)
	}
	script, err := proxyScript(TestReal, all, targets, 20000+rand.IntN(30000))
	if err != nil {
		return nil, err
	}
	out, err := remote.Run(ctx, "echo "+base64.StdEncoding.EncodeToString([]byte(script))+" | base64 -d | bash", nil)
	if err != nil && out == "" {
		return nil, err
	}
	got, err := parseTestOutput(TestReal, out, targets)
	if err != nil {
		return nil, err
	}
	for i, g := range got {
		ct := tested[i].result()
		ct.OK, ct.MS, ct.Error = g.OK, g.MS, g.Error
		res = append(res, ct)
	}
	return res, nil
}

// CleanCDNTest is one clean address tried through the server's own CDN
// hostname.
type CleanCDNTest struct {
	Address string `json:"address"`
	OK      bool   `json:"ok"`
	MS      int    `json:"ms"`
	Error   string `json:"error,omitempty"`
}

// cleanCDNScript asks Cloudflare, at each address, for https://host:port/:
// only a real Cloudflare edge serving our zone completes the TLS handshake
// for host and answers with an HTTP status (whatever the inbound returns).
// Domains are resolved to their first IPv4 first. Output: "R <i> <code> <s>".
func cleanCDNScript(host string, port int, addrs []string) string {
	var b strings.Builder
	for i, a := range addrs {
		ip := sshx.ShellQuote(a)
		if net.ParseIP(a) == nil {
			ip = fmt.Sprintf(`"$(getent ahostsv4 %s | awk 'NR==1{print $1}')"`, sshx.ShellQuote(a))
		}
		fmt.Fprintf(&b, `( i=%s; [ -z "$i" ] && { echo "R %d 000 0"; exit; }; r=$(curl -s -o /dev/null -m 8 -w '%%{http_code} %%{time_connect}' --resolve %s:%d:"$i" https://%s:%d/); echo "R %d $r" ) &`+"\n",
			ip, i, host, port, host, port, i)
	}
	b.WriteString("wait\n")
	return b.String()
}

// TestCleanViaCDN tries each clean address through the CDN hostname of s on
// port, from the server, as the script's clean-IP check does.
func TestCleanViaCDN(ctx context.Context, remote Remote, s *models.Server, port int, addrs []string) ([]CleanCDNTest, error) {
	if s.CDNHostname == "" {
		return nil, errors.New("this server has no proxied (CDN) hostname")
	}
	if !safeHost.MatchString(s.CDNHostname) {
		return nil, errors.New("invalid CDN hostname")
	}
	var ok []string
	for _, a := range addrs {
		if safeHost.MatchString(a) && !strings.Contains(a, ":") {
			ok = append(ok, a)
		}
	}
	if len(ok) == 0 {
		return nil, errors.New("no IPv4 address or domain to test")
	}
	out, err := remote.Run(ctx, "bash -c "+sshx.ShellQuote(cleanCDNScript(s.CDNHostname, port, ok)), nil)
	if err != nil && out == "" {
		return nil, err
	}
	res := make([]CleanCDNTest, len(ok))
	for i, a := range ok {
		res[i] = CleanCDNTest{Address: a, Error: "بدون پاسخ"}
	}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 4 || f[0] != "R" {
			continue
		}
		i, err := strconv.Atoi(f[1])
		if err != nil || i < 0 || i >= len(res) {
			continue
		}
		secs, _ := strconv.ParseFloat(f[3], 64)
		res[i].MS = int(secs * 1000)
		if f[2] == "000" {
			res[i].Error = "از این آدرس به " + s.CDNHostname + " نرسید"
		} else {
			res[i].OK, res[i].Error = true, ""
		}
	}
	return res, nil
}

// CDNPort is the port a server's CDN inbounds are served on through
// Cloudflare (snap narrowed by For): the first enabled TLS inbound on the
// orange name, else 443.
func CDNPort(snap *Snapshot) int {
	for _, t := range snap.Inbounds {
		if !t.Enabled || linkgen.Security(t.StreamSettings) != "tls" {
			continue
		}
		if (t.AddonID != 0 && t.Route == models.RouteCDN) || (t.AddonID == 0 && t.CDN) {
			return t.Port
		}
	}
	return 443
}

// hostTuningScript opens the inbounds' ports when ufw is active (TCP, UDP
// for Hysteria2, both for Shadowsocks) and turns on BBR once, like the
// reference script. Cloud images usually run without ufw; then it only
// does BBR.
func hostTuningScript(snap *Snapshot) string {
	var b strings.Builder
	b.WriteString("if command -v ufw >/dev/null && ufw status 2>/dev/null | grep -q 'Status: active'; then\n  :\n")
	for _, t := range snap.Inbounds {
		if !t.Enabled {
			continue
		}
		switch {
		case t.Protocol == "hysteria":
			fmt.Fprintf(&b, "  ufw allow %d/udp >/dev/null\n", t.Port)
		case t.Protocol == "shadowsocks":
			fmt.Fprintf(&b, "  ufw allow %d/tcp >/dev/null; ufw allow %d/udp >/dev/null\n", t.Port, t.Port)
		default:
			fmt.Fprintf(&b, "  ufw allow %d/tcp >/dev/null\n", t.Port)
		}
	}
	b.WriteString("fi\n")
	b.WriteString(`if [ "$(sysctl -n net.ipv4.tcp_congestion_control 2>/dev/null)" != bbr ]; then
  printf 'net.core.default_qdisc=fq\nnet.ipv4.tcp_congestion_control=bbr\n' > /etc/sysctl.d/99-bbr.conf
  modprobe tcp_bbr 2>/dev/null; sysctl --system >/dev/null 2>&1
fi
true
`)
	return b.String()
}

// The config test of a new server (all shortened by tests): each round is
// bounded, so a server that stopped answering cannot hold the install open,
// and failures are tried again because a fresh orange name can take a few
// minutes to resolve.
var (
	autoTestTimeout = 2 * time.Minute
	autoTestRounds  = 3
	autoTestGap     = 30 * time.Second
)

// gateResult is what the config test of a server decided: which inbounds may
// go to vpn-pannel.
type gateResult struct {
	Passed []uint   `json:"passed"`
	Total  int      `json:"total"`  // inbounds tested; 0 when there was nothing to test
	Failed []string `json:"failed"` // remarks of the inbounds that do not work
}

// none reports a server on which not a single config works.
func (g gateResult) none() bool { return g.Total > 0 && len(g.Passed) == 0 }

// only is the upload filter: nil (everything) when nothing was tested.
func (g gateResult) only() map[uint]bool {
	if g.Total == 0 {
		return nil
	}
	out := map[uint]bool{}
	for _, id := range g.Passed {
		out[id] = true
	}
	return out
}

// gate runs the config test of a server once per job (a resumed job reuses
// the stored result) and reports which inbounds work.
func (o *Orchestrator) gate(ctx context.Context, r *jobs.Run, s *models.Server) (gateResult, error) {
	var g gateResult
	if raw := r.GetString("gate"); raw != "" && json.Unmarshal([]byte(raw), &g) == nil {
		return g, nil
	}
	g = o.testNewServer(ctx, r, s)
	// A canceled job must not act on a test that was cut short.
	if err := ctx.Err(); err != nil {
		return g, err
	}
	b, _ := json.Marshal(g)
	r.Set("gate", string(b))
	return g, nil
}

// testNewServer runs every client config of s from the server itself, as the
// reference script checks its work, and logs OK/FAIL per config. A test that
// cannot run at all counts as nothing working.
func (o *Orchestrator) testNewServer(ctx context.Context, r *jobs.Run, s *models.Server) gateResult {
	r.Step("test every config")
	snap, err := LoadSnapshot(o.DB)
	if err != nil {
		r.Logf("warn", "config test: %v", err)
		return gateResult{}
	}
	ns := snap.For(s)
	if ns.CDNHost != "" {
		ns.Clean, _ = cleanip.Pick(o.DB, 2)
	}
	names := map[uint]string{}
	var order []uint
	for _, l := range configTestLinks(ns, s) {
		if l.variant == "" {
			names[l.inbound] = l.name
			order = append(order, l.inbound)
		}
	}
	if len(order) == 0 {
		r.Info("this server has no config to test")
		return gateResult{}
	}

	once := func() ([]ConfigTest, error) {
		tctx, cancel := context.WithTimeout(ctx, autoTestTimeout)
		defer cancel()
		remote, _, err := o.Connect(tctx, s)
		if err != nil {
			return nil, fmt.Errorf("ssh: %w", err)
		}
		defer remote.Close()
		return TestConfigs(tctx, remote, ns, s)
	}
	var res []ConfigTest
	for round := 1; ; round++ {
		res, err = once()
		failed := err != nil
		for _, t := range res {
			failed = failed || !t.OK
		}
		if !failed || round >= autoTestRounds || ctx.Err() != nil {
			break
		}
		if err != nil {
			r.Logf("warn", "config test (round %d of %d): %v", round, autoTestRounds, err)
		} else {
			r.Info("round %d of %d: not every config works yet; trying again in %s", round, autoTestRounds, autoTestGap)
		}
		if sleep(ctx, autoTestGap) != nil {
			break
		}
	}
	if err != nil {
		r.Logf("warn", "config test: %v", err)
		res = nil
	}
	ok := 0
	for _, t := range res {
		if t.OK {
			ok++
			r.Info("OK   %s (%s, %dms)", t.Name, t.Via, t.MS)
		} else {
			r.Logf("warn", "FAIL %s (%s): %s", t.Name, t.Via, t.Error)
		}
	}
	r.Info("%d/%d configs work", ok, len(res))

	passed := passedInbounds(res)
	g := gateResult{Total: len(order)}
	for _, id := range order {
		if passed[id] {
			g.Passed = append(g.Passed, id)
		} else {
			g.Failed = append(g.Failed, names[id])
		}
	}
	return g
}
