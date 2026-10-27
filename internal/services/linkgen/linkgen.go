// Package linkgen turns an inbound definition into client share links, and
// fills in the parts of an inbound template that must be generated once
// (clients, Reality keys) so every server ends up with identical links.
package linkgen

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"golang.org/x/crypto/curve25519"
)

type Link struct {
	Email string `json:"email"`
	URL   string `json:"url"`
}

type obj = map[string]any

func parse(s string) obj {
	m := obj{}
	if strings.TrimSpace(s) != "" {
		_ = json.Unmarshal([]byte(s), &m)
	}
	return m
}

func str(m obj, keys ...string) string {
	var cur any = m
	for _, k := range keys {
		mm, ok := cur.(obj)
		if !ok {
			return ""
		}
		cur = mm[k]
	}
	switch v := cur.(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(v)
	}
	return ""
}

func sub(m obj, k string) obj {
	if v, ok := m[k].(obj); ok {
		return v
	}
	return obj{}
}

func first(m obj, k string) string {
	switch v := m[k].(type) {
	case []any:
		if len(v) > 0 {
			if s, ok := v[0].(string); ok {
				return s
			}
		}
	case string:
		return strings.Split(v, ",")[0]
	}
	return ""
}

func joinList(m obj, k string) string {
	if v, ok := m[k].([]any); ok {
		parts := make([]string, 0, len(v))
		for _, x := range v {
			if s, ok := x.(string); ok {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, ",")
	}
	return ""
}

// streamParams are the transport/security query parameters shared by vless
// and trojan links (the v2rayN / 3x-ui convention).
func streamParams(stream obj) url.Values {
	q := url.Values{}
	network := str(stream, "network")
	if network == "" {
		network = "tcp"
	}
	q.Set("type", network)
	switch network {
	case "tcp":
		h := sub(sub(stream, "tcpSettings"), "header")
		if str(h, "type") == "http" {
			q.Set("headerType", "http")
			req := sub(h, "request")
			if p := first(req, "path"); p != "" {
				q.Set("path", p)
			}
			if host := first(sub(req, "headers"), "Host"); host != "" {
				q.Set("host", host)
			}
		}
	case "ws":
		ws := sub(stream, "wsSettings")
		q.Set("path", str(ws, "path"))
		host := str(ws, "host")
		if host == "" {
			host = str(ws, "headers", "Host")
		}
		if host != "" {
			q.Set("host", host)
		}
	case "grpc":
		g := sub(stream, "grpcSettings")
		q.Set("serviceName", str(g, "serviceName"))
		if str(g, "multiMode") == "true" {
			q.Set("mode", "multi")
		}
		if a := str(g, "authority"); a != "" {
			q.Set("authority", a)
		}
	case "httpupgrade":
		h := sub(stream, "httpupgradeSettings")
		q.Set("path", str(h, "path"))
		if host := str(h, "host"); host != "" {
			q.Set("host", host)
		}
	case "xhttp":
		x := sub(stream, "xhttpSettings")
		q.Set("path", str(x, "path"))
		if host := str(x, "host"); host != "" {
			q.Set("host", host)
		}
		if mode := str(x, "mode"); mode != "" {
			q.Set("mode", mode)
		}
	case "kcp":
		k := sub(stream, "kcpSettings")
		if t := str(sub(k, "header"), "type"); t != "" {
			q.Set("headerType", t)
		}
		if seed := str(k, "seed"); seed != "" {
			q.Set("seed", seed)
		}
	}

	security := str(stream, "security")
	if security == "" {
		security = "none"
	}
	q.Set("security", security)
	switch security {
	case "tls":
		t := sub(stream, "tlsSettings")
		if sni := str(t, "serverName"); sni != "" {
			q.Set("sni", sni)
		}
		if alpn := joinList(t, "alpn"); alpn != "" {
			q.Set("alpn", alpn)
		}
		if fp := str(t, "settings", "fingerprint"); fp != "" {
			q.Set("fp", fp)
		}
	case "reality":
		r := sub(stream, "realitySettings")
		if sni := first(r, "serverNames"); sni != "" {
			q.Set("sni", sni)
		}
		q.Set("pbk", str(r, "settings", "publicKey"))
		if sid := first(r, "shortIds"); sid != "" {
			q.Set("sid", sid)
		}
		fp := str(r, "settings", "fingerprint")
		if fp == "" {
			fp = "chrome"
		}
		q.Set("fp", fp)
		if spx := str(r, "settings", "spiderX"); spx != "" {
			q.Set("spx", spx)
		}
	}
	return q
}

func clients(settings obj) []obj {
	var out []obj
	if arr, ok := settings["clients"].([]any); ok {
		for _, c := range arr {
			if m, ok := c.(obj); ok {
				if en, ok := m["enable"].(bool); ok && !en {
					continue
				}
				out = append(out, m)
			}
		}
	}
	return out
}

// Links builds one share link per enabled client. remark is the visible name;
// each link gets "remark-email" so configs stay distinguishable.
func Links(protocol, settingsJSON, streamJSON, host string, port int, remark string) ([]Link, error) {
	settings := parse(settingsJSON)
	stream := parse(streamJSON)
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	// A TLS inbound without its own serverName is reached by the hostname
	// the link points at; with a bare IP there is nothing to send.
	params := func() url.Values {
		q := streamParams(stream)
		if q.Get("security") == "tls" && q.Get("sni") == "" && host != "" && net.ParseIP(host) == nil {
			q.Set("sni", host)
		}
		return q
	}
	var out []Link
	for _, c := range clients(settings) {
		email := str(c, "email")
		label := remark
		if email != "" {
			label = remark + "-" + email
		}
		var u string
		switch protocol {
		case "vless":
			q := params()
			// "decryption" is the server half of VLESS Encryption (holds the private key); links carry the client half.
			q.Set("encryption", firstNonEmpty(str(settings, "encryption"), "none"))
			if flow := str(c, "flow"); flow != "" {
				q.Set("flow", flow)
			}
			u = "vless://" + str(c, "id") + "@" + addr + "?" + q.Encode() + "#" + url.PathEscape(label)
		case "trojan":
			q := params()
			u = "trojan://" + url.PathEscape(str(c, "password")) + "@" + addr + "?" + q.Encode() + "#" + url.PathEscape(label)
		case "vmess":
			q := params()
			v := map[string]string{
				"v": "2", "ps": label, "add": host, "port": strconv.Itoa(port), "id": str(c, "id"),
				"aid": "0", "scy": firstNonEmpty(str(c, "security"), "auto"), "net": q.Get("type"),
				"type": firstNonEmpty(q.Get("headerType"), "none"), "host": q.Get("host"),
				"path": firstNonEmpty(q.Get("path"), q.Get("serviceName")), "tls": q.Get("security"),
				"sni": q.Get("sni"), "alpn": q.Get("alpn"), "fp": q.Get("fp"),
			}
			if v["tls"] == "none" {
				v["tls"] = ""
			}
			b, _ := json.Marshal(v)
			u = "vmess://" + base64.StdEncoding.EncodeToString(b)
		case "shadowsocks":
			method := firstNonEmpty(str(c, "method"), str(settings, "method"))
			password := str(c, "password")
			if sp := str(settings, "password"); sp != "" && strings.HasPrefix(method, "2022-") {
				// SS-2022 multi-user: server key and user key are joined.
				password = sp + ":" + password
			}
			cred := base64.RawURLEncoding.EncodeToString([]byte(method + ":" + password))
			u = "ss://" + cred + "@" + addr + "#" + url.PathEscape(label)
		default:
			return nil, fmt.Errorf("links for protocol %q are not supported", protocol)
		}
		out = append(out, Link{Email: email, URL: u})
	}
	return out, nil
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

// ---- template preparation ----

func UUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// X25519 returns a Reality key pair in xray's base64url form.
func X25519() (private, public string, err error) {
	priv := make([]byte, 32)
	if _, err = rand.Read(priv); err != nil {
		return
	}
	priv[0] &= 248
	priv[31] &= 127
	priv[31] |= 64
	pub, err := curve25519.X25519(priv, curve25519.Basepoint)
	if err != nil {
		return
	}
	return base64.RawURLEncoding.EncodeToString(priv), base64.RawURLEncoding.EncodeToString(pub), nil
}

// PrepareSettings adds count clients when the settings have none, so links
// are generated once here and are identical on every panel.
func PrepareSettings(protocol, settingsJSON string, count int, emailPrefix string) (string, error) {
	settings := parse(settingsJSON)
	if len(clients(settings)) > 0 || count <= 0 {
		return marshal(settings)
	}
	if _, ok := settings["clients"]; !ok && protocol == "shadowsocks" && str(settings, "method") == "" {
		settings["method"] = "chacha20-ietf-poly1305"
	}
	arr := make([]any, 0, count)
	for i := 1; i <= count; i++ {
		email := fmt.Sprintf("%s%d-%s", emailPrefix, i, randHex(2))
		c := obj{"email": email, "enable": true, "limitIp": 0, "totalGB": 0, "expiryTime": 0, "subId": randHex(8), "tgId": 0, "reset": 0}
		switch protocol {
		case "vless":
			c["id"] = UUID()
			c["flow"] = ""
		case "vmess":
			c["id"] = UUID()
			c["security"] = "auto"
		case "trojan":
			c["password"] = randHex(12)
		case "shadowsocks":
			c["method"] = str(settings, "method")
			c["password"] = randHex(16)
		default:
			return "", fmt.Errorf("cannot generate clients for %q", protocol)
		}
		arr = append(arr, c)
	}
	settings["clients"] = arr
	if protocol == "vless" && str(settings, "decryption") == "" {
		settings["decryption"] = "none"
	}
	return marshal(settings)
}

// NormalizeSettings fixes client fields to the types 3x-ui v3 decodes strictly
// (tgId is an int64; older templates and panels used ""), leaving the rest as is.
func NormalizeSettings(settingsJSON string) string {
	settings := parse(settingsJSON)
	arr, ok := settings["clients"].([]any)
	if !ok {
		return settingsJSON
	}
	for _, c := range arr {
		m, ok := c.(obj)
		if !ok {
			continue
		}
		if s, isStr := m["tgId"].(string); isStr {
			n, _ := strconv.ParseInt(s, 10, 64)
			m["tgId"] = n
		}
	}
	out, err := marshal(settings)
	if err != nil {
		return settingsJSON
	}
	return out
}

// PrepareStream generates Reality keys and a short id when they are missing.
// The pair must be shared by every panel or links would differ per server.
func PrepareStream(streamJSON string) (string, error) {
	stream := parse(streamJSON)
	if str(stream, "security") == "reality" {
		r := sub(stream, "realitySettings")
		if str(r, "privateKey") == "" {
			priv, pub, err := X25519()
			if err != nil {
				return "", err
			}
			r["privateKey"] = priv
			s := sub(r, "settings")
			s["publicKey"] = pub
			if str(s, "fingerprint") == "" {
				s["fingerprint"] = "chrome"
			}
			r["settings"] = s
		}
		if first(r, "shortIds") == "" {
			r["shortIds"] = []any{randHex(4)}
		}
		stream["realitySettings"] = r
	}
	return marshal(stream)
}

func marshal(m obj) (string, error) {
	b, err := json.Marshal(m)
	return string(b), err
}
