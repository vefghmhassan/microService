package checker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Globalping (https://globalping.io) has probes on Iranian consumer networks
// (MCI, TCI, Rightel, …), which check-host's datacenter nodes do not cover.
type Globalping struct {
	BaseURL   string
	Token     string
	Limit     int
	HTTP      *http.Client
	PollEvery time.Duration
	Wait      time.Duration
}

func NewGlobalping(token string, limit int) *Globalping {
	if limit <= 0 {
		limit = 8
	}
	return &Globalping{
		BaseURL: "https://api.globalping.io/v1", Token: token, Limit: limit,
		HTTP:      &http.Client{Timeout: 20 * time.Second},
		PollEvery: time.Second, Wait: 40 * time.Second,
	}
}

func (g *Globalping) Name() string { return "globalping" }

func (g *Globalping) req(ctx context.Context, method, u string, in, out any) (int, error) {
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	r, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return 0, err
	}
	r.Header.Set("Accept", "application/json")
	if in != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	if g.Token != "" {
		r.Header.Set("Authorization", "Bearer "+g.Token)
	}
	resp, err := g.HTTP.Do(r)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return resp.StatusCode, err
		}
	}
	return resp.StatusCode, nil
}

func (g *Globalping) Check(ctx context.Context, host string, port int) ([]Probe, error) {
	in := map[string]any{
		"type":   "ping",
		"target": host,
		"locations": []map[string]any{
			{"country": "IR", "limit": g.Limit},
		},
		"measurementOptions": map[string]any{"protocol": "TCP", "port": port, "packets": 3},
	}
	var created struct {
		ID string `json:"id"`
	}
	if _, err := g.req(ctx, http.MethodPost, g.BaseURL+"/measurements", in, &created); err != nil {
		return nil, err
	}

	type gpResult struct {
		Probe struct {
			Country string `json:"country"`
			City    string `json:"city"`
			ASN     int    `json:"asn"`
			Network string `json:"network"`
		} `json:"probe"`
		Result struct {
			Status string `json:"status"`
			Stats  *struct {
				Avg  *float64 `json:"avg"`
				Loss float64  `json:"loss"`
			} `json:"stats"`
			RawOutput string `json:"rawOutput"`
		} `json:"result"`
	}
	var m struct {
		Status  string     `json:"status"`
		Results []gpResult `json:"results"`
	}
	deadline := time.Now().Add(g.Wait)
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(g.PollEvery):
		}
		if _, err := g.req(ctx, http.MethodGet, g.BaseURL+"/measurements/"+created.ID, nil, &m); err != nil {
			return nil, err
		}
		if m.Status != "in-progress" || time.Now().After(deadline) {
			break
		}
	}

	probes := make([]Probe, 0, len(m.Results))
	for i, r := range m.Results {
		p := Probe{
			Source: g.Name(), Node: "gp-" + strconv.Itoa(i+1),
			Country: strings.ToUpper(r.Probe.Country), City: r.Probe.City,
			Network: r.Probe.Network,
		}
		if r.Probe.ASN > 0 {
			p.ASN = "AS" + strconv.Itoa(r.Probe.ASN)
		}
		switch {
		case r.Result.Status != "finished":
			// in-progress / offline / failed: the probe did not measure.
			p.Error = r.Result.Status
			p.NoAnswer = true
		case r.Result.Stats == nil || r.Result.Stats.Loss >= 100:
			p.Error = "no TCP handshake"
		default:
			p.OK = true
			if r.Result.Stats.Avg != nil {
				p.LatencyMs = *r.Result.Stats.Avg
			}
		}
		probes = append(probes, p)
	}
	return probes, nil
}
