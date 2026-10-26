package orchestrator

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net"
	"regexp"
	"strconv"
	"strings"
)

// Outbound test modes, as in 3x-ui's outbound tester.
const (
	TestTCP  = "tcp"  // TCP connect time from the server to the outbound's address
	TestReal = "real" // full request through the outbound (generate_204)
	TestHTTP = "http" // request through the outbound to a GeoIP API: exit IP and country
)

// OutboundTest is the result for one outbound.
type OutboundTest struct {
	Tag         string `json:"tag"`
	OK          bool   `json:"ok"`
	Skipped     bool   `json:"skipped,omitempty"`
	MS          int    `json:"ms"`
	Error       string `json:"error,omitempty"`
	IP          string `json:"ip,omitempty"`
	Country     string `json:"country,omitempty"`
	CountryCode string `json:"country_code,omitempty"`
	City        string `json:"city,omitempty"`
	ISP         string `json:"isp,omitempty"`
}

const (
	realURL = "https://www.gstatic.com/generate_204"
	httpURL = "http://ip-api.com/json?fields=status,message,query,country,countryCode,city,isp"
)

// untestable outbounds carry no traffic to a destination.
var untestable = map[string]bool{"blackhole": true, "dns": true, "loopback": true}

// TestOutbounds tests the outbounds named by tags (all when empty) from the
// server behind remote, all at once. outbounds is the whole list so chained
// outbounds (proxySettings, dialerProxy) resolve.
func TestOutbounds(ctx context.Context, remote Remote, mode string, outbounds []map[string]any, tags []string) ([]OutboundTest, error) {
	want := map[string]bool{}
	for _, t := range tags {
		want[t] = true
	}
	var targets []map[string]any
	var res []OutboundTest
	for _, o := range outbounds {
		tag, _ := o["tag"].(string)
		if tag == "" || (len(want) > 0 && !want[tag]) {
			continue
		}
		proto, _ := o["protocol"].(string)
		if untestable[proto] {
			res = append(res, OutboundTest{Tag: tag, Skipped: true, Error: proto + " قابل تست نیست"})
			continue
		}
		targets = append(targets, o)
	}
	if len(targets) == 0 {
		return res, nil
	}
	var script string
	var err error
	switch mode {
	case TestTCP:
		script, err = tcpScript(targets)
	case TestReal, TestHTTP:
		script, err = proxyScript(mode, outbounds, targets, 20000+rand.IntN(30000))
	default:
		return nil, fmt.Errorf("unknown test mode %q", mode)
	}
	if err != nil {
		return nil, err
	}
	out, err := remote.Run(ctx, "echo "+base64.StdEncoding.EncodeToString([]byte(script))+" | base64 -d | bash", nil)
	if err != nil && out == "" {
		return nil, err
	}
	got, err := parseTestOutput(mode, out, targets)
	if err != nil {
		return nil, err
	}
	return append(got, res...), nil
}

var safeHost = regexp.MustCompile(`^[A-Za-z0-9.\-:]+$`)

// endpoint is the address an outbound connects to first.
func endpoint(o map[string]any) (string, int) {
	st, _ := o["settings"].(map[string]any)
	for _, k := range []string{"vnext", "servers"} {
		if arr, _ := st[k].([]any); len(arr) > 0 {
			if s, _ := arr[0].(map[string]any); s != nil {
				host, _ := s["address"].(string)
				port, _ := s["port"].(float64)
				return host, int(port)
			}
		}
	}
	if peers, _ := st["peers"].([]any); len(peers) > 0 {
		if p, _ := peers[0].(map[string]any); p != nil {
			ep, _ := p["endpoint"].(string)
			if h, ps, err := net.SplitHostPort(ep); err == nil {
				n, _ := strconv.Atoi(ps)
				return h, n
			}
		}
	}
	return "", 0
}

// tcpScript times a TCP connect to each outbound's address, in parallel.
// Output: "R <i> ok <ms>" or "R <i> fail <reason>".
func tcpScript(targets []map[string]any) (string, error) {
	var b strings.Builder
	for i, o := range targets {
		host, port := endpoint(o)
		if host == "" || port == 0 {
			fmt.Fprintf(&b, "echo 'R %d fail بدون آدرس مقصد (مثلاً freedom)'\n", i)
			continue
		}
		if !safeHost.MatchString(host) {
			fmt.Fprintf(&b, "echo 'R %d fail آدرس نامعتبر'\n", i)
			continue
		}
		fmt.Fprintf(&b, `( s=$(date +%%s%%N); if timeout 3 bash -c 'exec 3<>/dev/tcp/%s/%d' 2>/dev/null; then e=$(date +%%s%%N); echo "R %d ok $(( (e-s)/1000000 ))"; else echo "R %d fail timeout"; fi ) &`+"\n", host, port, i, i)
	}
	b.WriteString("wait\n")
	return b.String(), nil
}

