package providers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeHetzner records the calls of a primary-IP swap and answers like the API.
type fakeHetzner struct {
	mu      sync.Mutex
	calls   []string
	status  string
	ipID    int64
	ip      string
	deleted []string
}

func (f *fakeHetzner) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	call := r.Method + " " + r.URL.Path
	if r.Method != http.MethodGet {
		f.calls = append(f.calls, call)
	}
	w.Header().Set("Content-Type", "application/json")
	action := `{"action":{"id":7,"status":"running"}}`
	switch {
	case call == "GET /servers/42":
		json.NewEncoder(w).Encode(map[string]any{"server": map[string]any{
			"id": 42, "name": "srv", "status": f.status,
			"public_net": map[string]any{"ipv4": map[string]any{"id": f.ipID, "ip": f.ip}},
			"datacenter": map[string]any{"location": map[string]any{"name": "fsn1"}},
		}})
	case call == "GET /actions/7":
		io.WriteString(w, `{"action":{"id":7,"status":"success"}}`)
	case call == "POST /servers/42/actions/poweroff":
		f.status = "off"
		io.WriteString(w, action)
	case call == "POST /servers/42/actions/poweron":
		f.status = "running"
		io.WriteString(w, action)
	case strings.HasSuffix(call, "/actions/unassign"):
		if f.status != "off" {
			w.WriteHeader(http.StatusLocked)
			io.WriteString(w, `{"error":{"code":"server_not_stopped"}}`)
			return
		}
		f.ipID, f.ip = 0, ""
		io.WriteString(w, action)
	case call == "POST /primary_ips":
		var in map[string]any
		json.Unmarshal(body, &in)
		if f.status != "off" || in["assignee_id"] != float64(42) {
			w.WriteHeader(http.StatusUnprocessableEntity)
			return
		}
		f.ipID, f.ip = 200, "5.6.7.8"
		io.WriteString(w, `{"primary_ip":{"id":200,"ip":"5.6.7.8"},"action":{"id":7,"status":"running"}}`)
	case strings.HasPrefix(call, "DELETE /primary_ips/"):
		f.deleted = append(f.deleted, strings.TrimPrefix(r.URL.Path, "/primary_ips/"))
		w.WriteHeader(http.StatusNoContent)
	case strings.HasPrefix(call, "PUT /primary_ips/"):
		io.WriteString(w, `{}`)
	default:
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"error":{"code":"not_found"}}`)
	}
}

func TestHetznerRotateSwapsPrimaryIPWhilePoweredOff(t *testing.T) {
	pollInterval = time.Millisecond
	f := &fakeHetzner{status: "running", ipID: 100, ip: "1.2.3.4"}
	srv := httptest.NewServer(f)
	defer srv.Close()

	h := NewHetzner(srv.URL, "tok")
	res, err := h.RotateIP(context.Background(), &Instance{Ref: "42"}, RotateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"POST /servers/42/actions/poweroff",
		"POST /primary_ips/100/actions/unassign",
		"POST /primary_ips",
		"POST /servers/42/actions/poweron",
		"DELETE /primary_ips/100",
	}
	if strings.Join(f.calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls:\n%s\nwant:\n%s", strings.Join(f.calls, "\n"), strings.Join(want, "\n"))
	}
	if res.Instance.IPv4 != "5.6.7.8" || res.Instance.IPRef != "200" || res.OldIP != "1.2.3.4" || res.OldKept {
		t.Fatalf("unexpected result %+v / %+v", res, res.Instance)
	}
}

func TestHetznerRotateKeepsOldIP(t *testing.T) {
	pollInterval = time.Millisecond
	f := &fakeHetzner{status: "running", ipID: 100, ip: "1.2.3.4"}
	srv := httptest.NewServer(f)
	defer srv.Close()

	res, err := NewHetzner(srv.URL, "tok").RotateIP(context.Background(), &Instance{Ref: "42"}, RotateOptions{KeepOld: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res.OldKept || len(f.deleted) != 0 {
		t.Fatalf("old IP should be kept, deleted=%v", f.deleted)
	}
	if f.calls[1] != "PUT /primary_ips/100" {
		t.Fatalf("auto_delete must be turned off before unassigning, got %v", f.calls)
	}
}

func TestHetznerRotatePowersBackOnAfterFailure(t *testing.T) {
	pollInterval = time.Millisecond
	f := &fakeHetzner{status: "running", ipID: 100, ip: "1.2.3.4"}
	mux := http.NewServeMux()
	mux.HandleFunc("/primary_ips", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.calls = append(f.calls, "POST /primary_ips")
		f.mu.Unlock()
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, `{"error":{"code":"limit_exceeded"}}`)
	})
	mux.Handle("/", f)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	if _, err := NewHetzner(srv.URL, "tok").RotateIP(context.Background(), &Instance{Ref: "42"}, RotateOptions{}); err == nil {
		t.Fatal("expected error")
	}
	if f.status != "running" {
		t.Fatalf("server left %s after a failed rotation", f.status)
	}
}
