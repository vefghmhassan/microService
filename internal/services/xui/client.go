// Package xui is a client for the 3x-ui panel API (v3.x, bearer-token auth).
//
// Requests normally travel through the SSH connection to the server, reaching
// the panel on the server's own loopback, so the panel never has to be exposed
// to the internet and the token never crosses the wire in clear text.
package xui

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	BaseURL string // e.g. http://127.0.0.1:2053/abc
	Token   string
	HTTP    *http.Client
}

// New builds a client; dial, when set, replaces the network dialer (for the
// SSH tunnel).
func New(scheme, host string, port int, basePath, token string, dial func(ctx context.Context, network, addr string) (net.Conn, error)) *Client {
	if scheme == "" {
		scheme = "http"
	}
	bp := "/" + strings.Trim(basePath, "/")
	if bp == "/" {
		bp = ""
	}
	tr := &http.Transport{
		// The panel certificate, when there is one, is self-signed or issued
		// for an IP that just changed; the SSH tunnel is what authenticates it.
		TLSClientConfig:     &tls.Config{InsecureSkipVerify: true},
		DisableKeepAlives:   false,
		MaxIdleConnsPerHost: 2,
	}
	if dial != nil {
		tr.DialContext = dial
	}
	return &Client{
		BaseURL: fmt.Sprintf("%s://%s%s", scheme, net.JoinHostPort(host, strconv.Itoa(port)), bp),
		Token:   token,
		HTTP:    &http.Client{Transport: tr, Timeout: 90 * time.Second},
	}
}

type envelope struct {
	Success bool            `json:"success"`
	Msg     string          `json:"msg"`
	Obj     json.RawMessage `json:"obj"`
}

// call sends JSON (in is any) or a form (in is url.Values) and decodes obj.
func (c *Client) call(ctx context.Context, method, path string, in any, out any) error {
	var body io.Reader
	ctype := ""
	switch v := in.(type) {
	case nil:
	case url.Values:
		body = strings.NewReader(v.Encode())
		ctype = "application/x-www-form-urlencoded"
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
		ctype = "application/json"
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/json")
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("%w: %s %s", ErrNotFound, method, path)
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("3x-ui %s %s: HTTP %d: %s", method, path, resp.StatusCode, truncate(string(raw), 300))
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("3x-ui %s %s: unexpected response: %s", method, path, truncate(string(raw), 200))
	}
	if !env.Success {
		return fmt.Errorf("3x-ui %s %s: %s", method, path, env.Msg)
	}
	if out != nil && len(env.Obj) > 0 && string(env.Obj) != "null" {
		return json.Unmarshal(env.Obj, out)
	}
	return nil
}

var ErrNotFound = errors.New("3x-ui endpoint not found")

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// JSONText is a field 3x-ui may send as a JSON string or as a nested object.
// It always holds the JSON text.
type JSONText string

func (j *JSONText) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*j = JSONText(s)
		return nil
	}
	*j = JSONText(b)
	return nil
}

type Inbound struct {
	ID             int      `json:"id,omitempty"`
	Remark         string   `json:"remark"`
	Enable         bool     `json:"enable"`
	Listen         string   `json:"listen"`
	Port           int      `json:"port"`
	Protocol       string   `json:"protocol"`
	Settings       JSONText `json:"settings"`
	StreamSettings JSONText `json:"streamSettings"`
	Sniffing       JSONText `json:"sniffing"`
	Tag            string   `json:"tag,omitempty"`
	ExpiryTime     int64    `json:"expiryTime"`
	Total          int64    `json:"total"`
	// Read-only counters; never sent back (see MarshalJSON).
	Up          int64        `json:"up"`
	Down        int64        `json:"down"`
	ClientStats []ClientStat `json:"clientStats"`
}

// ClientStat is 3x-ui's per-client traffic row. Up/Down are cumulative and
// persisted in the panel's database.
type ClientStat struct {
	Email      string `json:"email"`
	Enable     bool   `json:"enable"`
	Up         int64  `json:"up"`
	Down       int64  `json:"down"`
	Total      int64  `json:"total"`
	ExpiryTime int64  `json:"expiryTime"`
	LastOnline int64  `json:"lastOnline"`
}

// MarshalJSON sends the nested settings as strings, the form every 3x-ui
// version accepts.
func (in Inbound) MarshalJSON() ([]byte, error) {
	type alias struct {
		ID             int    `json:"id,omitempty"`
		Remark         string `json:"remark"`
		Enable         bool   `json:"enable"`
		Listen         string `json:"listen"`
		Port           int    `json:"port"`
		Protocol       string `json:"protocol"`
		Settings       string `json:"settings"`
		StreamSettings string `json:"streamSettings"`
		Sniffing       string `json:"sniffing"`
		ExpiryTime     int64  `json:"expiryTime"`
		Total          int64  `json:"total"`
	}
	return json.Marshal(alias{in.ID, in.Remark, in.Enable, in.Listen, in.Port, in.Protocol,
		string(in.Settings), string(in.StreamSettings), string(in.Sniffing), in.ExpiryTime, in.Total})
}

func (c *Client) ListInbounds(ctx context.Context) ([]Inbound, error) {
	var out []Inbound
	err := c.call(ctx, http.MethodGet, "/panel/api/inbounds/list", nil, &out)
	return out, err
}