// testConfig is a throwaway xray config: every outbound, plus one socks
// inbound per target routed to it.
func testConfig(all, targets []map[string]any, base int) map[string]any {
	ins := make([]any, 0, len(targets))
	rules := make([]any, 0, len(targets))
	for i, o := range targets {
		in := fmt.Sprintf("t%d", i)
		ins = append(ins, map[string]any{"tag": in, "listen": "127.0.0.1", "port": base + i, "protocol": "socks", "settings": map[string]any{"udp": false}})
		rules = append(rules, map[string]any{"type": "field", "inboundTag": []string{in}, "outboundTag": o["tag"]})
	}
	outs := make([]any, len(all))
	for i, o := range all {
		outs[i] = o
	}
	return map[string]any{
		"log":       map[string]any{"loglevel": "warning"},
		"inbounds":  ins,
		"outbounds": outs,
		"routing":   map[string]any{"domainStrategy": "AsIs", "rules": rules},
	}
}

// proxyScript runs the server's xray with testConfig and requests a URL
// through each socks port at once. Output: "R <i> <code> <seconds> <b64 body>",
// or "ERR <message>" when xray could not start.
func proxyScript(mode string, all, targets []map[string]any, base int) (string, error) {
	cfg, err := json.Marshal(testConfig(all, targets, base))
	if err != nil {
		return "", err
	}
	target := realURL
	if mode == TestHTTP {
		target = httpURL
	}
	var b strings.Builder
	b.WriteString(`D=$(mktemp -d); P=
trap '[ -n "$P" ] && kill $P 2>/dev/null; rm -rf "$D"' EXIT
X=$(ls /usr/local/x-ui/bin/xray-linux-* 2>/dev/null | head -n1)
[ -x "$X" ] || { echo "ERR xray binary not found in /usr/local/x-ui/bin"; exit 0; }
echo ` + base64.StdEncoding.EncodeToString(cfg) + ` | base64 -d > "$D/c.json"
XRAY_LOCATION_ASSET=/usr/local/x-ui/bin "$X" run -c "$D/c.json" > "$D/log" 2>&1 & P=$!
sleep 1.5
kill -0 $P 2>/dev/null || { echo "ERR $(tail -c 400 "$D/log" | tr '\n' ' ')"; exit 0; }
`)
	for i := range targets {
		fmt.Fprintf(&b, `( r=$(curl -s -o "$D/b%d" -w '%%{http_code} %%{time_total}' --max-time 10 -x socks5h://127.0.0.1:%d '%s'); echo "R %d $r $(head -c 600 "$D/b%d" 2>/dev/null | base64 | tr -d '\n')" ) &`+"\n", i, base+i, target, i, i)
	}
	b.WriteString("wait\n")
	return b.String(), nil
}

func parseTestOutput(mode, out string, targets []map[string]any) ([]OutboundTest, error) {
	res := make([]OutboundTest, len(targets))
	for i, o := range targets {
		res[i].Tag, _ = o["tag"].(string)
		res[i].Error = "بدون پاسخ"
	}
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if msg, ok := strings.CutPrefix(line, "ERR "); ok {
			return nil, fmt.Errorf("xray: %s", strings.TrimSpace(msg))
		}
		f := strings.Fields(line)
		if len(f) < 3 || f[0] != "R" {
			continue
		}
		i, err := strconv.Atoi(f[1])
		if err != nil || i < 0 || i >= len(res) {
			continue
		}
		r := &res[i]
		if mode == TestTCP {
			if f[2] == "ok" && len(f) >= 4 {
				r.MS, _ = strconv.Atoi(f[3])
				r.OK, r.Error = true, ""
			} else {
				r.Error = strings.Join(f[3:], " ")
			}
			continue
		}
		code := f[2]
		secs := 0.0
		if len(f) >= 4 {
			secs, _ = strconv.ParseFloat(f[3], 64)
		}
		r.MS = int(secs * 1000)
		var body []byte
		if len(f) >= 5 {
			body, _ = base64.StdEncoding.DecodeString(f[4])
		}
		switch {
		case code == "000":
			r.Error = "اتصال برقرار نشد یا زمان تمام شد"
		case mode == TestReal && (code == "204" || code == "200"):
			r.OK, r.Error = true, ""
		case mode == TestReal:
			r.Error = "HTTP " + code
		default:
			var g struct {
				Status, Message, Query, Country, CountryCode, City, ISP string
			}
			if json.Unmarshal(body, &g) != nil || g.Status != "success" {
				r.Error = strings.TrimSpace("HTTP " + code + " " + g.Message)
				continue
			}
			r.OK, r.Error = true, ""
			r.IP, r.Country, r.CountryCode, r.City, r.ISP = g.Query, g.Country, g.CountryCode, g.City, g.ISP
		}
	}
	return res, sc.Err()
}
