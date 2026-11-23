package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"microserver/internal/models"
)

// A location with PanelJSON on sends vpn-pannel the configs of the 3x-ui
// panel's own JSON subscription (Server.PanelSubJSON) instead of share links:
// exactly what the panel serves, with every variant (Main, Fragment, IPv6,
// CDN, clean IPs) it has for an inbound.

var panelJSONClient = &http.Client{Timeout: 20 * time.Second}

// locationPanelJSON reports whether the server's location uploads the
// panel's JSON. A server without a location (or a missing one) never does.
func (o *Orchestrator) locationPanelJSON(s *models.Server) bool {
	if s.LocationID == 0 {
		return false
	}
	var loc models.Location
	if err := o.DB.Select("id", "panel_json").First(&loc, s.LocationID).Error; err != nil {
		return false
	}
	return loc.PanelJSON
}

// fetchPanelJSON reads the panel's JSON subscription of s: one compact
// (single-line) config per array element. The panel may still be restarting
// after a settings change, so a failed read is retried a few times.
func (o *Orchestrator) fetchPanelJSON(ctx context.Context, s *models.Server) ([]string, error) {
	if s.PanelSubJSON == "" {
		return nil, errors.New("the server has no panel JSON subscription yet")
	}
	var last error
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			if err := sleep(ctx, o.RetryDelay/6); err != nil {
				return nil, err
			}
		}
		out, err := getPanelJSON(ctx, s.PanelSubJSON)
		if err == nil {
			return out, nil
		}
		last = err
	}
	return nil, fmt.Errorf("panel JSON subscription %s: %w", s.PanelSubJSON, last)
}

func getPanelJSON(ctx context.Context, url string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := panelJSONClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return splitPanelJSON(body)
}

// splitPanelJSON turns the subscription body (a JSON array of configs, or a
// single config) into compact one-line configs.
func splitPanelJSON(body []byte) ([]string, error) {
	var arr []json.RawMessage
	if err := json.Unmarshal(body, &arr); err != nil {
		var one json.RawMessage
		if json.Unmarshal(body, &one) != nil {
			return nil, errors.New("the subscription is not JSON")
		}
		arr = []json.RawMessage{one}
	}
	out := make([]string, 0, len(arr))
	for _, raw := range arr {
		var buf bytes.Buffer
		if err := json.Compact(&buf, raw); err != nil {
			return nil, err
		}
		out = append(out, buf.String())
	}
	if len(out) == 0 {
		return nil, errors.New("the subscription has no configs")
	}
	return out, nil
}

// configPort is the server port a client config connects to (0 if unknown).
func configPort(cfg string) int {
	var c struct {
		Outbounds []struct {
			Tag      string `json:"tag"`
			Protocol string `json:"protocol"`
			Settings struct {
				Port    float64 `json:"port"`
				Servers []struct {
					Port float64 `json:"port"`
				} `json:"servers"`
				Vnext []struct {
					Port float64 `json:"port"`
				} `json:"vnext"`
			} `json:"settings"`
		} `json:"outbounds"`
	}
	if json.Unmarshal([]byte(cfg), &c) != nil {
		return 0
	}
	for _, ob := range c.Outbounds {
		if ob.Tag != "proxy" {
			continue
		}
		switch {
		case ob.Settings.Port > 0:
			return int(ob.Settings.Port)
		case len(ob.Settings.Servers) > 0:
			return int(ob.Settings.Servers[0].Port)
		case len(ob.Settings.Vnext) > 0:
			return int(ob.Settings.Vnext[0].Port)
		}
	}
	return 0
}

// panelJSONEntries is what goes to vpn-pannel for a PanelJSON location: the
// panel's configs whose port belongs to an inbound of snap that is enabled
// and marked for upload (snap is already narrowed to the inbounds that
// passed the config test, when there was one). Any other port is left out.
func (o *Orchestrator) panelJSONEntries(ctx context.Context, snap *Snapshot, s *models.Server) ([]string, error) {
	all, err := o.fetchPanelJSON(ctx, s)
	if err != nil {
		return nil, err
	}
	return filterPanelJSON(snap, all), nil
}

func filterPanelJSON(snap *Snapshot, all []string) []string {
	ports := map[int]bool{}
	for _, t := range snap.Inbounds {
		if t.Enabled && t.Upload {
			ports[t.Port] = true
		}
	}
	var out []string
	for _, cfg := range all {
		if ports[configPort(cfg)] {
			out = append(out, cfg)
		}
	}
	return out
}
