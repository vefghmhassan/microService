package checker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// CheckHost uses https://check-host.net/about/api with its Iranian nodes.
type CheckHost struct {
	BaseURL string
	Nodes   []string
	// ControlNodes sit outside Iran. They tell "filtered" apart from "down"
	// without trusting this machine's own network, which may be behind a VPN.
	ControlNodes []string
	HTTP         *http.Client
	// PollEvery and Wait bound how long results are awaited.
	PollEvery time.Duration
	Wait      time.Duration
}

// IranNodes are check-host.net's Iranian nodes (there is no ir6).
var IranNodes = []string{
	"ir1.node.check-host.net", "ir2.node.check-host.net", "ir3.node.check-host.net",
	"ir4.node.check-host.net", "ir5.node.check-host.net", "ir7.node.check-host.net",
}

// ControlNodes are European nodes near the usual server locations.
var ControlNodes = []string{"de1.node.check-host.net", "nl1.node.check-host.net", "se1.node.check-host.net"}

func NewCheckHost() *CheckHost {
	return &CheckHost{
		BaseURL: "https://check-host.net", Nodes: IranNodes, ControlNodes: ControlNodes,
		HTTP:      &http.Client{Timeout: 20 * time.Second},
		PollEvery: 3 * time.Second, Wait: 75 * time.Second,
	}
}

func (c *CheckHost) Name() string { return "check-host" }

func (c *CheckHost) get(ctx context.Context, u string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return json.Unmarshal(body, out)
}

func (c *CheckHost) Check(ctx context.Context, host string, port int) ([]Probe, error) {
	q := url.Values{}
	q.Set("host", net.JoinHostPort(host, strconv.Itoa(port)))
	nodes := append(append([]string{}, c.Nodes...), c.ControlNodes...)
	q.Set("max_nodes", strconv.Itoa(len(nodes)))
	for _, n := range nodes {
		q.Add("node", n)
	}
	var start struct {
		OK        int                 `json:"ok"`
		RequestID string              `json:"request_id"`
		Nodes     map[string][]string `json:"nodes"`
		Error     string              `json:"error"`
	}
	if err := c.get(ctx, c.BaseURL+"/check-tcp?"+q.Encode(), &start); err != nil {
		return nil, err
	}
	if start.RequestID == "" {
		return nil, fmt.Errorf("no request id (%s)", start.Error)
	}

	// Node info: [country code, country, city, ip, ASN].
	info := start.Nodes
	results := map[string]json.RawMessage{}
	deadline := time.Now().Add(c.Wait)
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(c.PollEvery):
		}
		if err := c.get(ctx, c.BaseURL+"/check-result/"+start.RequestID, &results); err != nil {
			return nil, err
		}
		pending := 0
		for n := range info {
			if r, ok := results[n]; !ok || string(r) == "null" {
				pending++
			}
		}
		if pending == 0 || time.Now().After(deadline) {
			break
		}
	}

	var probes []Probe
	for node, meta := range info {
		p := Probe{Source: c.Name(), Node: strings.TrimSuffix(node, ".node.check-host.net")}
		if len(meta) > 0 {
			p.Country = strings.ToUpper(meta[0])
		}
		if len(meta) > 2 {
			p.City = meta[2]
		}
		if len(meta) > 4 {
			p.ASN = meta[4]
		}
		raw, ok := results[node]
		if !ok || string(raw) == "null" {
			p.Error = "no result"
			p.NoAnswer = true
			probes = append(probes, p)
			continue
		}
		// check-tcp: [{"time":0.12,"address":"1.2.3.4"}] or [{"error":"Connection timed out"}]
		var rows []*struct {
			Time    *float64 `json:"time"`
			Address string   `json:"address"`
			Error   string   `json:"error"`
		}
		if err := json.Unmarshal(raw, &rows); err != nil || len(rows) == 0 || rows[0] == nil {
			p.Error = "unreadable result"
			p.NoAnswer = true
		} else if rows[0].Error != "" {
			p.Error = rows[0].Error
		} else if rows[0].Time != nil {
			p.OK = true
			p.LatencyMs = *rows[0].Time * 1000
		} else {
			p.Error = "empty result"
			p.NoAnswer = true
		}
		probes = append(probes, p)
	}
	return probes, nil
}
