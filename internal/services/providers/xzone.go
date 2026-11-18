package providers

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// X-ZoneServers customer API: https://my.x-zoneservers.com/api/v1/docs
//
// Servers are bought, not just created: an hourly order is placed and, when
// it is not settled from credit on the spot, its invoice or proforma is paid
// from credit. The API has no IP swap, so rotation recreates the VPS.
type XZone struct{ c *restClient }

// xzoneScopes are the key scopes create, poll and delete need.
var xzoneScopes = []string{"read", "orders", "billing", "destructive"}

func init() {
	Register(Descriptor{
		Type: "xzone", Name: "X-ZoneServers", Fields: tokenField,
		Capabilities: Capabilities{RotateMethod: RotateRecreate},
		Docs:         "https://my.x-zoneservers.com/api/v1/docs",
	}, func(raw json.RawMessage) (Provider, error) {
		tok, err := parseToken(raw)
		if err != nil {
			return nil, err
		}
		return NewXZone("https://my.x-zoneservers.com/api/v1", tok), nil
	})
}

func NewXZone(base, token string) *XZone { return &XZone{c: newREST(base, token)} }

func (x *XZone) Type() string               { return "xzone" }
func (x *XZone) Capabilities() Capabilities { return Capabilities{RotateMethod: RotateRecreate} }

// call unwraps the {success, data} envelope into out.
func (x *XZone) call(ctx context.Context, method, path string, hdr map[string]string, in, out any) error {
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := x.c.doH(ctx, method, path, hdr, in, &env); err != nil {
		return err
	}
	if out == nil || len(env.Data) == 0 || string(env.Data) == "null" {
		return nil
	}
	return json.Unmarshal(env.Data, out)
}

// unwrap returns data[key] when the payload nests the object under key,
// else the payload itself.
func unwrap(raw json.RawMessage, key string) json.RawMessage {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) == nil {
		if v, ok := m[key]; ok && len(v) > 0 && v[0] == '{' {
			return v
		}
	}
	return raw
}

// xzNum accepts prices sent as numbers, decimal strings or null.
type xzNum float64

func (n *xzNum) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(bytes.TrimSpace(b)), `"`)
	if s == "" || s == "null" {
		*n = 0
		return nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil
	}
	*n = xzNum(f)
	return nil
}

