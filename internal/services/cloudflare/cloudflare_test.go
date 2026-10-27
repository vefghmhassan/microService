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
	want := "GET /zones/z/dns_records,PATCH /zones/z/dns_records/a,DELETE /zones/z/dns_records/b"
	if got := strings.Join(calls, ","); got != want {
		t.Fatalf("calls %s", got)
	}

	c.Token = "bad"
	_, err = c.Zones(context.Background())
	if err == nil || !strings.Contains(err.Error(), "Invalid access token") {
		t.Fatalf("err %v", err)
	}
}
