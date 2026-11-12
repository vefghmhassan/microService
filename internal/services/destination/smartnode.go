package destination

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// Smart configs are replaced in place through vpn-pannel's admin pages:
// vpn-pannel has no upsert API, so a node is found by its (unique) name on
// the HTML list and changed with the same edit form the admin uses.
//
//	GET  /admin/v2ray?q=&page_size=   HTML table: <td>ID</td><td>Name</td>…
//	POST /admin/v2ray/:id/edit        form mode=link, link, country, ads
//	POST /admin/v2ray/:id/active      form active=true|false
//	POST /admin/v2ray/:id/delete

// rowRe matches the checkbox, ID and name cells of one row of the list.
var rowRe = regexp.MustCompile(`(?s)name="ids"\s+value="(\d+)"[^>]*/?>\s*</td>\s*<td>\s*(\d+)\s*</td>\s*<td>(.*?)</td>`)

// PanelNode is one row of the admin list.
type PanelNode struct {
	ID     uint
	Name   string
	Active bool
}

// parseNodeRows returns the nodes of an admin list page by name.
func parseNodeRows(page string) map[string]PanelNode {
	out := map[string]PanelNode{}
	for _, row := range strings.Split(page, "<tr")[1:] {
		m := rowRe.FindStringSubmatch(row)
		if m == nil || m[1] != m[2] {
			continue
		}
		id, err := strconv.ParseUint(m[2], 10, 64)
		if err != nil {
			continue
		}
		name := strings.TrimSpace(html.UnescapeString(m[3]))
		// The active cell is the one badge reading "فعال" (and not "غیرفعال").
		active := strings.Contains(row, `badge-success badge-sm">فعال<`)
		out[name] = PanelNode{ID: uint(id), Name: name, Active: active}
	}
	return out
}

func (v *VPNPanel) form(ctx context.Context, path string, form url.Values) (*http.Response, []byte, error) {
	return v.do(ctx, func(tok string) (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, v.BaseURL+path, strings.NewReader(form.Encode()))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Authorization", "Bearer "+tok)
		return req, nil
	})
}

// FindByName returns the node called name (ID 0 when there is none).
func (v *VPNPanel) FindByName(ctx context.Context, name string) (PanelNode, error) {
	nodes, err := v.listNodes(ctx, name)
	if err != nil {
		return PanelNode{}, err
	}
	return nodes[name], nil
}

func (v *VPNPanel) listNodes(ctx context.Context, search string) (map[string]PanelNode, error) {
	q := url.Values{"q": {search}, "page_size": {"2000"}, "sort": {"newest"}}
	resp, body, err := v.do(ctx, func(tok string) (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.BaseURL+"/admin/v2ray?"+q.Encode(), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		return req, nil
	})
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("vpn-pannel list: HTTP %d", resp.StatusCode)
	}
	return parseNodeRows(string(body)), nil
}

// SmartNode is a full xray JSON config kept under one name.
type SmartNode struct {
	Name    string
	JSON    string // its "remarks" must equal Name: vpn-pannel names the node from it
	Country string
	Ads     bool
}

// ErrNameTaken means a node with that name exists but could not be updated.
var ErrNameTaken = errors.New("vpn-pannel: name is taken")

// UpsertJSON creates the node or replaces its config in place, and returns
// its ID. A node that was deleted on the panel is created again.
func (v *VPNPanel) UpsertJSON(ctx context.Context, n SmartNode) (uint, error) {
	cur, err := v.FindByName(ctx, n.Name)
	if err != nil {
		return 0, err
	}
	id := cur.ID
	country := strings.ToUpper(strings.TrimSpace(n.Country))
	if id != 0 {
		form := url.Values{"mode": {"link"}, "link": {n.JSON}, "country": {country}}
		if n.Ads {
			form.Set("ads", "on")
		}
		resp, body, err := v.form(ctx, fmt.Sprintf("/admin/v2ray/%d/edit", id), form)
		if err != nil {
			return 0, err
		}
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			return id, nil
		}
		if strings.Contains(string(body), "duplicate name") {
			return 0, ErrNameTaken
		}
		return 0, fmt.Errorf("vpn-pannel edit: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	form := url.Values{"mode": {"link"}, "links": {n.JSON}, "country": {country}}
	if n.Ads {
		form.Set("ads", "on")
	}
	resp, body, err := v.form(ctx, "/admin/v2ray/new", form)
	if err != nil {
		return 0, err
	}
	if resp.StatusCode < 300 || resp.StatusCode >= 400 {
		return 0, fmt.Errorf("vpn-pannel create: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	cur, err = v.FindByName(ctx, n.Name)
	if err != nil {
		return 0, err
	}
	if id = cur.ID; id == 0 {
		return 0, errors.New("vpn-pannel did not keep the config (name taken or rejected)")
	}
	return id, nil
}

// SetNodeActive enables or disables one node.
func (v *VPNPanel) SetNodeActive(ctx context.Context, id uint, active bool) error {
	resp, body, err := v.form(ctx, fmt.Sprintf("/admin/v2ray/%d/active", id), url.Values{"active": {strconv.FormatBool(active)}})
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("vpn-pannel active: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

// DeleteNode removes one node; a node that is already gone is not an error.
func (v *VPNPanel) DeleteNode(ctx context.Context, id uint) error {
	resp, body, err := v.form(ctx, fmt.Sprintf("/admin/v2ray/%d/delete", id), url.Values{})
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("vpn-pannel delete: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}
