package providers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeXZone answers like the X-ZoneServers API for one product and one VPS.
type fakeXZone struct {
	mu       sync.Mutex
	calls    []string
	order    map[string]any
	idemKey  string
	settled  bool
	states   []string // successive "status/instance.state" answers for GET /vps/v1
	failure  bool
	scopes   []string
	vpsFound bool
}

func (f *fakeXZone) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	call := r.Method + " " + r.URL.Path
	if r.Method != http.MethodGet {
		f.calls = append(f.calls, call)
	}
	ok := func(data any) { json.NewEncoder(w).Encode(map[string]any{"success": true, "data": data}) }
	switch call {
	case "GET /":
		ok(map[string]any{"key": map[string]any{"scopes": f.scopes}})
	case "GET /products/p1":
		ok(map[string]any{
			"product": map[string]any{"id": "p1"}, "default_os_image_id": "img-deb",
			"os_images": []any{
				map[string]any{"id": "img-deb", "name": "Debian 12"},
				map[string]any{"id": "img-ubu", "name": "Ubuntu 24.04 LTS"},
			},
		})
	case "POST /orders":
		json.Unmarshal(body, &f.order)
		f.idemKey = r.Header.Get("Idempotency-Key")
		w.WriteHeader(http.StatusCreated)
		doc := any(nil)
		if !f.settled {
			doc = map[string]any{"type": "proforma", "id": "pf1", "status": "unpaid", "settled": false}
		}
		ok(map[string]any{
			"order":    map[string]any{"id": "o1"},
			"services": []any{map[string]any{"id": "v1", "status": "pending_payment"}},
			"document": doc, "settled": f.settled,
		})
	case "POST /proformas/pf1/pay":
		ok(map[string]any{"proformaPaid": true})
	case "GET /vps/v1":
		if !f.vpsFound {
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"success":false,"code":"NOT_FOUND","error":"not found"}`)
			return
		}
		st := f.states[0]
		if len(f.states) > 1 {
			f.states = f.states[1:]
		}
		status, state, _ := strings.Cut(st, "/")
		v := map[string]any{
			"id": "v1", "hostname": "srv", "status": status, "location": "Bucharest",
			"primary_ip": nil, "instance": nil,
			"ip_addresses": []any{
				map[string]any{"address": "2001:db8::5", "version": 6},
				map[string]any{"address": "203.0.113.10", "version": 4},
			},
		}
		if state != "" {
			v["instance"] = map[string]any{"state": state}
		}
		if f.failure {
			v["status"] = "provisioning_failed"
			v["failure"] = map[string]any{"code": "ip_capacity", "message": "No free address"}
		}
		ok(v)
	case "DELETE /vps/v1":
		json.Unmarshal(body, &f.order)
		ok(map[string]any{"immediate": true})
	default:
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"success":false,"code":"NOT_FOUND"}`)
	}
}

func TestXZoneCreatePaysUnsettledDocument(t *testing.T) {
	f := &fakeXZone{}
	srv := httptest.NewServer(f)
	defer srv.Close()

	inst, err := NewXZone(srv.URL, "tok").Create(context.Background(), CreateRequest{
		Name: "srv 1", Region: "romania", Plan: "p1", SSHPublicKey: "ssh-ed25519 AAAA k", UserData: "#cloud-config\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "POST /orders\nPOST /proformas/pf1/pay"
	if got := strings.Join(f.calls, "\n"); got != want {
		t.Fatalf("calls:\n%s\nwant:\n%s", got, want)
	}
	if f.order["withdrawalWaiverAccepted"] != true || f.order["acceptTerms"] != true {
		t.Fatalf("order must accept terms and the withdrawal waiver: %v", f.order)
	}
	if f.idemKey == "" {
		t.Fatal("no Idempotency-Key header")
	}
	cfg := f.order["configOptions"].(map[string]any)
	if cfg["os_image_id"] != "img-deb" || f.order["billingCycle"] != "hourly" || f.order["location"] != "romania" || f.order["hostname"] != "srv-1" {
		t.Fatalf("unexpected order body %v", f.order)
	}
	if inst.Ref != "v1" {
		t.Fatalf("ref = %q", inst.Ref)
	}
}

func TestXZoneCreateSettledResolvesImageByName(t *testing.T) {
	f := &fakeXZone{settled: true}
	srv := httptest.NewServer(f)
	defer srv.Close()

	if _, err := NewXZone(srv.URL, "tok").Create(context.Background(), CreateRequest{Name: "srv", Plan: "p1", Image: "ubuntu-24.04"}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(f.calls, "\n"); got != "POST /orders" {
		t.Fatalf("settled order should not be paid, calls: %s", got)
	}
	if id := f.order["configOptions"].(map[string]any)["os_image_id"]; id != "img-ubu" {
		t.Fatalf("os_image_id = %v", id)
	}
	if _, err := NewXZone(srv.URL, "tok").Create(context.Background(), CreateRequest{Name: "srv", Plan: "p1", Image: "Arch"}); err == nil || !strings.Contains(err.Error(), "Ubuntu 24.04 LTS") {
		t.Fatalf("unknown image should list choices, got %v", err)
	}
}

func TestXZoneWaitRunningMapsStatus(t *testing.T) {
	pollInterval = time.Millisecond
	f := &fakeXZone{vpsFound: true, states: []string{"pending", "active/creating", "active/running"}}
	srv := httptest.NewServer(f)
	defer srv.Close()

	inst, err := WaitRunning(context.Background(), NewXZone(srv.URL, "tok"), "v1", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if inst.Status != "running" || inst.IPv4 != "203.0.113.10" || inst.IPv6 != "2001:db8::5" || inst.Region != "Bucharest" {
		t.Fatalf("unexpected instance %+v", inst)
	}
}

func TestXZoneFailedBuild(t *testing.T) {
	f := &fakeXZone{vpsFound: true, states: []string{"pending"}, failure: true}
	srv := httptest.NewServer(f)
	defer srv.Close()

	_, err := NewXZone(srv.URL, "tok").Get(context.Background(), "v1")
	if err == nil || !strings.Contains(err.Error(), "ip_capacity") || !IsCapacityErr(err) {
		t.Fatalf("want ip_capacity capacity error, got %v", err)
	}
}

func TestXZoneDelete(t *testing.T) {
	f := &fakeXZone{}
	srv := httptest.NewServer(f)
	defer srv.Close()
	x := NewXZone(srv.URL, "tok")

	if err := x.Delete(context.Background(), "v1"); err != nil {
		t.Fatal(err)
	}
	if f.order["immediate"] != true {
		t.Fatalf("delete body %v", f.order)
	}
	if err := x.Delete(context.Background(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestXZoneSoldOutIsCapacity(t *testing.T) {
	err := &APIError{Status: 409, Body: `{"success":false,"code":"LOCATION_SOLD_OUT","error":"Location is sold out"}`}
	if !IsCapacityErr(err) {
		t.Fatal("LOCATION_SOLD_OUT should count as capacity")
	}
}

func TestXZoneValidateScopes(t *testing.T) {
	f := &fakeXZone{scopes: []string{"read", "billing", "destructive"}}
	srv := httptest.NewServer(f)
	defer srv.Close()
	x := NewXZone(srv.URL, "tok")

	if err := x.Validate(context.Background()); err == nil || !strings.Contains(err.Error(), "orders") {
		t.Fatalf("want missing orders scope, got %v", err)
	}
	f.scopes = append(f.scopes, "orders")
	if err := x.Validate(context.Background()); err != nil {
		t.Fatal(err)
	}
}