type xzVps struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Hostname    string `json:"hostname"`
	Status      string `json:"status"`
	Location    string `json:"location"`
	PrimaryIP   string `json:"primary_ip"`
	IPAddresses []struct {
		Address string `json:"address"`
		Version int    `json:"version"`
	} `json:"ip_addresses"`
	Instance *struct {
		State string `json:"state"`
	} `json:"instance"`
	Failure *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"failure"`
}

func (v *xzVps) instance() *Instance {
	inst := &Instance{Ref: v.ID, Name: firstNonEmpty(v.Hostname, v.Name), Region: v.Location, IPv4: v.PrimaryIP}
	for _, a := range v.IPAddresses {
		switch {
		case a.Version == 4 && inst.IPv4 == "":
			inst.IPv4 = a.Address
		case a.Version == 6 && inst.IPv6 == "":
			inst.IPv6 = hostIPv6(a.Address)
		}
	}
	// status is billing state, instance.state the machine's.
	switch {
	case v.Instance == nil:
		inst.Status = v.Status
	case v.Status == "active" && v.Instance.State == "running":
		inst.Status = "running"
	default:
		inst.Status = v.Instance.State
	}
	return inst
}

func (x *XZone) Validate(ctx context.Context) error {
	var out struct {
		Key struct {
			Scopes []string `json:"scopes"`
		} `json:"key"`
	}
	if err := x.call(ctx, http.MethodGet, "/", nil, nil, &out); err != nil {
		return err
	}
	var missing []string
	for _, s := range xzoneScopes {
		if !contains(out.Key.Scopes, s) {
			missing = append(missing, s)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("API key is missing scopes: %s (needs %s)", strings.Join(missing, ", "), strings.Join(xzoneScopes, ", "))
	}
	return nil
}

type xzLocation struct {
	Value       string   `json:"value"`
	Name        string   `json:"name"`
	CountryCode string   `json:"country_code"`
	SoldOut     bool     `json:"sold_out"`
	ProductIDs  []string `json:"product_ids"`
}

func (x *XZone) locations(ctx context.Context) ([]xzLocation, error) {
	var out struct {
		Locations []xzLocation `json:"locations"`
	}
	err := x.call(ctx, http.MethodGet, "/locations", nil, nil, &out)
	return out.Locations, err
}

func (x *XZone) Regions(ctx context.Context) ([]Option, error) {
	locs, err := x.locations(ctx)
	if err != nil {
		return nil, err
	}
	opts := make([]Option, 0, len(locs))
	for _, l := range locs {
		if l.SoldOut {
			continue
		}
		opts = append(opts, Option{ID: l.Value, Name: l.Name, Country: strings.ToUpper(l.CountryCode)})
	}
	return opts, nil
}

func (x *XZone) Plans(ctx context.Context, region string) ([]Option, error) {
	var out struct {
		Products []struct {
			ID           string `json:"id"`
			Name         string `json:"name"`
			HourlyPrice  xzNum  `json:"hourly_price"`
			MonthlyPrice xzNum  `json:"monthly_price"`
			Specs        struct {
				VPS struct {
					VCPUs    int `json:"vcpus"`
					MemoryMB int `json:"memory_mb"`
					DiskGB   int `json:"disk_gb"`
				} `json:"vps_config"`
			} `json:"specs"`
		} `json:"products"`
	}
	if err := x.call(ctx, http.MethodGet, "/products", nil, nil, &out); err != nil {
		return nil, err
	}
	var inRegion map[string]bool
	if region != "" {
		locs, err := x.locations(ctx)
		if err != nil {
			return nil, err
		}
		inRegion = map[string]bool{}
		for _, l := range locs {
			if l.Value == region {
				for _, id := range l.ProductIDs {
					inRegion[id] = true
				}
			}
		}
	}
	opts := make([]Option, 0, len(out.Products))
	for _, p := range out.Products {
		// Only hourly products can be created and destroyed on demand.
		if p.HourlyPrice <= 0 || (inRegion != nil && !inRegion[p.ID]) {
			continue
		}
		name := p.Name
		if c := p.Specs.VPS; c.VCPUs > 0 {
			name = fmt.Sprintf("%s — %d vCPU / %dMB / %dGB", p.Name, c.VCPUs, c.MemoryMB, c.DiskGB)
		}
		opts = append(opts, Option{ID: p.ID, Name: name, PriceHourly: float64(p.HourlyPrice), PriceMonth: float64(p.MonthlyPrice)})
	}
	sort.Slice(opts, func(i, j int) bool { return opts[i].PriceHourly < opts[j].PriceHourly })
	return opts, nil
}

// image resolves req.Image (an id or a name) against the product's orderable
// images, falling back to the product default.
func (x *XZone) image(ctx context.Context, productID, want string) (string, error) {
	// The images sit beside data.product, not inside it.
	var p struct {
		DefaultOSImageID string `json:"default_os_image_id"`
		OSImages         []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"os_images"`
	}
	if err := x.call(ctx, http.MethodGet, "/products/"+url.PathEscape(productID), nil, nil, &p); err != nil {
		return "", err
	}
	if want == "" {
		if p.DefaultOSImageID != "" {
			return p.DefaultOSImageID, nil
		}
		if len(p.OSImages) > 0 {
			return p.OSImages[0].ID, nil
		}
		return "", fmt.Errorf("product %s offers no orderable OS image", productID)
	}
	names := make([]string, 0, len(p.OSImages))
	for _, im := range p.OSImages {
		if im.ID == want || imageKey(im.Name) == imageKey(want) {
			return im.ID, nil
		}
		names = append(names, im.Name)
	}
	return "", fmt.Errorf("image %q is not offered by product %s (available: %s)", want, productID, strings.Join(names, ", "))
}

