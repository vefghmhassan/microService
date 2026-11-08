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
}

// testLink is one client config to test.
type testLink struct {
	name, url, via string
	fragment       bool
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
		out = append(out, testLink{name: t.Remark, url: ls[0].URL, via: via})
		if t.Fragment && linkgen.Security(t.StreamSettings) == "tls" && t.Protocol != "hysteria" {
			out = append(out, testLink{name: t.Remark + " · Fragment", url: ls[0].URL, via: via, fragment: true})
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
			out = append(out, testLink{name: t.Remark + " · " + snap.Clean[i%len(snap.Clean)].Address, url: l.URL, via: ViaClean})
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
	j, err := linkgen.ClientJSONWith(l.url, linkgen.JSONOptions{Fragment: l.fragment})
	if err != nil {
		return nil, nil, err
	}
	var cfg struct {
		Outbounds []map[string]any `json:"outbounds"`
	}
	if err := json.Unmarshal([]byte(j), &cfg); err != nil || len(cfg.Outbounds) == 0 {
		return nil, nil, errors.New("no proxy outbound")
	}
	for _, o := range cfg.Outbounds[1:] {
		if o["tag"] == "fragment" {
			fragment = o
		}
	}
	o := cfg.Outbounds[0]
	o["tag"] = tag
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
			res = append(res, ConfigTest{Name: l.name, Via: l.via, Error: err.Error()})
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
		res = append(res, ConfigTest{Name: tested[i].name, Via: tested[i].via, OK: g.OK, MS: g.MS, Error: g.Error})
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

// autoTestConfigs runs the config test at the end of an install, as the
// reference script does, and logs OK/FAIL per config. A failure is only a
// warning: a fresh orange name can take a few minutes to resolve.
func (o *Orchestrator) autoTestConfigs(ctx context.Context, r *jobs.Run, s *models.Server) {
	r.Step("test every config")
	snap, err := LoadSnapshot(o.DB)
	if err != nil {
		r.Logf("warn", "config test: %v", err)
		return
	}
	ns := snap.For(s)
	if ns.CDNHost != "" {
		ns.Clean, _ = cleanip.Pick(o.DB, 2)
	}
	remote, _, err := o.Connect(ctx, s)
	if err != nil {
		r.Logf("warn", "config test: ssh: %v", err)
		return
	}
	defer remote.Close()
	res, err := TestConfigs(ctx, remote, ns, s)
	if err != nil {
		r.Logf("warn", "config test: %v", err)
		return
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
}
