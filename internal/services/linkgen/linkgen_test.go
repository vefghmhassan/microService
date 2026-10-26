package linkgen

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

func TestRealityTemplateProducesStableVlessLinks(t *testing.T) {
	settings, err := PrepareSettings("vless", "", 2, "de-")
	if err != nil {
		t.Fatal(err)
	}
	stream, err := PrepareStream(`{"network":"tcp","security":"reality","realitySettings":{"dest":"www.speedtest.net:443","serverNames":["www.speedtest.net"]}}`)
	if err != nil {
		t.Fatal(err)
	}
	// Preparing again must not regenerate anything.
	if s2, _ := PrepareSettings("vless", settings, 2, "de-"); s2 != settings {
		t.Fatal("clients were regenerated")
	}
	if st2, _ := PrepareStream(stream); st2 != stream {
		t.Fatal("reality keys were regenerated")
	}

	links, err := Links("vless", settings, stream, "5.6.7.8", 443, "DE-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 2 {
		t.Fatalf("got %d links", len(links))
	}
	u, err := url.Parse(links[0].URL)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if u.Scheme != "vless" || u.Host != "5.6.7.8:443" || q.Get("security") != "reality" ||
		q.Get("pbk") == "" || q.Get("sid") == "" || q.Get("sni") != "www.speedtest.net" || q.Get("encryption") != "none" {
		t.Fatalf("bad link %s", links[0].URL)
	}
	if !strings.HasPrefix(u.Fragment, "DE-1-de-1-") {
		t.Fatalf("fragment %q", u.Fragment)
	}
}

func TestNormalizeSettingsMakesTgIdNumeric(t *testing.T) {
	out := NormalizeSettings(`{"clients":[{"id":"x","tgId":""},{"id":"y","tgId":"42"}],"decryption":"none"}`)
	var v struct {
		Clients []map[string]any `json:"clients"`
	}
	json.Unmarshal([]byte(out), &v)
	if v.Clients[0]["tgId"] != float64(0) || v.Clients[1]["tgId"] != float64(42) {
		t.Fatalf("%s", out)
	}
}

func TestVmessWebsocketLink(t *testing.T) {
	settings := `{"clients":[{"id":"11111111-2222-4333-8444-555555555555","email":"a"}]}`
	stream := `{"network":"ws","security":"tls","wsSettings":{"path":"/ws","headers":{"Host":"cdn.example.com"}},"tlsSettings":{"serverName":"cdn.example.com"}}`
	links, err := Links("vmess", settings, stream, "9.9.9.9", 8443, "x")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(links[0].URL, "vmess://"))
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]string
	json.Unmarshal(raw, &v)
	if v["add"] != "9.9.9.9" || v["port"] != "8443" || v["net"] != "ws" || v["path"] != "/ws" || v["host"] != "cdn.example.com" || v["tls"] != "tls" {
		t.Fatalf("%v", v)
	}
}

func TestDisabledClientsAreSkipped(t *testing.T) {
	settings := `{"clients":[{"password":"p1","email":"a"},{"password":"p2","email":"b","enable":false}]}`
	links, err := Links("trojan", settings, `{"network":"tcp","security":"none"}`, "1.1.1.1", 443, "t")
	if err != nil || len(links) != 1 || !strings.HasPrefix(links[0].URL, "trojan://p1@1.1.1.1:443") {
		t.Fatalf("%v %v", links, err)
	}
}
