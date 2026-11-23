package linkgen

import (
	"net/url"
	"strings"
	"testing"
)

func TestVLESSEncPairAndLink(t *testing.T) {
	out, err := PrepareVLESSEnc("vless", `{"clients":[{"id":"11111111-1111-1111-1111-111111111111","email":"a"}],"decryption":"vlessenc","fallbacks":[]}`)
	if err != nil {
		t.Fatal(err)
	}
	m := parse(out)
	if _, ok := m["fallbacks"]; ok {
		t.Fatal("fallbacks kept next to a decryption: xray would not start")
	}
	dec, enc := str(m, "decryption"), str(m, "encryption")
	if !strings.HasPrefix(dec, "mlkem768x25519plus.native.600s.") || !strings.HasPrefix(enc, "mlkem768x25519plus.native.0rtt.") {
		t.Fatalf("pair %q %q", dec, enc)
	}
	// Xray accepts a key of 32 bytes in base64url.
	for _, k := range []string{dec[strings.LastIndex(dec, ".")+1:], enc[strings.LastIndex(enc, ".")+1:]} {
		if b, err := b64decode(k); err != nil || len(b) != 32 {
			t.Fatalf("key %q", k)
		}
	}
	ls, _ := Links("vless", out, `{"network":"tcp","security":"none"}`, "1.2.3.4", 10443, "PQ")
	if !strings.Contains(ls[0].URL, "encryption="+url.QueryEscape(enc)) {
		t.Fatalf("link must carry the client half: %s", ls[0].URL)
	}
	if same, _ := PrepareVLESSEnc("vless", `{"decryption":"none"}`); same != `{"decryption":"none"}` {
		t.Fatal("touched a plain inbound")
	}
}

func TestSudokuFinalMaskTravelsAsFm(t *testing.T) {
	settings := `{"clients":[{"id":"11111111-1111-1111-1111-111111111111","email":"a"}],"decryption":"none"}`
	// Sudoku on both sides; the server-only filter mask stays out of links.
	stream := `{"network":"tcp","security":"none","finalmask":{"tcp":[{"type":"sudoku","settings":{"password":"p"}},{"type":"filter","settings":{}}]}}`
	ls, _ := Links("vless", settings, stream, "1.2.3.4", 10443, "PQ")
	q, _ := url.ParseQuery(strings.SplitN(strings.SplitN(ls[0].URL, "?", 2)[1], "#", 2)[0])
	if fm := q.Get("fm"); !strings.Contains(fm, "sudoku") || strings.Contains(fm, "filter") {
		t.Fatalf("fm %q", fm)
	}
	j, _ := ClientJSON(ls[0].URL)
	if !strings.Contains(j, `"finalmask":{"tcp":[{"settings":{"password":"p"},"type":"sudoku"}]}`) {
		t.Fatalf("client json lost the mask: %s", j)
	}
	// Hysteria2 keeps Salamander in obfs, never fm.
	hy, _ := Links("hysteria", `{"version":2,"clients":[{"auth":"x","email":"a"}]}`,
		`{"network":"hysteria","security":"tls","finalmask":{"udp":[{"type":"salamander","settings":{"password":"s"}}]}}`, "h.example.com", 443, "HY")
	if strings.Contains(hy[0].URL, "fm=") || !strings.Contains(hy[0].URL, "obfs=salamander") {
		t.Fatalf("hysteria link %s", hy[0].URL)
	}
}

func TestGrpcMultiModeAndSS2022Keys(t *testing.T) {
	g, _ := Links("vless", `{"clients":[{"id":"11111111-1111-1111-1111-111111111111"}]}`,
		`{"network":"grpc","security":"none","grpcSettings":{"serviceName":"g","multiMode":true}}`, "1.2.3.4", 1, "G")
	if j, _ := ClientJSON(g[0].URL); !strings.Contains(j, `"multiMode":true`) {
		t.Fatalf("multiMode lost: %s", j)
	}
	out, err := PrepareSettings("shadowsocks", `{"method":"2022-blake3-aes-256-gcm","password":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=","clients":[]}`, 1, "ss-")
	if err != nil {
		t.Fatal(err)
	}
	c := clients(parse(out))[0]
	if b, err := b64decode(str(c, "password")); err != nil || len(b) != 32 || str(c, "method") != "" {
		t.Fatalf("client %v", c)
	}
}

func TestNormalizeDropsFallbacksNextToDecryption(t *testing.T) {
	// The template that took xray down on live servers.
	bad := `{"clients":[{"id":"11111111-1111-1111-1111-111111111111","email":"a","tgId":""}],"decryption":"mlkem768x25519plus.native.600s.x","encryption":"e","fallbacks":[]}`
	m := parse(NormalizeSettings(bad))
	if _, ok := m["fallbacks"]; ok || str(m, "decryption") == "" {
		t.Fatalf("normalized %v", m)
	}
	plain := parse(NormalizeSettings(`{"clients":[],"decryption":"none","fallbacks":[{"dest":80}]}`))
	if _, ok := plain["fallbacks"]; !ok {
		t.Fatal("fallbacks of a plain VLESS inbound removed")
	}
}
