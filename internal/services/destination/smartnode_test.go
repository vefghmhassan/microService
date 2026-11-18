package destination

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
)

type fakeNode struct {
	ID      uint
	Name    string
	Raw     string
	Country string
	Ads     bool
	Active  bool
}

// smartPanel mimics vpn-pannel's admin list, new, edit, active and delete
// pages closely enough for the smart node client.
type smartPanel struct {
	mu     sync.Mutex
	nextID uint
	nodes  map[uint]*fakeNode
}

func (p *smartPanel) byName(name string) *fakeNode {
	for _, n := range p.nodes {
		if n.Name == name {
			return n
		}
	}
	return nil
}

func remarks(raw string) string {
	var m struct {
		Remarks string `json:"remarks"`
	}
	json.Unmarshal([]byte(raw), &m)
	return m.Remarks
}

func (p *smartPanel) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		defer p.mu.Unlock()
		if r.URL.Path == "/login" {
			http.SetCookie(w, &http.Cookie{Name: "admin_token", Value: "jwt"})
			http.Redirect(w, r, "/admin", http.StatusFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer jwt" {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		r.ParseForm()
		path := strings.TrimPrefix(r.URL.Path, "/admin/v2ray")
		switch {
		case path == "" && r.Method == http.MethodGet:
			q := strings.ToLower(r.URL.Query().Get("q"))
			var ids []int
			for id, n := range p.nodes {
				if strings.Contains(strings.ToLower(n.Name), q) {
					ids = append(ids, int(id))
				}
			}
			sort.Sort(sort.Reverse(sort.IntSlice(ids)))
			var b strings.Builder
			b.WriteString(`<table class="table"><thead><tr><th>ID</th></tr></thead><tbody>`)
			for _, id := range ids {
				n := p.nodes[uint(id)]
				act := `<span class="badge badge-ghost badge-sm">غیرفعال</span>`
				if n.Active {
					act = `<span class="badge badge-success badge-sm">فعال</span>`
				}
				ads := `<span class="badge badge-ghost badge-sm">-</span>`
				if n.Ads {
					ads = `<span class="badge badge-success badge-sm">Ads</span>`
				}
				fmt.Fprintf(&b, "\n<tr>\n  <td><input type=\"checkbox\" name=\"ids\" value=\"%d\" class=\"checkbox checkbox-sm js-row-select\" /></td>\n  <td>%d</td>\n  <td>%s</td>\n  <td>1.2.3.4</td>\n  <td>%s</td>\n  <td>%s</td>\n</tr>",
					n.ID, n.ID, html.EscapeString(n.Name), ads, act)
			}
			b.WriteString("</tbody></table>")
			w.Write([]byte(b.String()))
		case path == "/new":
			raw := r.Form.Get("links")
			name := remarks(raw)
			if r.Form.Get("base_name") != "" || name == "" || p.byName(name) != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			p.nextID++
			p.nodes[p.nextID] = &fakeNode{ID: p.nextID, Name: name, Raw: raw, Country: r.Form.Get("country"), Ads: r.Form.Get("ads") != "", Active: true}
			http.Redirect(w, r, "/admin/v2ray", http.StatusFound)
		default:
			parts := strings.Split(strings.Trim(path, "/"), "/")
			id, _ := strconv.Atoi(parts[0])
			n := p.nodes[uint(id)]
			if n == nil || len(parts) != 2 {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			switch parts[1] {
			case "edit":
				raw := r.Form.Get("link")
				if o := p.byName(remarks(raw)); o != nil && o.ID != n.ID {
					w.WriteHeader(http.StatusBadRequest)
					w.Write([]byte("duplicate name"))
					return
				}
				n.Name, n.Raw, n.Country, n.Ads = remarks(raw), raw, r.Form.Get("country"), r.Form.Get("ads") != ""
			case "active":
				n.Active = r.Form.Get("active") == "true"
			case "delete":
				delete(p.nodes, n.ID)
			}
			http.Redirect(w, r, "/admin/v2ray", http.StatusFound)
		}
	})
}

func cfg(name, v string) string { return `{"remarks":"` + name + `","v":"` + v + `"}` }

func TestSmartNodeUpsert(t *testing.T) {
	p := &smartPanel{nodes: map[uint]*fakeNode{}}
	srv := httptest.NewServer(p.handler(t))
	defer srv.Close()
	v := NewVPNPanel(srv.URL, "a", "b")
	ctx := context.Background()

	// An unrelated node whose name contains ours must not be picked.
	p.nextID = 1
	p.nodes[1] = &fakeNode{ID: 1, Name: "de-1-smart-ads", Active: true}

	id, err := v.UpsertJSON(ctx, SmartNode{Name: "de-1-smart", JSON: cfg("de-1-smart", "1"), Country: "de"})
	if err != nil || id != 2 {
		t.Fatalf("create: id=%d err=%v", id, err)
	}
	if n := p.nodes[2]; n.Country != "DE" || n.Ads || n.Raw != cfg("de-1-smart", "1") {
		t.Fatalf("created %+v", n)
	}
	id, err = v.UpsertJSON(ctx, SmartNode{Name: "de-1-smart", JSON: cfg("de-1-smart", "2"), Country: "DE", Ads: true})
	if err != nil || id != 2 || len(p.nodes) != 2 {
		t.Fatalf("edit: id=%d err=%v nodes=%d", id, err, len(p.nodes))
	}
	if n := p.nodes[2]; n.Raw != cfg("de-1-smart", "2") || !n.Ads {
		t.Fatalf("edited %+v", n)
	}

	if err := v.SetNodeActive(ctx, 2, false); err != nil {
		t.Fatal(err)
	}
	n, err := v.FindByName(ctx, "de-1-smart")
	if err != nil || n.ID != 2 || n.Active {
		t.Fatalf("find: %+v %v", n, err)
	}
	if n, _ := v.FindByName(ctx, "de-1-smart-ads"); n.ID != 1 || !n.Active {
		t.Fatalf("find other: %+v", n)
	}

	// Deleted on the panel: created again.
	if err := v.DeleteNode(ctx, 2); err != nil {
		t.Fatal(err)
	}
	if err := v.DeleteNode(ctx, 2); err != nil {
		t.Fatalf("second delete: %v", err)
	}
	id, err = v.UpsertJSON(ctx, SmartNode{Name: "de-1-smart", JSON: cfg("de-1-smart", "3")})
	if err != nil || id != 3 {
		t.Fatalf("recreate: id=%d err=%v", id, err)
	}
}

func TestParseNodeRowsEscaped(t *testing.T) {
	page := "<tr>\n<td><input type=\"checkbox\" name=\"ids\" value=\"7\" class=\"x\" /></td>\n<td>7</td>\n<td>a &amp; b</td><td><span class=\"badge badge-success badge-sm\">فعال</span></td></tr>"
	got := parseNodeRows(page)["a & b"]
	if got.ID != 7 || !got.Active {
		t.Fatalf("%+v", got)
	}
}
