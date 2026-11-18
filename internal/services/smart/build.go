// Package smart builds "smart" (intelligent) client configs: one xray JSON
// holding many proxy outbounds (proxy-1..N) that an observatory probes and a
// balancer picks from, with routing rules for well known services.
package smart

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"microserver/internal/models"
	"microserver/internal/services/linkgen"
)

// Fixed tags of a smart config.
const (
	ProxyPrefix = "proxy-"
	BalancerTag = "proxy-round"
)

// Defaults filled in by Normalize.
const (
	DefaultProbeURL      = "https://www.gstatic.com/generate_204"
	DefaultProbeInterval = "1m"
	DefaultTimeout       = "5s"
	DefaultSampling      = 3
	DefaultMaxRTT        = "1500ms"
)

// Item is one share link that becomes a proxy outbound.
type Item struct {
	URL string
	// Label says where it came from (server name · inbound), for messages.
	Label string
}

// Result is a built config.
type Result struct {
	JSON      string
	Outbounds int
	// Skipped lists the links that could not be turned into outbounds.
	Skipped []string
}

// Normalize fills empty profile fields with defaults and lower-cases the
// enumerations; Validate then checks what is left.
func Normalize(p *models.SmartProfile) {
	p.Name = strings.TrimSpace(p.Name)
	if p.Scope != models.SmartScopeAll {
		p.Scope = models.SmartScopeServer
	}
	switch p.Family {
	case models.FamilyV4, models.FamilyV6, models.FamilyBoth:
	default:
		p.Family = models.FamilyBoth
	}
	if p.ObsKind != "burst" {
		p.ObsKind = "observatory"
	}
	if strings.TrimSpace(p.ProbeURL) == "" {
		p.ProbeURL = DefaultProbeURL
	}
	if strings.TrimSpace(p.ProbeInterval) == "" {
		p.ProbeInterval = DefaultProbeInterval
	}
	if strings.TrimSpace(p.Timeout) == "" {
		p.Timeout = DefaultTimeout
	}
	if p.Sampling <= 0 {
		p.Sampling = DefaultSampling
	}
	if p.HTTPMethod != "GET" {
		p.HTTPMethod = "HEAD"
	}
	switch p.Strategy {
	case "leastPing", "leastLoad", "random", "roundRobin":
	default:
		p.Strategy = "leastPing"
	}
	if strings.TrimSpace(p.MaxRTT) == "" {
		p.MaxRTT = DefaultMaxRTT
	}
	if p.DNSPreset != "simple" {
		p.DNSPreset = "smart"
	}
	p.Country = strings.ToUpper(strings.TrimSpace(p.Country))
	p.Countries = strings.ToUpper(strings.ReplaceAll(p.Countries, " ", ""))
	p.NameTemplate = strings.TrimSpace(p.NameTemplate)
	if p.Scope == models.SmartScopeAll && (strings.Contains(p.NameTemplate, "{base}") || strings.Contains(p.NameTemplate, "{server}")) {
		// Per-server placeholders mean nothing for one config of every server.
		p.NameTemplate = ""
	}
	if p.NameTemplate == "" {
		if p.Scope == models.SmartScopeAll {
			p.NameTemplate = "{name}"
		} else {
			p.NameTemplate = "{base}-smart"
		}
	}
}

// Validate reports what is wrong with a normalized profile, in Persian
// (the dashboard shows it as is).
func Validate(p *models.SmartProfile) error {
	if p.Name == "" {
		return errors.New("نام پروفایل لازم است")
	}
	if !strings.HasPrefix(p.ProbeURL, "http://") && !strings.HasPrefix(p.ProbeURL, "https://") {
		return errors.New("آدرس تست باید با http:// یا https:// شروع شود")
	}
	for name, v := range map[string]string{"فاصلهٔ تست": p.ProbeInterval, "مهلت تست": p.Timeout} {
		if d, err := time.ParseDuration(v); err != nil || d <= 0 {
			return fmt.Errorf("%s باید مدت زمان معتبر باشد (مثل 10s یا 1m)", name)
		}
	}
	if d, err := time.ParseDuration(p.MaxRTT); err != nil || d < 0 {
		return errors.New("maxRTT باید مدت زمان معتبر باشد (مثل 1500ms)")
	}
	if p.Expected < 0 {
		return errors.New("expected نمی‌تواند منفی باشد")
	}
	if p.MaxOutbounds < 0 {
		return errors.New("سقف outbound نمی‌تواند منفی باشد")
	}
	if p.Scope == models.SmartScopeServer && !strings.Contains(p.NameTemplate, "{base}") && !strings.Contains(p.NameTemplate, "{server}") {
		return errors.New("در حالت «هر سرور» قالب نام باید {base} یا {server} داشته باشد تا نام‌ها تکراری نشوند")
	}
	if p.Country != "" && len(p.Country) != 2 {
		return errors.New("کد کشور باید دو حرفی باشد")
	}
	return nil
}

