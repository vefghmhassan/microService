package linkgen

import (
	"encoding/json"
	"strings"
	"testing"
)

func clientCfg(t *testing.T, link string) map[string]any {
	t.Helper()
	s, err := ClientJSON(link)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(s, "\n") {
		t.Fatal("output must be a single line")
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func proxyOf(m map[string]any) map[string]any {
	return m["outbounds"].([]any)[0].(map[string]any)
}

func TestClientJSONVlessHTTPCamouflage(t *testing.T) {
	settings := `{"clients":[{"id":"c43a21e9-dcba-49dd-be3a-4ca991a9ad25","email":"a","flow":""}],"decryption":"mlkem-server","encryption":"mlkem-client"}`
	stream := `{"network":"tcp","security":"none","tcpSettings":{"header":{"type":"http","request":{"path":["/"],"headers":{"Host":["speedtest.net"]}}}}}`
	links, err := Links("vless", settings, stream, "1.2.3.4", 2007, "in-2007")
	if err != nil || len(links) != 1 {
		t.Fatal(err, links)
	}
	if strings.Contains(links[0].URL, "mlkem-server") || !strings.Contains(links[0].URL, "encryption=mlkem-client") {
		t.Fatalf("link must carry the client encryption: %s", links[0].URL)
	}
	m := clientCfg(t, links[0].URL)
	p := proxyOf(m)
	user := p["settings"].(map[string]any)["vnext"].([]any)[0].(map[string]any)["users"].([]any)[0].(map[string]any)
	if user["id"] != "c43a21e9-dcba-49dd-be3a-4ca991a9ad25" || user["encryption"] != "mlkem-client" {
		t.Fatalf("user: %v", user)
	}
	st := p["streamSettings"].(map[string]any)
	if st["security"] != "" {
		t.Fatalf("security: %v", st["security"])
	}
	req := st["tcpSettings"].(map[string]any)["header"].(map[string]any)["request"].(map[string]any)
	if req["method"] != "SSH-2.0-OpenSSH_9.6" || req["headers"].(map[string]any)["Host"].([]any)[0] != "speedtest.net" {
		t.Fatalf("camouflage: %v", req)
	}
	if !strings.HasPrefix(m["remarks"].(string), "in-2007") {
		t.Fatalf("remarks: %v", m["remarks"])
	}
}

func TestClientJSONOtherProtocols(t *testing.T) {
	m := clientCfg(t, "vless://uuid@h.example:443?type=tcp&security=reality&pbk=PUB&sid=ab&sni=www.x.com&fp=chrome&flow=xtls-rprx-vision#r")
	rs := proxyOf(m)["streamSettings"].(map[string]any)["realitySettings"].(map[string]any)
	if rs["publicKey"] != "PUB" || rs["shortId"] != "ab" || rs["serverName"] != "www.x.com" {
		t.Fatalf("reality: %v", rs)
	}
	if _, has := proxyOf(m)["streamSettings"].(map[string]any)["tcpSettings"]; has {
		t.Fatal("no tcpSettings without camouflage")
	}

	m = clientCfg(t, "trojan://pw@h.example:443?type=ws&security=tls&path=%2Fp&host=h.example&sni=h.example#t")
	p := proxyOf(m)
	if p["protocol"] != "trojan" || p["streamSettings"].(map[string]any)["wsSettings"].(map[string]any)["path"] != "/p" {
		t.Fatalf("trojan ws: %v", p)
	}

	m = clientCfg(t, "ss://"+"Y2hhY2hhMjAtaWV0Zi1wb2x5MTMwNTpwdw"+"@h.example:8388#s")
	srv := proxyOf(m)["settings"].(map[string]any)["servers"].([]any)[0].(map[string]any)
	if srv["method"] != "chacha20-ietf-poly1305" || srv["password"] != "pw" {
		t.Fatalf("ss: %v", srv)
	}

	if _, err := ClientJSON("http://nope"); err == nil {
		t.Fatal("expected error for unsupported scheme")
	}
}
