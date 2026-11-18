package xui

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

func testClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	host, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	p, _ := strconv.Atoi(port)
	return New("http", host, p, "/bp", "tok", nil)
}

func TestScanRealityTargetSendsForm(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bp/panel/api/server/scanRealityTarget" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.FormValue("target") != "www.microsoft.com:443" || r.FormValue("sni") != "www.microsoft.com" || r.FormValue("xver") != "1" {
			t.Errorf("form = %v", r.PostForm)
		}
		w.Write([]byte(`{"success":true,"obj":{"target":"www.microsoft.com:443","feasible":true,"tlsVersion":"1.3","serverNames":["www.microsoft.com"],"latencyMs":42}}`))
	})
	res, err := c.ScanRealityTarget(context.Background(), "www.microsoft.com:443", "www.microsoft.com", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Feasible || res.TLSVersion != "1.3" || res.LatencyMs != 42 || len(res.ServerNames) != 1 {
		t.Fatalf("res = %+v", res)
	}
}

func TestScanRealityTargetsEmptyUsesDefaults(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if _, ok := r.PostForm["targets"]; ok {
			t.Errorf("targets sent for an empty list: %v", r.PostForm)
		}
		w.Write([]byte(`{"success":true,"obj":[{"target":"a:443","feasible":true},{"target":"b:443","reason":"no h2"}]}`))
	})
	res, err := c.ScanRealityTargets(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 || !res[0].Feasible || res[1].Reason != "no h2" {
		t.Fatalf("res = %+v", res)
	}
}

func TestScanRealityOldPanel(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	if _, err := c.ScanRealityTargets(context.Background(), "x.com"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}
