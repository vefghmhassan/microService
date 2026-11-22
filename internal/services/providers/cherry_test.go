package providers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeCherry answers like the Cherry Servers API for one team and project.
type fakeCherry struct {
	mu       sync.Mutex
	created  map[string]any
	keyAdded bool
	statuses []string
	deleted  []string
}

func (f *fakeCherry) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer tok" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	body, _ := io.ReadAll(r.Body)
	write := func(v any) { json.NewEncoder(w).Encode(v) }
	server := func(id int, status string) map[string]any {
		return map[string]any{
			"id": id, "hostname": "srv", "status": status, "state": "active",
			"region":     map[string]any{"slug": "LT-Siauliai", "region_iso_2": "LT"},
			"pricing":    map[string]any{"price": 0.0052, "unit": "Hourly", "currency": "EUR"},
			"created_at": time.Now().Add(-10 * time.Hour).UTC().Format(time.RFC3339),
			"ip_addresses": []any{
				map[string]any{"address": "10.168.0.5", "address_family": 4, "type": "private-ip"},
				map[string]any{"address": "2a0c:9a40::5", "address_family": 6, "type": "primary-ip"},
				map[string]any{"address": "5.199.160.9", "address_family": 4, "type": "primary-ip"},
			},
		}
	}
	switch r.Method + " " + r.URL.Path {
	case "GET /teams":
		write([]any{
			map[string]any{"id": 1, "projects": []any{map[string]any{"id": 99}}},
			map[string]any{"id": 7, "projects": []any{map[string]any{"id": 42}},
				"credit": map[string]any{
					"account": map[string]any{"remaining": 12.5, "currency": "EUR"},
					"promo":   map[string]any{"remaining": 2.5, "currency": "EUR"},
				}},
		})
	case "GET /regions":
		write([]any{map[string]any{"slug": "LT-Siauliai", "name": "EU-Nord-1", "region_iso_2": "LT", "location": "Lithuania, Siauliai"}})
	case "GET /teams/7/plans":
		region := func(slug string, qty int) map[string]any {
			return map[string]any{"slug": slug, "name": slug, "stock_qty": qty}
		}
		write([]any{
			map[string]any{"slug": "B2-1-1gb-20s-shared", "name": "Cloud VPS 1 (Gen 2)", "type": "vps", "category": "Shared resources",
				"specs": map[string]any{"cpus": map[string]any{"cores": 1}, "memory": map[string]any{"total": 1, "unit": "GB"},
					"storage": []any{map[string]any{"size": 20}}, "bandwidth": map[string]any{"name": "1TB"}},
				"pricing":           []any{map[string]any{"price": 0.0052, "unit": "Hourly"}, map[string]any{"price": 3.76, "unit": "Monthly"}},
				"available_regions": []any{region("LT-Siauliai", 24), region("NL-Amsterdam", 0)}},
			map[string]any{"slug": "e5_1620v4", "name": "E5-1620v4", "type": "baremetal",
				"available_regions": []any{region("LT-Siauliai", 5)}},
		})
	case "GET /ssh-keys":
		write([]any{map[string]any{"id": 3, "key": "ssh-ed25519 OTHER x"}})
	case "POST /ssh-keys":
		f.keyAdded = true
		w.WriteHeader(http.StatusCreated)
		write(map[string]any{"id": 11})
	case "POST /projects/42/servers":
		json.Unmarshal(body, &f.created)
		w.WriteHeader(http.StatusCreated)
		write(server(555, "deploying"))
	case "GET /servers/555":
		st := f.statuses[0]
		if len(f.statuses) > 1 {
			f.statuses = f.statuses[1:]
		}
		write(server(555, st))
	case "GET /projects/42/servers":
		write([]any{server(555, "deployed")})
	case "DELETE /servers/555":
		f.deleted = append(f.deleted, "555")
		w.WriteHeader(http.StatusNoContent)
	case "DELETE /servers/404":
		w.WriteHeader(http.StatusNotFound)
	default:
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"code":404,"message":"no route `+r.URL.Path+`"}`)
	}
}

func TestCherryFlow(t *testing.T) {
	pollInterval = time.Millisecond
	fake := &fakeCherry{statuses: []string{"deploying", "deployed"}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	ctx := context.Background()

	p, err := New("cherry", json.RawMessage(`{"token":"tok","project_id":"42"}`))
	if err != nil {
		t.Fatal(err)
	}
	ch := p.(*Cherry)
	ch.c.BaseURL = srv.URL

	if err := ch.Validate(ctx); err != nil || ch.team != 7 {
		t.Fatalf("validate: team %d, %v", ch.team, err)
	}
	regs, err := ch.Regions(ctx)
	if err != nil || len(regs) != 1 || regs[0].ID != "LT-Siauliai" || regs[0].Country != "LT" {
		t.Fatalf("regions: %+v %v", regs, err)
	}
	plans, err := ch.Plans(ctx, "LT-Siauliai")
	if err != nil || len(plans) != 1 || plans[0].ID != "B2-1-1gb-20s-shared" || plans[0].PriceMonth != 3.76 || plans[0].PriceHourly != 0.0052 {
		t.Fatalf("plans: %+v %v", plans, err)
	}
	if plans, _ := ch.Plans(ctx, "NL-Amsterdam"); len(plans) != 0 {
		t.Fatalf("out-of-stock region should list nothing: %+v", plans)
	}

	inst, err := ch.Create(ctx, CreateRequest{Name: "srv", Region: "LT-Siauliai", Plan: "B2-1-1gb-20s-shared",
		SSHPublicKey: "ssh-ed25519 AAAAmine me@host", UserData: "#cloud-config\n"})
	if err != nil {
		t.Fatal(err)
	}
	if inst.Ref != "555" || inst.IPv4 != "5.199.160.9" || inst.IPv6 != "2a0c:9a40::5" || inst.Running() {
		t.Fatalf("created: %+v", inst)
	}
	if !fake.keyAdded {
		t.Fatal("ssh key was not added")
	}
	c := fake.created
	ud, _ := base64.StdEncoding.DecodeString(c["user_data"].(string))
	if c["plan"] != "B2-1-1gb-20s-shared" || c["region"] != "LT-Siauliai" || c["image"] != "ubuntu_24_04_64bit" ||
		c["cycle"] != "hourly" || string(ud) != "#cloud-config\n" || c["ssh_keys"].([]any)[0] != "11" {
		t.Fatalf("create body: %+v", c)
	}

	ready, err := WaitRunning(ctx, ch, inst.Ref, time.Second)
	if err != nil || !ready.Running() {
		t.Fatalf("wait: %+v %v", ready, err)
	}

	b, err := ch.Billing(ctx)
	if err != nil || b.Credit == nil || *b.Credit != 15 || b.Currency != "EUR" || b.Spent <= 0 || !b.Estimated {
		t.Fatalf("billing: %+v %v", b, err)
	}

	if err := ch.Delete(ctx, "555"); err != nil {
		t.Fatal(err)
	}
	if err := ch.Delete(ctx, "404"); !isNotFound(err) {
		t.Fatalf("delete missing: %v", err)
	}
}

func TestCherryFailedDeployment(t *testing.T) {
	srv := httptest.NewServer(&fakeCherry{statuses: []string{"failed deployment"}})
	defer srv.Close()
	ch := NewCherry(srv.URL, "tok", 42)
	if _, err := ch.Get(context.Background(), "555"); err == nil || !strings.Contains(err.Error(), "failed") {
		t.Fatalf("want failed deployment error, got %v", err)
	}
}

func TestCherryCredentials(t *testing.T) {
	for _, raw := range []string{`{"token":"t"}`, `{"token":"t","project_id":"abc"}`, `{"project_id":"1"}`} {
		if _, err := New("cherry", json.RawMessage(raw)); err == nil {
			t.Fatalf("%s: want error", raw)
		}
	}
	if _, err := New("cherry", json.RawMessage(`{"token":"t","project_id":42}`)); err != nil {
		t.Fatalf("numeric project id: %v", err)
	}
	if !IsCapacityErr(&APIError{Status: 400, Body: `{"message":"Plan is out of stock in this region"}`}) {
		t.Fatal("out of stock should count as capacity")
	}
}
