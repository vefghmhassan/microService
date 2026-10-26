package checker

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDecide(t *testing.T) {
	ir := func(asn string, ok bool) Probe { return Probe{Source: "x", Country: "IR", ASN: asn, OK: ok} }
	local := func(ok bool) Probe { return Probe{Source: "local", OK: ok} }
	p := Policy{MinOK: 2, MinASN: 2}
	cases := []struct {
		name   string
		probes []Probe
		want   string
	}{
		{"healthy", []Probe{ir("AS1", true), ir("AS2", true), local(true)}, Healthy},
		{"same network twice is not enough", []Probe{ir("AS1", true), ir("AS1", true), ir("AS2", false), local(true)}, Filtered},
		{"filtered", []Probe{ir("AS1", false), ir("AS2", false), local(true)}, Filtered},
		{"down everywhere", []Probe{ir("AS1", false), ir("AS2", false), local(false)}, Down},
		{"too few probes", []Probe{ir("AS1", false), local(true)}, Unknown},
		{"unanswered probes are not failures", []Probe{ir("AS1", true), ir("AS2", true), {Source: "x", Country: "IR", NoAnswer: true}, local(true)}, Healthy},
		{"only silence is unknown, not filtered", []Probe{{Source: "x", Country: "IR", NoAnswer: true}, {Source: "x", Country: "IR", NoAnswer: true}, local(true)}, Unknown},
		{"foreign probes are the control", []Probe{{Country: "DE", ASN: "AS9", OK: true}, ir("AS1", false), ir("AS2", false), local(false)}, Filtered},
		{"remote control beats a local VPN that accepts everything", []Probe{{Country: "DE", OK: false}, ir("AS1", false), ir("AS2", false), local(true)}, Down},
	}
	for _, c := range cases {
		r := &Result{Probes: c.probes}
		Decide(r, p)
		if r.Verdict != c.want {
			t.Errorf("%s: got %s want %s (%s)", c.name, r.Verdict, c.want, r.Summary)
		}
	}
}

func TestCheckHostParsesResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/check-tcp"):
			if r.URL.Query().Get("host") != "9.9.9.9:443" {
				t.Errorf("host=%s", r.URL.Query().Get("host"))
			}
			io.WriteString(w, `{"ok":1,"request_id":"abc","nodes":{
				"ir1.node.check-host.net":["ir","Iran","Tehran","1.1.1.1","AS47430"],
				"ir2.node.check-host.net":["ir","Iran","Isfahan","2.2.2.2","AS209279"]}}`)
		case r.URL.Path == "/check-result/abc":
			io.WriteString(w, `{"ir1.node.check-host.net":[{"time":0.081,"address":"9.9.9.9"}],
				"ir2.node.check-host.net":[{"error":"Connection timed out"}]}`)
		}
	}))
	defer srv.Close()
	c := NewCheckHost()
	c.BaseURL, c.PollEvery, c.Wait = srv.URL, time.Millisecond, time.Second
	probes, err := c.Check(context.Background(), "9.9.9.9", 443)
	if err != nil {
		t.Fatal(err)
	}
	ok, failed := 0, 0
	for _, p := range probes {
		if p.Country != "IR" {
			t.Fatalf("country %q", p.Country)
		}
		if p.OK {
			ok++
			if p.ASN != "AS47430" || p.LatencyMs < 80 {
				t.Fatalf("bad probe %+v", p)
			}
		} else {
			failed++
		}
	}
	if ok != 1 || failed != 1 {
		t.Fatalf("ok=%d failed=%d", ok, failed)
	}
}

func TestGlobalpingParsesResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), `"country":"IR"`) || !strings.Contains(string(body), `"protocol":"TCP"`) {
				t.Errorf("request %s", body)
			}
			w.WriteHeader(http.StatusAccepted)
			io.WriteString(w, `{"id":"m1","probesCount":2}`)
			return
		}
		io.WriteString(w, `{"status":"finished","results":[
			{"probe":{"country":"IR","city":"Tehran","asn":197207,"network":"MCI"},"result":{"status":"finished","stats":{"avg":40.5,"loss":0}}},
			{"probe":{"country":"IR","city":"Tehran","asn":58224,"network":"TCI"},"result":{"status":"finished","stats":{"avg":null,"loss":100}}}]}`)
	}))
	defer srv.Close()
	g := NewGlobalping("", 4)
	g.BaseURL, g.PollEvery = srv.URL, time.Millisecond
	probes, err := g.Check(context.Background(), "9.9.9.9", 443)
	if err != nil {
		t.Fatal(err)
	}
	if len(probes) != 2 || !probes[0].OK || probes[1].OK || probes[0].ASN != "AS197207" {
		t.Fatalf("%+v", probes)
	}
}
