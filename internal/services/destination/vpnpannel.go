// Package destination pushes generated configs to the vpn-pannel admin
// (github.com/vefghmhassan/vpn-pannel), the panel the mobile app reads from.
package destination

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
	"sync"
	"time"
)

// VPNPanel uses the admin routes of vpn-pannel:
//
//	POST /login                      form email, password → admin_token cookie
//	POST /admin/v2ray/new            form mode=link, links, base_name, country, ads → 302
//	POST /admin/v2ray/replace-address JSON old_address, new_address, … → JSON result
//
// The JWT from the cookie is replayed as a Bearer token, which the panel's
// AuthRequired middleware also accepts.
type VPNPanel struct {
	BaseURL  string
	Email    string
	Password string
	HTTP     *http.Client

	mu    sync.Mutex
	token string
}

func NewVPNPanel(base, email, password string) *VPNPanel {
	return &VPNPanel{
		BaseURL: strings.TrimRight(base, "/"), Email: email, Password: password,
		HTTP: &http.Client{
			Timeout: 120 * time.Second,
			// Redirects carry the result (302 to the list page on success,
			// to /login on auth failure), so they are inspected, not followed.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

var ErrAuth = errors.New("vpn-pannel rejected the credentials")

func (v *VPNPanel) login(ctx context.Context) (string, error) {
	form := url.Values{"email": {v.Email}, "password": {v.Password}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, v.BaseURL+"/login", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := v.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	for _, c := range resp.Cookies() {
		if c.Name == "admin_token" && c.Value != "" {
			return c.Value, nil
		}
	}
	return "", fmt.Errorf("%w (HTTP %d)", ErrAuth, resp.StatusCode)
}

func (v *VPNPanel) authToken(ctx context.Context, refresh bool) (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.token != "" && !refresh {
		return v.token, nil
	}
	t, err := v.login(ctx)
	if err != nil {
		return "", err
	}
	v.token = t
	return t, nil
}

// do sends the request, logging in again once if the session has expired
// (the middleware answers with a redirect to /login).
func (v *VPNPanel) do(ctx context.Context, build func(token string) (*http.Request, error)) (*http.Response, []byte, error) {
	for attempt := 0; attempt < 2; attempt++ {
		tok, err := v.authToken(ctx, attempt > 0)
		if err != nil {
			return nil, nil, err
		}
		req, err := build(tok)
		if err != nil {
			return nil, nil, err
		}
		resp, err := v.HTTP.Do(req)
		if err != nil {
			return nil, nil, err
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if isLoginRedirect(resp) || resp.StatusCode == http.StatusUnauthorized {
			continue
		}
		return resp, body, nil
	}
	return nil, nil, ErrAuth
}

func isLoginRedirect(resp *http.Response) bool {
	if resp.StatusCode < 300 || resp.StatusCode >= 400 {
		return false
	}
	loc := resp.Header.Get("Location")
	return strings.HasSuffix(strings.SplitN(loc, "?", 2)[0], "/login")
}

// Test checks the credentials.
func (v *VPNPanel) Test(ctx context.Context) error {
	_, err := v.authToken(ctx, true)
	return err
}

type UploadRequest struct {
	Links    []string
	BaseName string
	Country  string
	Ads      bool
}

// Upload creates one V2RayNode per link.
func (v *VPNPanel) Upload(ctx context.Context, in UploadRequest) error {
	if len(in.Links) == 0 {
		return errors.New("no links to upload")
	}
	form := url.Values{
		"mode":      {"link"},
		"links":     {strings.Join(in.Links, "\n")},
		"base_name": {in.BaseName},
		"country":   {strings.ToUpper(in.Country)},
	}
	if in.Ads {
		form.Set("ads", "on")
	}
	resp, body, err := v.do(ctx, func(tok string) (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, v.BaseURL+"/admin/v2ray/new", strings.NewReader(form.Encode()))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Authorization", "Bearer "+tok)
		return req, nil
	})
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return nil
	}
	return fmt.Errorf("vpn-pannel upload: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
}

type ReplaceResult struct {
	Updated int      `json:"updated"`
	Skipped int      `json:"skipped"`
	IDs     []uint   `json:"ids"`
	Errors  []string `json:"errors"`
}

// ErrNoNodes means vpn-pannel had no configs on the old address.
var ErrNoNodes = errors.New("vpn-pannel has no configs on that address")

// ReplaceAddress moves every config from oldIP to newIP in one transaction on
// the panel side.
func (v *VPNPanel) ReplaceAddress(ctx context.Context, oldIP, newIP, country string) (*ReplaceResult, error) {
	payload, _ := json.Marshal(map[string]any{
		"old_address": oldIP, "new_address": newIP, "country": strings.ToUpper(country), "activate": "true",
	})
	resp, body, err := v.do(ctx, func(tok string) (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, v.BaseURL+"/admin/v2ray/replace-address", bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Authorization", "Bearer "+tok)
		return req, nil
	})
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrNoNodes
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("vpn-pannel replace-address: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out ReplaceResult
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("vpn-pannel replace-address: %w", err)
	}
	return &out, nil
}

// SetActive enables or disables every config on an address (used while a
// filtered server waits for its new IP).
func (v *VPNPanel) SetActive(ctx context.Context, address string, active bool) error {
	payload, _ := json.Marshal(map[string]any{"address": address, "active": fmt.Sprint(active)})
	resp, body, err := v.do(ctx, func(tok string) (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, v.BaseURL+"/admin/v2ray/bulk-active", bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Authorization", "Bearer "+tok)
		return req, nil
	})
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("vpn-pannel bulk-active: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}
