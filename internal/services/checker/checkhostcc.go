package checker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// CheckHostCC uses the documented API of https://check-host.cc/docs. It has two
// Iranian nodes (both Pars Abr) and European ones for the outside control.
type CheckHostCC struct {
	BaseURL string
	Key     string
	// Regions are country codes: Iranian nodes plus a control outside Iran.
	Regions   []string
	HTTP      *http.Client
	PollEvery time.Duration
	Wait      time.Duration
}

func NewCheckHostCC(key string) *CheckHostCC {
	return &CheckHostCC{
		BaseURL: "https://api.check-host.cc", Key: key, Regions: []string{"IR", "DE", "NL"},
		HTTP:      &http.Client{Timeout: 30 * time.Second},
		PollEvery: 2 * time.Second, Wait: 40 * time.Second,
	}
}

func (c *CheckHostCC) Name() string { return "check-host.cc" }

func (c *CheckHostCC) do(ctx context.Context, method, u string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Key != "" {
		req.Header.Set("Authorization", "Bearer "+c.Key)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncateText(strings.TrimSpace(string(raw)), 160))
	}
	return json.Unmarshal(raw, out)
}

func truncateText(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

type ccNode struct {
	CountryCode string `json:"countryCode"`
	City        string `json:"city"`
	ASN         string `json:"asn"`
	Location    string `json:"locationname"`
	Checks      []struct {
		Status         int     `json:"status"`
		ConnectionTime float64 `json:"connectiontime"`
		ErrorText      string  `json:"errortext"`
	} `json:"checks"`
}

func (c *CheckHostCC) Check(ctx context.Context, host string, port int) ([]Probe, error) {
	var start struct {
		Success bool   `json:"success"`
		UUID    string `json:"uuid"`
		Message string `json:"message"`
	}
	in := map[string]any{"target": host, "port": port, "region": c.Regions}
	if err := c.do(ctx, http.MethodPost, c.BaseURL+"/tcp", in, &start); err != nil {
		return nil, err
	}
	if start.UUID == "" {
		return nil, fmt.Errorf("no uuid (%s)", start.Message)
	}

	var rep struct {
		Data map[string]ccNode `json:"data"`
	}
	deadline := time.Now().Add(c.Wait)
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(c.PollEvery):
		}
		rep.Data = nil
		if err := c.do(ctx, http.MethodGet, c.BaseURL+"/report/"+start.UUID, nil, &rep); err != nil {
			return nil, err
		}
		done := len(rep.Data) > 0
		for _, n := range rep.Data {
			if len(n.Checks) == 0 {
				done = false
			}
		}
		if done || time.Now().After(deadline) {
			break
		}
	}

	var probes []Probe
	for name, n := range rep.Data {
		p := Probe{Source: c.Name(), Node: name, Country: strings.ToUpper(n.CountryCode), City: n.City, ASN: n.ASN}
		switch {
		case len(n.Checks) == 0:
			p.Error, p.NoAnswer = "no result", true
		case n.Checks[0].Status == 1:
			p.OK = true
			p.LatencyMs = n.Checks[0].ConnectionTime
		default:
			p.Error = n.Checks[0].ErrorText
			if p.Error == "" {
				p.Error = "connection failed"
			}
		}
		probes = append(probes, p)
	}
	return probes, nil
}