// imageKey lets "ubuntu-24.04" match "Ubuntu 24.04 LTS".
func imageKey(s string) string {
	s = strings.NewReplacer("-", " ", "_", " ").Replace(strings.ToLower(strings.TrimSpace(s)))
	return strings.TrimSuffix(strings.Join(strings.Fields(s), " "), " lts")
}

func (x *XZone) Create(ctx context.Context, req CreateRequest) (*Instance, error) {
	img, err := x.image(ctx, req.Plan, req.Image)
	if err != nil {
		return nil, err
	}
	cfg := map[string]any{"os_image_id": img}
	if k := strings.TrimSpace(req.SSHPublicKey); k != "" {
		cfg["ssh_keys"] = []string{k}
	}
	if req.UserData != "" {
		cfg["user_data"] = req.UserData
	}
	body := map[string]any{
		"productId":     req.Plan,
		"billingCycle":  "hourly",
		"hostname":      sanitizeLabel(req.Name),
		"configOptions": cfg,
		// The operator refuses orders (400 WITHDRAWAL_WAIVER_REQUIRED) unless
		// the buyer accepts that an hourly server starts at once, waiving the
		// right of withdrawal; the panel owner agrees by choosing this provider.
		"acceptTerms":              true,
		"withdrawalWaiverAccepted": true,
	}
	if req.Region != "" {
		body["location"] = req.Region
	}
	// One key per Create: a retried POST returns the same order instead of
	// buying a second server.
	key := make([]byte, 16)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	var out struct {
		Services []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"services"`
		Document *struct {
			Type string `json:"type"`
			ID   string `json:"id"`
		} `json:"document"`
		Settled bool `json:"settled"`
	}
	hdr := map[string]string{"Idempotency-Key": hex.EncodeToString(key)}
	if err := x.call(ctx, http.MethodPost, "/orders", hdr, body, &out); err != nil {
		return nil, err
	}
	if len(out.Services) == 0 {
		return nil, errors.New("order created no service")
	}
	svc := out.Services[0]
	if !out.Settled && out.Document != nil && out.Document.ID != "" {
		path := "/invoices/"
		if out.Document.Type == "proforma" {
			path = "/proformas/"
		}
		if err := x.call(ctx, http.MethodPost, path+url.PathEscape(out.Document.ID)+"/pay", nil, map[string]any{}, nil); err != nil {
			return nil, fmt.Errorf("pay %s %s for order of service %s: %w", out.Document.Type, out.Document.ID, svc.ID, err)
		}
	}
	return &Instance{Ref: svc.ID, Name: req.Name, Status: svc.Status, Region: req.Region}, nil
}

func (x *XZone) Get(ctx context.Context, ref string) (*Instance, error) {
	var raw json.RawMessage
	if err := x.call(ctx, http.MethodGet, "/vps/"+url.PathEscape(ref), nil, nil, &raw); err != nil {
		return nil, err
	}
	var v xzVps
	if err := json.Unmarshal(unwrap(raw, "vps"), &v); err != nil {
		return nil, fmt.Errorf("decode vps: %w", err)
	}
	if v.Status == "provisioning_failed" {
		code, msg := "unknown", ""
		if v.Failure != nil {
			code, msg = firstNonEmpty(v.Failure.Code, code), v.Failure.Message
		}
		return v.instance(), fmt.Errorf("x-zoneservers build failed (%s): %s", code, msg)
	}
	return v.instance(), nil
}

func (x *XZone) Delete(ctx context.Context, ref string) error {
	return x.call(ctx, http.MethodDelete, "/vps/"+url.PathEscape(ref), nil,
		map[string]any{"immediate": true, "reason": "Deleted by panel"}, nil)
}

func (x *XZone) RotateIP(ctx context.Context, inst *Instance, opts RotateOptions) (*RotateResult, error) {
	opts.KeepOld, opts.ReuseIPRef = false, ""
	return recreate(ctx, x, inst, opts, nil)
}

func (x *XZone) ReleaseIP(context.Context, string) error { return nil }