// Build turns the profile and links into one compact client config named
// name. rules are the routing rules sent through the balancer, in order.
func Build(p models.SmartProfile, rules []models.SmartRule, items []Item, name string) (*Result, error) {
	Normalize(&p)
	if err := Validate(&p); err != nil {
		return nil, err
	}
	res := &Result{}
	var proxies []any
	fragment := false
	seen := map[string]bool{}
	for _, it := range items {
		if p.MaxOutbounds > 0 && len(proxies) >= p.MaxOutbounds {
			res.Skipped = append(res.Skipped, it.Label+": over the outbound cap")
			continue
		}
		u := strings.TrimSpace(it.URL)
		if u == "" || seen[u] {
			continue
		}
		seen[u] = true
		tag := fmt.Sprintf("%s%d", ProxyPrefix, len(proxies)+1)
		o, frag, _, err := linkgen.OutboundFromLink(u, tag, linkgen.JSONOptions{Fragment: p.Fragment})
		if err != nil {
			res.Skipped = append(res.Skipped, fmt.Sprintf("%s: %v", it.Label, err))
			continue
		}
		fragment = fragment || frag
		proxies = append(proxies, o)
	}
	if len(proxies) == 0 {
		return res, errors.New("no usable link for this smart config")
	}
	res.Outbounds = len(proxies)

	outbounds := append([]any{}, proxies...)
	outbounds = append(outbounds,
		obj{"tag": "direct", "protocol": "freedom", "settings": obj{"domainStrategy": "UseIP"}},
		obj{"tag": "block", "protocol": "blackhole", "settings": obj{"response": obj{"type": "http"}}},
	)
	if fragment {
		outbounds = append(outbounds, linkgen.FragmentOutbound())
	}

	cfg := obj{
		"remarks":   name,
		"log":       obj{"loglevel": "warning"},
		"dns":       dnsBlock(p),
		"inbounds":  inbounds(),
		"outbounds": outbounds,
		"routing":   routing(p, rules),
		"policy": obj{
			"levels": obj{"8": obj{"connIdle": 300, "downlinkOnly": 1, "handshake": 4, "uplinkOnly": 1}},
			"system": obj{"statsOutboundDownlink": true, "statsOutboundUplink": true},
		},
		"stats": obj{},
	}
	sel := []string{ProxyPrefix}
	if p.ObsKind == "burst" {
		cfg["burstObservatory"] = obj{
			"subjectSelector": sel,
			"pingConfig": obj{
				"destination": p.ProbeURL, "interval": p.ProbeInterval, "timeout": p.Timeout,
				"sampling": p.Sampling, "httpMethod": p.HTTPMethod,
			},
		}
	} else {
		cfg["observatory"] = obj{
			"subjectSelector": sel, "probeUrl": p.ProbeURL, "probeInterval": p.ProbeInterval, "enableConcurrency": true,
		}
	}
	j, err := encode(cfg)
	if err != nil {
		return nil, err
	}
	res.JSON = j
	return res, nil
}

type obj = map[string]any

func inbounds() []any {
	return []any{
		obj{
			"tag": "socks", "listen": "127.0.0.1", "port": 10808, "protocol": "socks",
			"settings": obj{"auth": "noauth", "udp": true, "userLevel": 8},
			"sniffing": obj{"enabled": true, "destOverride": []string{"http", "tls"}, "routeOnly": false},
		},
		obj{"tag": "http", "listen": "127.0.0.1", "port": 10809, "protocol": "http", "settings": obj{"userLevel": 8}},
	}
}

var dnsHosts = obj{
	"domain:googleapis.cn": "googleapis.com",
	"dns.google":           []string{"8.8.8.8", "8.8.4.4", "2001:4860:4860::8888", "2001:4860:4860::8844"},
	"one.one.one.one":      []string{"1.1.1.1", "1.0.0.1", "2606:4700:4700::1111", "2606:4700:4700::1001"},
	"dns.quad9.net":        []string{"9.9.9.9", "149.112.112.112", "2620:fe::fe", "2620:fe::9"},
}

func dnsBlock(p models.SmartProfile) obj {
	if p.DNSPreset == "simple" {
		return obj{"hosts": obj{"domain:googleapis.cn": "googleapis.com"}, "servers": []string{"1.1.1.1"}}
	}
	return obj{
		"hosts":   dnsHosts,
		"servers": []any{"1.1.1.1", "8.8.8.8"},
		"tag":     "dns-module",
	}
}

