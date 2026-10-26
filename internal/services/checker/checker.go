// Package checker decides whether an IP is reachable from inside Iran.
//
// A TCP connect from Iranian vantage points is the signal. It proves the IP is
// not blackholed by the GFW; it cannot prove a given proxy protocol survives
// DPI, which is why the thresholds are configurable and the verdict separates
// "filtered" (Iran fails, the outside world succeeds) from "down".
package checker

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"
)

// Verdicts.
const (
	Healthy  = "healthy"  // enough Iranian probes connected
	Filtered = "filtered" // reachable from outside, not from Iran
	Down     = "down"     // not reachable from outside either: not a filtering problem
	Unknown  = "unknown"  // too few Iranian probes answered to decide
)

type Probe struct {
	Source  string `json:"source"`
	Node    string `json:"node"`
	Country string `json:"country"`
	City    string `json:"city"`
	ASN     string `json:"asn"`
	Network string `json:"network"`
	OK      bool   `json:"ok"`
	// NoAnswer means the vantage point never reported back. It is left out
	// of the verdict: silence from a busy node says nothing about the IP.
	NoAnswer  bool    `json:"no_answer"`
	LatencyMs float64 `json:"latency_ms"`
	Error     string  `json:"error,omitempty"`
}

type Source interface {
	Name() string
	Check(ctx context.Context, host string, port int) ([]Probe, error)
}

type Policy struct {
	MinOK  int // Iranian probes that must connect
	MinASN int // distinct Iranian networks among them
}

type Result struct {
	Host    string   `json:"host"`
	Port    int      `json:"port"`
	Verdict string   `json:"verdict"`
	OK      bool     `json:"ok"`
	Summary string   `json:"summary"`
	Probes  []Probe  `json:"probes"`
	Errors  []string `json:"errors,omitempty"`
}

type Checker struct {
	Sources []Source
	Policy  Policy
}

// Run queries every source in parallel plus a local control probe from this
// machine (which sits outside Iran).
func (c *Checker) Run(ctx context.Context, host string, port int) *Result {
	res := &Result{Host: host, Port: port}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, s := range c.Sources {
		wg.Add(1)
		go func(s Source) {
			defer wg.Done()
			probes, err := s.Check(ctx, host, port)
			mu.Lock()
			defer mu.Unlock()
			res.Probes = append(res.Probes, probes...)
			if err != nil {
				res.Errors = append(res.Errors, s.Name()+": "+err.Error())
			}
		}(s)
	}
	control := LocalTCP(ctx, host, port)
	wg.Wait()
	res.Probes = append(res.Probes, control)
	Decide(res, c.Policy)
	return res
}

// Decide fills Verdict/OK/Summary from the probes.
func Decide(res *Result, p Policy) {
	if p.MinOK < 1 {
		p.MinOK = 1
	}
	if p.MinASN < 1 {
		p.MinASN = 1
	}
	var irTotal, irOK int
	asns := map[string]bool{}
	// The control is a probe outside Iran. Remote ones (check-host's European
	// nodes) are preferred; the local dial only counts when there are none,
	// because this machine may sit behind a VPN that answers every connect.
	var remoteControl, remoteOK, localSeen, localOK bool
	for _, pr := range res.Probes {
		if pr.NoAnswer {
			continue
		}
		if pr.Source == "local" {
			localSeen, localOK = true, localOK || pr.OK
			continue
		}
		if pr.Country != "IR" {
			remoteControl, remoteOK = true, remoteOK || pr.OK
			continue
		}
		irTotal++
		if pr.OK {
			irOK++
			key := pr.ASN
			if key == "" {
				key = pr.Node
			}
			asns[key] = true
		}
	}
	haveControl, controlOK := remoteControl, remoteOK
	if !remoteControl {
		haveControl, controlOK = localSeen, localOK
	}
	switch {
	case irOK >= p.MinOK && len(asns) >= p.MinASN:
		res.Verdict = Healthy
	case haveControl && !controlOK && irOK == 0:
		res.Verdict = Down
	case irTotal < p.MinOK:
		res.Verdict = Unknown
	default:
		res.Verdict = Filtered
	}
	res.OK = res.Verdict == Healthy
	res.Summary = fmt.Sprintf("%s: %d/%d Iranian probes connected across %d networks (need %d across %d); outside control %s",
		res.Verdict, irOK, irTotal, len(asns), p.MinOK, p.MinASN, okWord(controlOK))
}

func okWord(ok bool) string {
	if ok {
		return "ok"
	}
	return "failed"
}

// LocalTCP is the control probe: a TCP connect from this machine.
func LocalTCP(ctx context.Context, host string, port int) Probe {
	p := Probe{Source: "local", Node: "panel", Country: "--"}
	d := net.Dialer{Timeout: 8 * time.Second}
	start := time.Now()
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		p.Error = err.Error()
		return p
	}
	_ = conn.Close()
	p.OK = true
	p.LatencyMs = float64(time.Since(start).Microseconds()) / 1000
	return p
}
