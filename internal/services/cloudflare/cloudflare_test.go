package cloudflare

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUpsertAUpdatesFirstAndRemovesOthers(t *testing.T) {
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(403)
			w.Write([]byte(`{"success":false,"errors":[{"code":9109,"message":"Invalid access token"}]}`))
			return
		}
		calls = append(calls, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == "GET" && r.URL.Query().Get("type") == "AAAA":
			w.Write([]byte(`{"success":true,"result":[]}`))
		case r.Method == "GET":
			if r.URL.Query().Get("name") != "fr1.example.com" || r.URL.Query().Get("type") != "A" {
				t.Errorf("query %s", r.URL.RawQuery)
			}
			w.Write([]byte(`{"success":true,"result":[
				{"id":"a","type":"A","name":"fr1.example.com","content":"1.1.1.1"},
				{"id":"b","type":"A","name":"fr1.example.com","content":"2.2.2.2"}]}`))
		case r.Method == "PATCH":
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if body["content"] != "9.9.9.9" || body["ttl"] != float64(60) {
				t.Errorf("patch body %v", body)
			}
			w.Write([]byte(`{"success":true,"result":{"id":"a"}}`))
		default:
			w.Write([]byte(`{"success":true,"result":{}}`))
		}
	}))
	defer srv.Close()

	c := New("tok")
	c.BaseURL = srv.URL
	id, err := c.UpsertA(context.Background(), "z", "fr1.example.com", "9.9.9.9", false)
	if err != nil || id != "a" {
		t.Fatalf("id=%s err=%v", id, err)
	}
	want := "GET /zones/z/dns_records,GET /zones/z/dns_records,PATCH /zones/z/dns_records/a,DELETE /zones/z/dns_records/b"
	if got := strings.Join(calls, ","); got != want {
		t.Fatalf("calls %s", got)
	}

	c.Token = "bad"
	_, err = c.Zones(context.Background())
	if err == nil || !strings.Contains(err.Error(), "Invalid access token") {
		t.Fatalf("err %v", err)
	}
}

func TestUpsertAddrSwitchesAToAAAA(t *testing.T) {
	var calls []string
	var created map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Query().Get("type")+r.URL.Path)
		switch {
		case r.Method == "GET" && r.URL.Query().Get("type") == "A":
			w.Write([]byte(`{"success":true,"result":[{"id":"old","type":"A","name":"fr1.example.com","content":"1.1.1.1"}]}`))
		case r.Method == "GET":
			w.Write([]byte(`{"success":true,"result":[]}`))
		case r.Method == "POST":
			json.NewDecoder(r.Body).Decode(&created)
			w.Write([]byte(`{"success":true,"result":{"id":"new"}}`))
		default:
			w.Write([]byte(`{"success":true,"result":{}}`))
		}
	}))
	defer srv.Close()

	c := New("tok")
	c.BaseURL = srv.URL
	id, err := c.UpsertAddr(context.Background(), "z", "fr1.example.com", "2a01:4f8::1", true)
	if err != nil || id != "new" {
		t.Fatalf("id=%s err=%v", id, err)
	}
	if created["type"] != "AAAA" || created["content"] != "2a01:4f8::1" {
		t.Fatalf("created %v", created)
	}
	want := "GET A/zones/z/dns_records,GET AAAA/zones/z/dns_records,DELETE /zones/z/dns_records/old,POST /zones/z/dns_records"
	if got := strings.Join(calls, ","); got != want {
		t.Fatalf("calls %s", got)
	}
}

func TestCreateTokenWithGlobalKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Auth-Email") != "a@b.c" || r.Header.Get("X-Auth-Key") != "gk" || r.Header.Get("Authorization") != "" {
			w.WriteHeader(403)
			w.Write([]byte(`{"success":false,"errors":[{"code":9103,"message":"Unknown X-Auth-Key or X-Auth-Email"}]}`))
			return
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /user/tokens/permission_groups":
			w.Write([]byte(`{"success":true,"result":[
				{"id":"dns-acc","name":"DNS Write","scopes":["com.cloudflare.api.account"]},
				{"id":"dns","name":"DNS Write","scopes":["com.cloudflare.api.account.zone"]},
				{"id":"zr","name":"Zone Read","scopes":["com.cloudflare.api.account.zone"]},
				{"id":"zs","name":"Zone Settings Write","scopes":["com.cloudflare.api.account.zone"]}]}`))
		case "POST /user/tokens":
			var body struct {
				Name     string
				Policies []struct {
					Resources        map[string]string
					PermissionGroups []struct{ ID string } `json:"permission_groups"`
				}
			}
			json.NewDecoder(r.Body).Decode(&body)
			p := body.Policies[0]
			if body.Name != "panel" || p.Resources["com.cloudflare.api.account.zone.*"] != "*" ||
				len(p.PermissionGroups) != 3 || p.PermissionGroups[0].ID != "zr" || p.PermissionGroups[1].ID != "dns" || p.PermissionGroups[2].ID != "zs" {
				t.Errorf("token body %+v", body)
			}
			w.Write([]byte(`{"success":true,"result":{"id":"t1","value":"secret"}}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, AuthEmail: "a@b.c", AuthKey: "gk", HTTP: srv.Client()}
	tok, err := c.CreateToken(context.Background(), "panel")
	if err != nil || tok != "secret" {
		t.Fatalf("got %q, %v", tok, err)
	}
}

func TestErrorChainAndKeyShape(t *testing.T) {
	var e APIError
	json.Unmarshal([]byte(`{"errors":[{"code":6003,"message":"Invalid request headers","error_chain":[{"code":6103,"message":"Invalid format for X-Auth-Key header"}]}]}`), &e)
	if got := e.Error(); got != "cloudflare: 6003 Invalid request headers; 6103 Invalid format for X-Auth-Key header" {
		t.Errorf("error %q", got)
	}
	if !LooksLikeGlobalKey(strings.Repeat("a1", 18)+"f") || LooksLikeGlobalKey(strings.Repeat("A", 40)) {
		t.Error("key shape")
	}
}

func TestEnsureCDNSettings(t *testing.T) {
	got := map[string]any{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || !strings.HasPrefix(r.URL.Path, "/zones/z1/settings/") {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		var body struct{ Value any }
		json.NewDecoder(r.Body).Decode(&body)
		key := strings.TrimPrefix(r.URL.Path, "/zones/z1/settings/")
		got[key] = body.Value
		if key == "websockets" {
			w.WriteHeader(403)
			w.Write([]byte(`{"success":false,"errors":[{"code":10000,"message":"Authentication error"}]}`))
			return
		}
		w.Write([]byte(`{"success":true,"result":{}}`))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, Token: "tok", HTTP: srv.Client()}
	err := c.EnsureCDNSettings(context.Background(), "z1", true)
	if err == nil || !strings.Contains(err.Error(), "websockets: cloudflare: 10000") {
		t.Errorf("error %v", err)
	}
	if _, ok := got["grpc"]; ok || got["websockets"] != "on" || got["ssl"] != "strict" || got["min_tls_version"] != "1.2" {
		t.Errorf("settings %v", got)
	}
}