func routing(p models.SmartProfile, rules []models.SmartRule) obj {
	viaBalancer := func(r obj) obj {
		r["type"], r["balancerTag"] = "field", BalancerTag
		return r
	}
	to := func(r obj, tag string) obj {
		r["type"], r["outboundTag"] = "field", tag
		return r
	}
	var rs []any
	if p.DNSPreset != "simple" {
		rs = append(rs, viaBalancer(obj{"inboundTag": []string{"dns-module"}}))
	}
	if p.BlockUDP443 {
		rs = append(rs, to(obj{"network": "udp", "port": "443"}, "block"))
	}
	rs = append(rs,
		to(obj{"ip": []string{"geoip:private"}}, "direct"),
		to(obj{"domain": []string{"geosite:private"}}, "direct"),
	)
	if p.BlockAds {
		rs = append(rs, to(obj{"domain": []string{"geosite:category-ads-all"}}, "block"))
	}
	if p.IranDirect {
		rs = append(rs,
			to(obj{"domain": []string{"geosite:category-ir", "regexp:.*\\.ir$"}}, "direct"),
			to(obj{"ip": []string{"geoip:ir"}}, "direct"),
		)
	}
	for _, r := range rules {
		rs = append(rs, ruleObjs(r, viaBalancer)...)
	}
	rs = append(rs, viaBalancer(obj{"network": "tcp,udp"}))

	strategy := obj{"type": p.Strategy}
	if p.Strategy == "leastLoad" {
		strategy["settings"] = obj{"expected": p.Expected, "maxRTT": p.MaxRTT}
	}
	return obj{
		"domainStrategy": "IPIfNonMatch",
		"balancers": []any{obj{
			"tag": BalancerTag, "selector": []string{ProxyPrefix}, "strategy": strategy,
			"fallbackTag": ProxyPrefix + "1",
		}},
		"rules": rs,
	}
}

// ruleObjs turns a SmartRule into xray rules: domains and IPs are matched
// separately (one xray rule needs every field it lists to match).
func ruleObjs(r models.SmartRule, wrap func(obj) obj) []any {
	extra := func(o obj) obj {
		if v := strings.TrimSpace(r.Ports); v != "" {
			o["port"] = v
		}
		if v := strings.TrimSpace(r.Network); v != "" {
			o["network"] = v
		}
		return wrap(o)
	}
	var out []any
	if d := Lines(r.Domains); len(d) > 0 {
		out = append(out, extra(obj{"domain": d}))
	}
	if ip := Lines(r.IPs); len(ip) > 0 {
		out = append(out, extra(obj{"ip": ip}))
	}
	if len(out) == 0 && (strings.TrimSpace(r.Ports) != "" || strings.TrimSpace(r.Network) != "") {
		out = append(out, extra(obj{}))
	}
	return out
}

// Lines splits on newlines and commas, dropping blanks and # comments.
func Lines(s string) []string {
	var out []string
	for _, l := range strings.FieldsFunc(s, func(r rune) bool { return r == '\n' || r == '\r' || r == ',' }) {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
			out = append(out, l)
		}
	}
	return out
}

func encode(cfg obj) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(cfg); err != nil {
		return "", err
	}
	return strings.TrimSpace(buf.String()), nil
}

// Preset is a built-in routing rule.
type Preset struct {
	Key, Name, Domains, IPs string
}

// Presets are seeded as SmartRules on first start. They use plain domains
// and CIDRs only: a geosite/geoip tag missing from the app's geo files would
// stop xray from starting.
var Presets = []Preset{
	{Key: "instagram", Name: "Instagram", Domains: "domain:instagram.com\ndomain:cdninstagram.com\ndomain:ig.me\ndomain:instagr.am"},
	{Key: "whatsapp", Name: "WhatsApp", Domains: "domain:whatsapp.com\ndomain:whatsapp.net\ndomain:wa.me"},
	{Key: "telegram", Name: "Telegram", Domains: "domain:telegram.org\ndomain:t.me\ndomain:telegram.me\ndomain:telesco.pe\ndomain:tdesktop.com", IPs: "91.108.4.0/22\n91.108.8.0/22\n91.108.12.0/22\n91.108.16.0/22\n91.108.56.0/22\n95.161.64.0/20\n149.154.160.0/20\n2001:b28:f23d::/48\n2001:b28:f23f::/48\n2001:67c:4e8::/48"},
	{Key: "youtube", Name: "YouTube", Domains: "domain:youtube.com\ndomain:googlevideo.com\ndomain:ytimg.com\ndomain:youtu.be\ndomain:youtube-nocookie.com"},
	{Key: "google", Name: "Google", Domains: "domain:google.com\ndomain:gstatic.com\ndomain:googleapis.com\ndomain:googleusercontent.com\ndomain:gmail.com"},
	{Key: "twitter", Name: "Twitter / X", Domains: "domain:x.com\ndomain:twitter.com\ndomain:twimg.com\ndomain:t.co"},
	{Key: "facebook", Name: "Facebook", Domains: "domain:facebook.com\ndomain:fbcdn.net\ndomain:messenger.com\ndomain:fb.com"},
}
