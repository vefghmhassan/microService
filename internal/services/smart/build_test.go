package smart

import (
	"encoding/json"
	"strings"
	"testing"

	"microserver/internal/models"
)

var testItems = []Item{
	{URL: "vless://uuid@1.2.3.4:443?type=tcp&security=reality&pbk=PUB&sid=ab&sni=www.x.com&fp=chrome&flow=xtls-rprx-vision#r", Label: "a"},
	{URL: "trojan://pw@h.example:443?type=ws&security=tls&path=%2Fp&host=h.example&sni=h.example#t", Label: "b"},
	{URL: "ss://Y2hhY2hhMjAtaWV0Zi1wb2x5MTMwNTpwdw@[2001:db8::1]:8388#s", Label: "c"},
	{URL: "bogus://x", Label: "d"},
}

func decode(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	return m
}

func TestBuildDefaults(t *testing.T) {
	p := models.SmartProfile{Name: "all", Scope: models.SmartScopeAll}
	res, err := Build(p, nil, testItems, "smart-all")
	if err != nil {
		t.Fatal(err)
	}
	if res.Outbounds != 3 || len(res.Skipped) != 1 {
		t.Fatalf("outbounds=%d skipped=%v", res.Outbounds, res.Skipped)
	}
	m := decode(t, res.JSON)
	if m["remarks"] != "smart-all" {
		t.Errorf("remarks = %v", m["remarks"])
	}
	obs := m["observatory"].(map[string]any)
	if obs["probeUrl"] != DefaultProbeURL || obs["probeInterval"] != DefaultProbeInterval {
		t.Errorf("observatory = %v", obs)
	}
	if _, ok := m["burstObservatory"]; ok {
		t.Error("burstObservatory next to observatory")
	}
	obs1 := m["outbounds"].([]any)
	tags := []string{}
	for _, o := range obs1 {
		tags = append(tags, o.(map[string]any)["tag"].(string))
	}
	if strings.Join(tags, ",") != "proxy-1,proxy-2,proxy-3,direct,block" {
		t.Errorf("tags = %v", tags)
	}
	rt := m["routing"].(map[string]any)
	bal := rt["balancers"].([]any)[0].(map[string]any)
	if bal["tag"] != BalancerTag || bal["strategy"].(map[string]any)["type"] != "leastPing" || bal["fallbackTag"] != "proxy-1" {
		t.Errorf("balancer = %v", bal)
	}
	rules := rt["rules"].([]any)
	last := rules[len(rules)-1].(map[string]any)
	if last["balancerTag"] != BalancerTag || last["network"] != "tcp,udp" {
		t.Errorf("last rule = %v", last)
	}
	first := rules[0].(map[string]any)
	if first["balancerTag"] != BalancerTag {
		t.Errorf("dns-module rule = %v", first)
	}
}

func TestBuildBurstLeastLoadRulesFragment(t *testing.T) {
	p := models.SmartProfile{
		Name: "x", ObsKind: "burst", Strategy: "leastLoad", Expected: 2, MaxRTT: "1s",
		ProbeInterval: "10s", Timeout: "3s", Sampling: 5, HTTPMethod: "GET",
		Fragment: true, BlockUDP443: true, BlockAds: true, IranDirect: true, MaxOutbounds: 2,
		NameTemplate: "{base}-smart",
	}
	rules := []models.SmartRule{{Name: "tg", Domains: "domain:t.me\n# c\n", IPs: "91.108.4.0/22"}}
	res, err := Build(p, rules, testItems, "n")
	if err != nil {
		t.Fatal(err)
	}
	if res.Outbounds != 2 {
		t.Fatalf("cap: %d", res.Outbounds)
	}
	m := decode(t, res.JSON)
	if _, ok := m["observatory"]; ok {
		t.Error("observatory next to burstObservatory")
	}
	pc := m["burstObservatory"].(map[string]any)["pingConfig"].(map[string]any)
	if pc["interval"] != "10s" || pc["timeout"] != "3s" || pc["sampling"].(float64) != 5 || pc["httpMethod"] != "GET" {
		t.Errorf("pingConfig = %v", pc)
	}
	st := m["routing"].(map[string]any)["balancers"].([]any)[0].(map[string]any)["strategy"].(map[string]any)
	set := st["settings"].(map[string]any)
	if st["type"] != "leastLoad" || set["expected"].(float64) != 2 || set["maxRTT"] != "1s" {
		t.Errorf("strategy = %v", st)
	}
	hasFrag := false
	for _, o := range m["outbounds"].([]any) {
		if o.(map[string]any)["tag"] == "fragment" {
			hasFrag = true
		}
	}
	if !hasFrag {
		t.Error("trojan tls with fragment needs the fragment outbound")
	}
	js := res.JSON
	for _, want := range []string{`"geosite:category-ads-all"`, `"geoip:ir"`, `"port":"443"`, `"domain":["domain:t.me"]`, `"ip":["91.108.4.0/22"]`} {
		if !strings.Contains(js, want) {
			t.Errorf("missing %s", want)
		}
	}
	if strings.Index(js, `"domain:t.me"`) > strings.LastIndex(js, `"tcp,udp"`) {
		t.Error("custom rule after catch-all")
	}
}

func TestValidate(t *testing.T) {
	bad := []models.SmartProfile{
		{Name: ""},
		{Name: "a", ProbeInterval: "10"},
		{Name: "a", Timeout: "-1s"},
		{Name: "a", MaxRTT: "fast"},
		{Name: "a", ProbeURL: "ftp://x"},
		{Name: "a", NameTemplate: "fixed"},
		{Name: "a", Scope: models.SmartScopeAll, Country: "DEU"},
	}
	for i, p := range bad {
		Normalize(&p)
		if Validate(&p) == nil {
			t.Errorf("case %d accepted", i)
		}
	}
	ok := models.SmartProfile{Name: "a", Scope: models.SmartScopeAll, NameTemplate: "fixed"}
	Normalize(&ok)
	if err := Validate(&ok); err != nil {
		t.Error(err)
	}
	if _, err := Build(ok, nil, []Item{{URL: "nope"}}, "x"); err == nil {
		t.Error("built without any usable link")
	}
}
