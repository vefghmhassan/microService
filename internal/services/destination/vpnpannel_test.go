package destination

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakePanel mimics the vpn-pannel routes the client uses.
func fakePanel(t *testing.T, logins *int, expireFirst bool) *httptest.Server {
	served := 0
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login":
			r.ParseForm()
			if r.Form.Get("email") != "admin@x" || r.Form.Get("password") != "pw" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			*logins++
			http.SetCookie(w, &http.Cookie{Name: "admin_token", Value: "jwt" + strings.Repeat("x", *logins)})
			http.Redirect(w, r, "/admin", http.StatusFound)
		case "/admin/v2ray/new":
			served++
			if expireFirst && served == 1 {
				http.Redirect(w, r, "/login", http.StatusFound)
				return
			}
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer jwt") {
				http.Redirect(w, r, "/login", http.StatusFound)
				return
			}
			r.ParseForm()
			if r.Form.Get("mode") != "link" || len(strings.Split(r.Form.Get("links"), "\n")) != 2 || r.Form.Get("country") != "DE" {
				t.Errorf("form %v", r.Form)
			}
			http.Redirect(w, r, "/admin/v2ray", http.StatusFound)
		case "/admin/v2ray/replace-address":
			var in map[string]any
			json.NewDecoder(r.Body).Decode(&in)
			if in["old_address"] == "0.0.0.0" {
				w.WriteHeader(http.StatusNotFound)
				io.WriteString(w, "no nodes use that address")
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"updated": 3, "skipped": 0, "ids": []int{1, 2, 3}})
		}
	}))
}

func TestUploadAndReplace(t *testing.T) {
	logins := 0
	srv := fakePanel(t, &logins, false)
	defer srv.Close()
	v := NewVPNPanel(srv.URL, "admin@x", "pw")
	ctx := context.Background()
	if err := v.Upload(ctx, UploadRequest{Links: []string{"vless://a", "vless://b"}, Country: "de"}); err != nil {
		t.Fatal(err)
	}
	res, err := v.ReplaceAddress(ctx, "1.1.1.1", "2.2.2.2", "de")
	if err != nil || res.Updated != 3 {
		t.Fatalf("%+v %v", res, err)
	}
	if _, err := v.ReplaceAddress(ctx, "0.0.0.0", "2.2.2.2", ""); !errors.Is(err, ErrNoNodes) {
		t.Fatalf("want ErrNoNodes, got %v", err)
	}
	if logins != 1 {
		t.Fatalf("logged in %d times", logins)
	}
}

func TestReloginWhenSessionExpired(t *testing.T) {
	logins := 0
	srv := fakePanel(t, &logins, true)
	defer srv.Close()
	v := NewVPNPanel(srv.URL, "admin@x", "pw")
	if err := v.Upload(context.Background(), UploadRequest{Links: []string{"vless://a", "vless://b"}, Country: "DE"}); err != nil {
		t.Fatal(err)
	}
	if logins != 2 {
		t.Fatalf("expected a second login, got %d", logins)
	}
}

func TestBadCredentials(t *testing.T) {
	logins := 0
	srv := fakePanel(t, &logins, false)
	defer srv.Close()
	if err := NewVPNPanel(srv.URL, "admin@x", "wrong").Test(context.Background()); !errors.Is(err, ErrAuth) {
		t.Fatalf("want ErrAuth, got %v", err)
	}
}