func (c *Client) AddInbound(ctx context.Context, in Inbound) error {
	return c.call(ctx, http.MethodPost, "/panel/api/inbounds/add", in, nil)
}

func (c *Client) UpdateInbound(ctx context.Context, id int, in Inbound) error {
	in.ID = id
	return c.call(ctx, http.MethodPost, "/panel/api/inbounds/update/"+strconv.Itoa(id), in, nil)
}

func (c *Client) DeleteInbound(ctx context.Context, id int) error {
	return c.call(ctx, http.MethodPost, "/panel/api/inbounds/del/"+strconv.Itoa(id), nil, nil)
}

// XraySetting returns the panel's xray config template as a JSON object.
func (c *Client) XraySetting(ctx context.Context) (map[string]any, error) {
	var raw json.RawMessage
	err := c.call(ctx, http.MethodPost, "/panel/api/xray/", nil, &raw)
	if errors.Is(err, ErrNotFound) {
		// 3x-ui v2 served this outside the API prefix.
		err = c.call(ctx, http.MethodPost, "/panel/xray/", nil, &raw)
	}
	if err != nil {
		return nil, err
	}
	return extractXraySetting(raw)
}

// extractXraySetting unwraps the shapes the endpoint has used: the template
// itself, {"xraySetting": template, ...}, either of them JSON-encoded as a
// string, and xraySetting itself being a string.
func extractXraySetting(raw json.RawMessage) (map[string]any, error) {
	for i := 0; i < 3; i++ {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			raw = json.RawMessage(s)
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, fmt.Errorf("xray setting: %w", err)
		}
		inner, ok := m["xraySetting"]
		if !ok {
			return m, nil
		}
		b, _ := json.Marshal(inner)
		raw = b
	}
	return nil, errors.New("xray setting: unexpected nesting")
}

func (c *Client) UpdateXraySetting(ctx context.Context, setting map[string]any) error {
	b, err := json.MarshalIndent(setting, "", "  ")
	if err != nil {
		return err
	}
	form := url.Values{"xraySetting": {string(b)}}
	err = c.call(ctx, http.MethodPost, "/panel/api/xray/update", form, nil)
	if errors.Is(err, ErrNotFound) {
		err = c.call(ctx, http.MethodPost, "/panel/xray/update", form, nil)
	}
	return err
}

// DisableSubscription turns off 3x-ui's subscription server. Settings can only
// be written as a whole, so the current set is read, changed and sent back.
// It reports whether anything changed (the panel then needs a restart).
func (c *Client) DisableSubscription(ctx context.Context) (bool, error) {
	var all map[string]any
	if err := c.call(ctx, http.MethodPost, "/panel/api/setting/all", nil, &all); err != nil {
		return false, err
	}
	if v, ok := all["subEnable"].(bool); ok && !v {
		return false, nil
	}
	all["subEnable"] = false
	if err := c.call(ctx, http.MethodPost, "/panel/api/setting/update", all, nil); err != nil {
		return false, err
	}
	return true, nil
}

// RestartPanel restarts the 3x-ui process (needed after settings changes).
func (c *Client) RestartPanel(ctx context.Context) error {
	return c.call(ctx, http.MethodPost, "/panel/api/setting/restartPanel", nil, nil)
}

// Onlines returns the emails of clients connected right now.
func (c *Client) Onlines(ctx context.Context) ([]string, error) {
	var out []string
	err := c.call(ctx, http.MethodPost, "/panel/api/clients/onlines", nil, &out)
	if errors.Is(err, ErrNotFound) {
		// 3x-ui v2 kept it under inbounds.
		err = c.call(ctx, http.MethodPost, "/panel/api/inbounds/onlines", nil, &out)
	}
	return out, err
}

func (c *Client) RestartXray(ctx context.Context) error {
	return c.call(ctx, http.MethodPost, "/panel/api/server/restartXrayService", nil, nil)
}

func (c *Client) XrayVersions(ctx context.Context) ([]string, error) {
	var out []string
	err := c.call(ctx, http.MethodGet, "/panel/api/server/getXrayVersion", nil, &out)
	return out, err
}

func (c *Client) InstallXray(ctx context.Context, version string) error {
	return c.call(ctx, http.MethodPost, "/panel/api/server/installXray/"+url.PathEscape(version), nil, nil)
}

type Status struct {
	Xray struct {
		State   string `json:"state"`
		Version string `json:"version"`
		Error   string `json:"errorMsg"`
	} `json:"xray"`
	CPU float64 `json:"cpu"`
	Mem struct {
		Current int64 `json:"current"`
		Total   int64 `json:"total"`
	} `json:"mem"`
	Uptime int64 `json:"uptime"`
	NetIO  struct {
		Up   int64 `json:"up"`
		Down int64 `json:"down"`
	} `json:"netIO"`
	NetTraffic struct {
		Sent int64 `json:"sent"`
		Recv int64 `json:"recv"`
	} `json:"netTraffic"`
}

func (c *Client) Status(ctx context.Context) (*Status, error) {
	var s Status
	err := c.call(ctx, http.MethodGet, "/panel/api/server/status", nil, &s)
	if errors.Is(err, ErrNotFound) {
		err = c.call(ctx, http.MethodPost, "/panel/api/server/status", nil, &s)
	}
	return &s, err
}
