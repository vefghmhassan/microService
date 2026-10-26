package linkgen

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// ClientJSON turns a vless/vmess/trojan/ss share link into the full client
// xray config the dashboard's "ساخت کانفیگ JSON" page builds (socks/http
// inbounds, proxy/direct/block outbounds, HTTP camouflage on plain TCP when
// the link asks for it) and returns it as one compact line. vpn-pannel stores
// such a JSON as a custom node.
func ClientJSON(link string) (string, error) {
	f, err := parseLink(link)
	if err != nil {
		return "", err
	}
	remark := f.remark
	if remark == "" {
		remark = strings.ToUpper(f.protocol)
	}

	proxy := obj{"tag": "proxy", "protocol": f.protocol}
	switch f.protocol {
	case "vless":
		proxy["settings"] = obj{"vnext": []any{obj{"address": f.address, "port": f.port, "users": []any{
			obj{"id": f.id, "encryption": firstNonEmpty(f.encryption, "none"), "flow": f.flow, "level": 8, "security": "auto"},
		}}}}
	case "vmess":
		proxy["settings"] = obj{"vnext": []any{obj{"address": f.address, "port": f.port, "users": []any{
			obj{"id": f.id, "security": firstNonEmpty(f.security, "auto"), "alterId": 0, "level": 8},
		}}}}
	case "trojan":
		proxy["settings"] = obj{"servers": []any{obj{"address": f.address, "port": f.port, "password": f.password}}}
	case "shadowsocks":
		proxy["settings"] = obj{"servers": []any{obj{"address": f.address, "port": f.port, "password": f.password, "method": f.method, "uot": false}}}
	}

	network := firstNonEmpty(f.network, "tcp")
	sec := firstNonEmpty(f.tls, "none")
	stream := obj{"network": network, "security": sec}
	plainTCP := network == "tcp" && sec == "none"
	switch network {
	case "tcp":
		if plainTCP && f.headerType == "http" {
			stream["tcpSettings"] = obj{"header": obj{
				"type": "http",
				"request": obj{
					"version": "1.1", "method": "SSH-2.0-OpenSSH_9.6", "path": listOr(f.path, "/"),
					"headers": obj{
						"Accept-Encoding": []string{"gzip, deflate"},
						"Connection":      []string{"keep-alive"},
						"Host":            listOr(f.host, "speedtest.net"),
						"Pragma":          "no-cache",
						"User-Agent":      []string{camoUA[0], camoUA[1]},
					},
				},
			}}
		}
	case "ws":
		stream["wsSettings"] = obj{"path": firstNonEmpty(f.path, "/"), "host": f.host}
	case "grpc":
		stream["grpcSettings"] = obj{"serviceName": f.serviceName, "authority": f.host, "multiMode": false}
	case "httpupgrade":
		stream["httpupgradeSettings"] = obj{"path": firstNonEmpty(f.path, "/"), "host": f.host}
	case "xhttp":
		stream["xhttpSettings"] = obj{"path": firstNonEmpty(f.path, "/"), "host": f.host, "mode": firstNonEmpty(f.mode, "auto")}
	}
	switch sec {
	case "tls":
		t := obj{"serverName": f.sni, "fingerprint": firstNonEmpty(f.fp, "chrome"), "allowInsecure": f.allowInsecure}
		if len(f.alpn) > 0 {
			t["alpn"] = f.alpn
		}
		stream["tlsSettings"] = t
	case "reality":
		stream["realitySettings"] = obj{"serverName": f.sni, "fingerprint": firstNonEmpty(f.fp, "chrome"), "publicKey": f.publicKey, "shortId": f.shortID, "spiderX": f.spiderX}
	}
	if sec == "none" {
		stream["security"] = ""
	}
	proxy["streamSettings"] = stream
	proxy["mux"] = obj{"concurrency": -1, "enabled": false, "xudpConcurrency": 8, "xudpProxyUDP443": ""}

	cfg := obj{
		"dns": obj{"hosts": obj{"domain:googleapis.cn": "googleapis.com"}, "servers": []string{"1.1.1.1"}},
		"inbounds": []any{
			obj{
				"listen": "127.0.0.1", "port": 10808, "protocol": "socks",
				"settings": obj{"auth": "noauth", "udp": true, "userLevel": 8},
				"sniffing": obj{"destOverride": []string{"http", "tls"}, "enabled": true},
				"tag":      "socks",
			},
			obj{"listen": "127.0.0.1", "port": 10809, "protocol": "http", "settings": obj{"userLevel": 8}, "tag": "http"},
		},
		"log": obj{"loglevel": "warning"},
		"outbounds": []any{
			proxy,
			obj{"protocol": "freedom", "settings": obj{}, "tag": "direct"},
			obj{"protocol": "blackhole", "settings": obj{"response": obj{"type": "http"}}, "tag": "block"},
		},
		"policy": obj{
			"levels": obj{"8": obj{"connIdle": 300, "downlinkOnly": 1, "handshake": 4, "uplinkOnly": 1}},
			"system": obj{"statsOutboundDownlink": true, "statsOutboundUplink": true},
		},
		"remarks": remark,
		"routing": obj{
			"domainStrategy": "IPIfNonMatch",
			"rules":          []any{obj{"ip": []string{"1.1.1.1"}, "outboundTag": "proxy", "port": "53", "type": "field"}},
		},
		"stats": obj{},
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(cfg); err != nil {
		return "", err
	}
	return strings.TrimSpace(buf.String()), nil
}

var camoUA = [2]string{
	"Mozilla/5.0 (Windows NT 10.0; WOW64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/53.0.2785.143 Safari/537.36",
	"Mozilla/5.0 (iPhone; CPU iPhone OS 10_0_2 like Mac OS X) AppleWebKit/601.1 (KHTML, like Gecko) CriOS/53.0.2785.109 Mobile/14A456 Safari/601.1.46",
}

// listOr splits a comma separated value, falling back to one default entry.
func listOr(s, def string) []string {
	var out []string
	for _, x := range strings.Split(s, ",") {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	if len(out) == 0 {
		return []string{def}
	}
	return out
}

type linkForm struct {
	protocol, remark, address                string
	port                                     int
	id, encryption, flow, security, password string
	method                                   string
	network, host, path, serviceName, mode   string
	headerType, tls, sni, fp                 string
	publicKey, shortID, spiderX              string
	alpn                                     []string
	allowInsecure                            bool
}

func b64decode(s string) ([]byte, error) {
	s = strings.NewReplacer("-", "+", "_", "/").Replace(strings.TrimRight(s, "="))
	return base64.RawStdEncoding.DecodeString(s)
}

func parseLink(link string) (*linkForm, error) {
	raw := strings.TrimSpace(link)
	scheme, rest, ok := strings.Cut(raw, "://")
	if !ok {
		return nil, errors.New("not a share link")
	}
	scheme = strings.ToLower(scheme)
	if scheme == "vmess" {
		return parseVmess(rest)
	}
	if scheme != "vless" && scheme != "trojan" && scheme != "ss" {
		return nil, fmt.Errorf("unsupported link scheme %q", scheme)
	}
	u, err := url.Parse("http://" + rest)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	port, _ := strconv.Atoi(u.Port())
	if u.Hostname() == "" || port == 0 {
		return nil, errors.New("link has no valid address or port")
	}
	f := &linkForm{
		protocol: scheme, address: u.Hostname(), port: port, remark: u.Fragment,
		network: firstNonEmpty(q.Get("type"), "tcp"), host: firstNonEmpty(q.Get("host"), q.Get("authority")), path: q.Get("path"),
		serviceName: q.Get("serviceName"), mode: q.Get("mode"), headerType: q.Get("headerType"),
		tls: firstNonEmpty(q.Get("security"), "none"), sni: q.Get("sni"), fp: q.Get("fp"),
		publicKey: q.Get("pbk"), shortID: q.Get("sid"), spiderX: q.Get("spx"), allowInsecure: q.Get("allowInsecure") == "1",
	}
	if f.network == "raw" {
		f.network = "tcp"
	}
	if a := q.Get("alpn"); a != "" {
		f.alpn = strings.Split(a, ",")
	}
	user := u.User.Username()
	switch scheme {
	case "vless":
		f.id, f.encryption, f.flow = user, q.Get("encryption"), q.Get("flow")
	case "trojan":
		f.password = user
	case "ss":
		f.protocol = "shadowsocks"
		if pw, has := u.User.Password(); has {
			user += ":" + pw
		}
		if !strings.Contains(user, ":") {
			dec, err := b64decode(user)
			if err != nil {
				return nil, errors.New("bad shadowsocks userinfo")
			}
			user = string(dec)
		}
		f.method, f.password, _ = strings.Cut(user, ":")
		f.network, f.tls = "tcp", "none"
	}
	return f, nil
}

func parseVmess(b64 string) (*linkForm, error) {
	dec, err := b64decode(b64)
	if err != nil {
		return nil, errors.New("bad vmess link")
	}
	var v map[string]any
	if err := json.Unmarshal(dec, &v); err != nil {
		return nil, errors.New("bad vmess link")
	}
	s := func(k string) string {
		switch x := v[k].(type) {
		case string:
			return x
		case float64:
			return strconv.FormatFloat(x, 'f', -1, 64)
		}
		return ""
	}
	port, _ := strconv.Atoi(s("port"))
	f := &linkForm{
		protocol: "vmess", remark: s("ps"), address: s("add"), port: port, id: s("id"), security: s("scy"),
		network: firstNonEmpty(s("net"), "tcp"), host: s("host"), path: s("path"), headerType: s("type"),
		tls: "none", sni: s("sni"), fp: s("fp"),
	}
	if s("tls") == "tls" {
		f.tls = "tls"
	}
	if f.network == "grpc" {
		f.serviceName = f.path
	}
	if a := s("alpn"); a != "" {
		f.alpn = strings.Split(a, ",")
	}
	if f.address == "" || f.port == 0 {
		return nil, errors.New("link has no valid address or port")
	}
	return f, nil
}
