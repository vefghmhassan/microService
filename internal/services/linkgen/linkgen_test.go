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

func TestLinksViaSendsHostnameThroughCDN(t *testing.T) {
	settings := `{"clients":[{"id":"11111111-1111-1111-1111-111111111111","email":"a"}]}`
	cases := map[string]string{
		`{"network":"ws","security":"tls","wsSettings":{"path":"/w"}}`:              "host=fr1.example.com",
		`{"network":"xhttp","security":"none","xhttpSettings":{"path":"/x"}}`:       "host=fr1.example.com",
		`{"network":"grpc","security":"tls","grpcSettings":{"serviceName":"g"}}`:    "authority=fr1.example.com",
		`{"network":"ws","security":"tls","wsSettings":{"path":"/w","host":"own"}}`: "host=own",
	}
	for stream, want := range cases {
		ls, err := LinksVia("vless", settings, stream, "104.16.1.1", "fr1.example.com", 443, "cdn")
		if err != nil || len(ls) != 1 {
			t.Fatalf("%s: %v %v", stream, ls, err)
		}
		u := ls[0].URL
		if !strings.Contains(u, "@104.16.1.1:443?") || !strings.Contains(u, want) {
			t.Errorf("%s: %s lacks %s", stream, u, want)
		}
		if strings.Contains(stream, `"tls"`) && !strings.Contains(u, "sni=fr1.example.com") {
			t.Errorf("%s: no sni in %s", stream, u)
		}
	}
	// Without a CDN hostname nothing is invented for a bare IP.
	ls, _ := Links("vless", settings, `{"network":"ws","security":"tls","wsSettings":{"path":"/w"}}`, "1.2.3.4", 443, "x")
	if strings.Contains(ls[0].URL, "sni=") || strings.Contains(ls[0].URL, "host=") {
		t.Errorf("plain link got sni/host: %s", ls[0].URL)
	}
}

func TestLinksOnIPv6Host(t *testing.T) {
	const host = "2a01:4f8:c17:3f5::1"
	uuid := `{"clients":[{"id":"11111111-1111-1111-1111-111111111111","email":"a"}]}`
	cases := []struct{ proto, settings string }{
		{"vless", uuid},
		{"trojan", `{"clients":[{"password":"pw","email":"a"}]}`},
		{"shadowsocks", `{"method":"2022-blake3-aes-128-gcm","password":"AAAAAAAAAAAAAAAAAAAAAA==","clients":[{"password":"AAAAAAAAAAAAAAAAAAAAAA==","email":"a"}]}`},
	}
	for _, c := range cases {
		ls, err := Links(c.proto, c.settings, `{"network":"tcp","security":"none"}`, host, 443, "v6")
		if err != nil || len(ls) != 1 {
			t.Fatalf("%s: %v %v", c.proto, ls, err)
		}
		if !strings.Contains(ls[0].URL, "@["+host+"]:443") {
			t.Errorf("%s: unbracketed %s", c.proto, ls[0].URL)
		}
		js, err := ClientJSON(ls[0].URL)
		if err != nil || !strings.Contains(js, `"address":"`+host+`"`) {
			t.Errorf("%s: client json %v %s", c.proto, err, js)
		}
	}
	// vmess carries the bare address in its JSON.
	ls, err := Links("vmess", uuid, `{"network":"tcp","security":"none"}`, host, 443, "v6")
	if err != nil || len(ls) != 1 {
		t.Fatalf("vmess: %v %v", ls, err)
	}
	raw, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(ls[0].URL, "vmess://"))
	var v map[string]any
	if json.Unmarshal(raw, &v) != nil || v["add"] != host {
		t.Errorf("vmess %s", raw)
	}
}
