// Package cloudflare is a small client for the Cloudflare API v4: list the
// zones a token can see and keep A/TXT records pointed where we want.
//
// The token needs Zone:Read and DNS:Edit on the zones it should manage, and
// Zone Settings:Edit to prepare a zone for CDN inbounds.
package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

const DefaultBaseURL = "https://api.cloudflare.com/client/v4"

// Client talks to one Cloudflare account through an API token, or through
// the legacy Global API Key when AuthKey is set (used only to create a token).
type Client struct {
	BaseURL   string
	Token     string
	AuthEmail string
	AuthKey   string
	HTTP      *http.Client
}

func New(token string) *Client {
	return &Client{BaseURL: DefaultBaseURL, Token: token, HTTP: &http.Client{Timeout: 30 * time.Second}}
}

type Zone struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

type Record struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	Proxied bool   `json:"proxied"`
	TTL     int    `json:"ttl"`
}

// APIError is Cloudflare's own error list, or the HTTP status when there is none.
type APIError struct {
	Status int `json:"-"`
	Errors []struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		// ErrorChain holds the real cause behind a generic error such as
		// 6003 "Invalid request headers".
		ErrorChain []struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error_chain"`
	}
}

func (e *APIError) Error() string {
	var parts []string
	for _, x := range e.Errors {
		parts = append(parts, fmt.Sprintf("%d %s", x.Code, x.Message))
		for _, y := range x.ErrorChain {
			parts = append(parts, fmt.Sprintf("%d %s", y.Code, y.Message))
		}
	}
	if len(parts) == 0 {
		return fmt.Sprintf("cloudflare: HTTP %d", e.Status)
	}
	return "cloudflare: " + strings.Join(parts, "; ")
}

type envelope struct {
	Success    bool            `json:"success"`
	Result     json.RawMessage `json:"result"`
	ResultInfo struct {
		Page       int `json:"page"`
		TotalPages int `json:"total_pages"`
	} `json:"result_info"`
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) (*envelope, error) {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return nil, err
	}
	if c.AuthKey != "" {
		req.Header.Set("X-Auth-Email", c.AuthEmail)
		req.Header.Set("X-Auth-Key", c.AuthKey)
	} else {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil || resp.StatusCode >= 300 || !env.Success {
		ae := &APIError{Status: resp.StatusCode}
		_ = json.Unmarshal(raw, ae)
		return nil, ae
	}
	if out != nil && len(env.Result) > 0 {
		if err := json.Unmarshal(env.Result, out); err != nil {
			return nil, fmt.Errorf("cloudflare: decode %s: %w", path, err)
		}
	}
	return &env, nil
}

// Zones lists every zone the token can read.
func (c *Client) Zones(ctx context.Context) ([]Zone, error) {
	var all []Zone
	for page := 1; ; page++ {
		var zs []Zone
		env, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/zones?per_page=50&page=%d", page), nil, &zs)
		if err != nil {
			return nil, err
		}
		all = append(all, zs...)
		if env.ResultInfo.TotalPages <= page || len(zs) == 0 {
			return all, nil
		}
	}
}

// Records lists the records of one type and exact name.
func (c *Client) Records(ctx context.Context, zoneID, typ, name string) ([]Record, error) {
	q := url.Values{"type": {typ}, "name": {name}, "per_page": {"100"}}
	var rs []Record
	_, err := c.do(ctx, http.MethodGet, "/zones/"+zoneID+"/dns_records?"+q.Encode(), nil, &rs)
	return rs, err
}

// ttl is short for a direct record so a moved address is picked up quickly;
// proxied records must use 1 (automatic).
func ttl(proxied bool) int {
	if proxied {
		return 1
	}
	return 60
}

// AddrType is the record type for ip: "AAAA" for IPv6, else "A".
func AddrType(ip string) string {
	if p := net.ParseIP(ip); p != nil && p.To4() == nil {
		return "AAAA"
	}
	return "A"
}

// UpsertA is UpsertAddr for an IPv4 address.
func (c *Client) UpsertA(ctx context.Context, zoneID, name, ip string, proxied bool) (string, error) {
	return c.UpsertAddr(ctx, zoneID, name, ip, proxied)
}

