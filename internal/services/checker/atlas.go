package checker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Atlas runs a one-off TCP traceroute from RIPE Atlas probes in Iran. Unlike
// check-host it has Iranian probes with IPv6, so it can judge IPv6 servers.
// A probe counts as connected when the target itself answered the SYN.
// It needs an API key with measurement credits.
type Atlas struct {
	BaseURL   string
	Key       string
	Limit     int
	HTTP      *http.Client
	PollEvery time.Duration
	Wait      time.Duration
}

func NewAtlas(key string, limit int) *Atlas {
	if limit <= 0 {
		limit = 8
	}
	return &Atlas{
		BaseURL: "https://atlas.ripe.net/api/v2", Key: key, Limit: limit,
		HTTP:      &http.Client{Timeout: 20 * time.Second},
		PollEvery: 5 * time.Second, Wait: 90 * time.Second,
	}
}

func (a *Atlas) Name() string { return "atlas" }

func (a *Atlas) req(ctx context.Context, method, u string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	r, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return err
	}
	r.Header.Set("Accept", "application/json")
	if in != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	if a.Key != "" {
		r.Header.Set("Authorization", "Key "+a.Key)
	}
	resp, err := a.HTTP.Do(r)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

type atlasResult struct {
	ProbeID int    `json:"prb_id"`
	DstAddr string `json:"dst_addr"`
	Result  []struct {
		Hop    int `json:"hop"`
		Result []struct {
			From string   `json:"from"`
			RTT  *float64 `json:"rtt"`
		} `json:"result"`
	} `json:"result"`
}

// reached reports whether the target answered, and its round-trip time.
func (r atlasResult) reached(target string) (bool, float64) {
	want := net.ParseIP(target)
	for i := len(r.Result) - 1; i >= 0; i-- {
		for _, rep := range r.Result[i].Result {
			if got := net.ParseIP(rep.From); got != nil && want != nil && got.Equal(want) {
				rtt := 0.0
				if rep.RTT != nil {
					rtt = *rep.RTT
				}
				return true, rtt
			}
		}
	}
	return false, 0
}

func (a *Atlas) Check(ctx context.Context, host string, port int) ([]Probe, error) {
	if a.Key == "" {
		return nil, fmt.Errorf("no API key set")
	}
	af := 4
	if ip := net.ParseIP(host); ip != nil && ip.To4() == nil {
		af = 6
	}
	in := map[string]any{
		"definitions": []map[string]any{{
			"target": host, "af": af, "type": "traceroute", "protocol": "TCP", "port": port,
			"description": "microserver reachability " + host, "max_hops": 32, "paris": 0,
		}},
		"probes":    []map[string]any{{"type": "country", "value": "IR", "requested": a.Limit}},
		"is_oneoff": true,
	}
	var created struct {
		Measurements []int64 `json:"measurements"`
	}
	if err := a.req(ctx, http.MethodPost, a.BaseURL+"/measurements/", in, &created); err != nil {
		return nil, err
	}
	if len(created.Measurements) == 0 {
		return nil, fmt.Errorf("no measurement was created")
	}
	id := strconv.FormatInt(created.Measurements[0], 10)

	// A one-off measurement stops once its probes have reported; results
	// that arrive by then are used either way.
	var results []atlasResult
	deadline := time.Now().Add(a.Wait)
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(a.PollEvery):
		}
		var m struct {
			Status struct {
				Name string `json:"name"`
			} `json:"status"`
		}
		if err := a.req(ctx, http.MethodGet, a.BaseURL+"/measurements/"+id+"/", nil, &m); err != nil {
			return nil, err
		}
		stopped := m.Status.Name == "Stopped" || m.Status.Name == "Failed" || m.Status.Name == "No suitable probes"
		if stopped || time.Now().After(deadline) {
			if err := a.req(ctx, http.MethodGet, a.BaseURL+"/measurements/"+id+"/results/?format=json", nil, &results); err != nil {
				return nil, err
			}
			break
		}
	}
	return a.probes(ctx, host, af, results), nil
}

// probes turns results into Probes, naming each probe's network by its ASN.
func (a *Atlas) probes(ctx context.Context, host string, af int, results []atlasResult) []Probe {
	asn := map[int]string{}
	if len(results) > 0 {
		ids := make([]string, 0, len(results))
		for _, r := range results {
			ids = append(ids, strconv.Itoa(r.ProbeID))
		}
		var list struct {
			Results []struct {
				ID    int  `json:"id"`
				ASNv4 *int `json:"asn_v4"`
				ASNv6 *int `json:"asn_v6"`
			} `json:"results"`
		}
		// Without the ASNs every probe still counts, keyed by node.
		if a.req(ctx, http.MethodGet, a.BaseURL+"/probes/?fields=asn_v4,asn_v6&id__in="+strings.Join(ids, ","), nil, &list) == nil {
			for _, p := range list.Results {
				n := p.ASNv4
				if af == 6 && p.ASNv6 != nil {
					n = p.ASNv6
				}
				if n != nil && *n > 0 {
					asn[p.ID] = "AS" + strconv.Itoa(*n)
				}
			}
		}
	}
	out := make([]Probe, 0, len(results))
	for _, r := range results {
		p := Probe{Source: a.Name(), Node: "atlas-" + strconv.Itoa(r.ProbeID), Country: "IR", ASN: asn[r.ProbeID]}
		if ok, rtt := r.reached(host); ok {
			p.OK, p.LatencyMs = true, rtt
		} else {
			p.Error = "no TCP answer from the target"
		}
		out = append(out, p)
	}
	return out
}
