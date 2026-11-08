package linkgen

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestHysteria2LinkAndJSON(t *testing.T) {
	settings, err := PrepareSettings("hysteria", `{"version":2}`, 1, "hy-")
	if err != nil {
		t.Fatal(err)
	}
	stream, err := PrepareStream(`{"network":"hysteria","security":"tls","tlsSettings":{"alpn":["h3"]},"finalmask":{"udp":[{"type":"salamander","settings":{}}]}}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(settings, `"auth":"`) || !strings.Contains(stream, `"password":"`) {
		t.Fatalf("not prepared: %s %s", settings, stream)
	}
	ls, err := LinksVia("hysteria", settings, stream, "d1.example.com", "", 8443, "hysteria2")
	if err != nil || len(ls) != 1 {
		t.Fatalf("%v %v", ls, err)
	}
	u := ls[0].URL
	if !strings.HasPrefix(u, "hysteria2://") || !strings.Contains(u, "@d1.example.com:8443/?") || !strings.Contains(u, "sni=d1.example.com") ||
		!strings.Contains(u, "obfs=salamander") || !strings.Contains(u, "alpn=h3") {
		t.Fatalf("link %s", u)
	}
	j, err := ClientJSON(u)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Outbounds []map[string]any `json:"outbounds"`
	}
	if err := json.Unmarshal([]byte(j), &cfg); err != nil {
		t.Fatal(err)
	}
	p := cfg.Outbounds[0]
	ss, _ := p["streamSettings"].(map[string]any)
	if p["protocol"] != "hysteria" || ss["network"] != "hysteria" || ss["finalmask"] == nil ||
		!strings.Contains(j, `"address":"d1.example.com"`) || !strings.Contains(j, `"serverName":"d1.example.com"`) {
		t.Fatalf("json %s", j)
	}
}

func TestFragmentOnlyForTLS(t *testing.T) {
	j, err := ClientJSONWith("vless://11111111-2222-4333-8444-555555555555@1.2.3.4:443?type=tcp&security=reality&pbk=x&sni=a.com#r", JSONOptions{Fragment: true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(j, "fragment") {
		t.Fatalf("fragment on reality: %s", j)
	}
}