// UpsertAddr makes name resolve to ip alone: the first A (or, for an IPv6,
// AAAA) record of that name is updated (or one is created), and every other
// A and AAAA record of the same name is removed, so no client is left on an
// old address or the other IP family. It returns the record ID.
func (c *Client) UpsertAddr(ctx context.Context, zoneID, name, ip string, proxied bool) (string, error) {
	typ := AddrType(ip)
	other := map[string]string{"A": "AAAA", "AAAA": "A"}[typ]
	stale, err := c.Records(ctx, zoneID, other, name)
	if err != nil {
		return "", err
	}
	rs, err := c.Records(ctx, zoneID, typ, name)
	if err != nil {
		return "", err
	}
	body := map[string]any{"type": typ, "name": name, "content": ip, "proxied": proxied, "ttl": ttl(proxied)}
	var rec Record
	var errs []error
	for _, r := range stale {
		if err := c.DeleteRecord(ctx, zoneID, r.ID); err != nil {
			errs = append(errs, err)
		}
	}
	if len(rs) == 0 {
		if _, err := c.do(ctx, http.MethodPost, "/zones/"+zoneID+"/dns_records", body, &rec); err != nil {
			return "", err
		}
		return rec.ID, errors.Join(errs...)
	}
	keep := rs[0]
	for _, r := range rs {
		if r.Content == ip {
			keep = r
		}
	}
	if keep.Content != ip || keep.Proxied != proxied {
		if _, err := c.do(ctx, http.MethodPatch, "/zones/"+zoneID+"/dns_records/"+keep.ID, body, &rec); err != nil {
			return "", err
		}
	}
	for _, r := range rs {
		if r.ID != keep.ID {
			if err := c.DeleteRecord(ctx, zoneID, r.ID); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return keep.ID, errors.Join(errs...)
}

// CreateTXT adds a TXT record (used for ACME DNS-01) and returns its ID.
func (c *Client) CreateTXT(ctx context.Context, zoneID, name, value string) (string, error) {
	var rec Record
	_, err := c.do(ctx, http.MethodPost, "/zones/"+zoneID+"/dns_records",
		map[string]any{"type": "TXT", "name": name, "content": value, "ttl": 60}, &rec)
	return rec.ID, err
}

func (c *Client) DeleteRecord(ctx context.Context, zoneID, id string) error {
	_, err := c.do(ctx, http.MethodDelete, "/zones/"+zoneID+"/dns_records/"+id, nil, nil)
	return err
}

// LooksLikeGlobalKey reports whether k has the shape of a Global API Key
// (37 hex characters); API tokens are 40 characters and not hex-only.
func LooksLikeGlobalKey(k string) bool {
	if len(k) != 37 {
		return false
	}
	for _, r := range k {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}

// ZoneScope is the permission-group scope of zone-level permissions.
const ZoneScope = "com.cloudflare.api.account.zone"

// TokenPermissions are the permission groups a panel token gets, by the
// names Cloudflare lists them under. Zone Settings Write lets the panel turn
// on WebSockets and strict SSL for CDN inbounds.
var TokenPermissions = []string{"Zone Read", "DNS Write", "Zone Settings Write"}

type permissionGroup struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	Scopes []string `json:"scopes"`
}

// CreateToken makes a user API token with Zone:Read and DNS:Edit on every
// zone and returns its secret. The client must be allowed to create tokens:
// the Global API Key (AuthEmail + AuthKey) or a token with API Tokens:Edit.
func (c *Client) CreateToken(ctx context.Context, name string) (string, error) {
	var groups []permissionGroup
	if _, err := c.do(ctx, http.MethodGet, "/user/tokens/permission_groups", nil, &groups); err != nil {
		return "", err
	}
	var picked []map[string]string
	for _, want := range TokenPermissions {
		id := ""
		for _, g := range groups {
			if g.Name == want && slices.Contains(g.Scopes, ZoneScope) {
				id = g.ID
				break
			}
		}
		if id == "" {
			return "", fmt.Errorf("cloudflare: permission group %q not found", want)
		}
		picked = append(picked, map[string]string{"id": id})
	}
	body := map[string]any{
		"name": name,
		"policies": []map[string]any{{
			"effect":            "allow",
			"resources":         map[string]string{ZoneScope + ".*": "*"},
			"permission_groups": picked,
		}},
	}
	var out struct {
		Value string `json:"value"`
	}
	if _, err := c.do(ctx, http.MethodPost, "/user/tokens", body, &out); err != nil {
		return "", err
	}
	if out.Value == "" {
		return "", errors.New("cloudflare: the new token came back empty")
	}
	return out.Value, nil
}

// SetZoneSetting changes one zone setting, e.g. "grpc" → "on".
func (c *Client) SetZoneSetting(ctx context.Context, zoneID, key string, value any) error {
	_, err := c.do(ctx, http.MethodPatch, "/zones/"+zoneID+"/settings/"+key, map[string]any{"value": value}, nil)
	return err
}

// EnsureCDNSettings makes a zone ready for ws/grpc/xhttp inbounds behind the
// proxy: WebSockets on, TLS 1.2+, and SSL "strict" when the origin has a real
// certificate for the zone (else "full", which accepts any). gRPC has no
// zone setting in the API ("1006 Unrecognized zone setting name"); it is
// switched on by hand under Network → gRPC. Every setting is tried; the
// errors are joined.
func (c *Client) EnsureCDNSettings(ctx context.Context, zoneID string, strict bool) error {
	ssl := "full"
	if strict {
		ssl = "strict"
	}
	var errs []error
	for _, s := range []struct {
		key   string
		value string
	}{{"websockets", "on"}, {"min_tls_version", "1.2"}, {"ssl", ssl}} {
		if err := c.SetZoneSetting(ctx, zoneID, s.key, s.value); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", s.key, err))
		}
	}
	return errors.Join(errs...)
}
