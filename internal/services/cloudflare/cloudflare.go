// Package cloudflare is a small client for the Cloudflare API v4: list the
// zones a token can see and keep A/TXT records pointed where we want.
//
// The token needs Zone:Read and DNS:Edit on the zones it should manage.
package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const DefaultBaseURL = "https://api.cloudflare.com/client/v4"

// Client talks to one Cloudflare account through an API token.
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
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
	}
}

func (e *APIError) Error() string {
	var parts []string
	for _, x := range e.Errors {
		parts = append(parts, fmt.Sprintf("%d %s", x.Code, x.Message))
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
	req.Header.Set("Authorization", "Bearer "+c.Token)
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

// UpsertA makes name resolve to ip alone: the first A record of that name is
// updated (or one is created) and any other A record of the same name is
// removed, so no client is left on an old address. It returns the record ID.
func (c *Client) UpsertA(ctx context.Context, zoneID, name, ip string, proxied bool) (string, error) {
	rs, err := c.Records(ctx, zoneID, "A", name)
	if err != nil {
		return "", err
	}
	body := map[string]any{"type": "A", "name": name, "content": ip, "proxied": proxied, "ttl": ttl(proxied)}
	var rec Record
	if len(rs) == 0 {
		if _, err := c.do(ctx, http.MethodPost, "/zones/"+zoneID+"/dns_records", body, &rec); err != nil {
			return "", err
		}
		return rec.ID, nil
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
	var errs []error
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
