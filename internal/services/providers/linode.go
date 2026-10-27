package providers

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// Linode / Akamai: https://techdocs.akamai.com/linode-api/reference/api
//
// Rotation recreates the Linode. The old IP cannot be kept: Linode's reserved
// IPs are too new to rely on here.
type Linode struct{ c *restClient }

func init() {
	Register(Descriptor{
		Type: "linode", Name: "Linode (Akamai)", Fields: tokenField,
		Capabilities: Capabilities{RotateMethod: RotateRecreate, DefaultImage: "linode/ubuntu24.04"},
		Docs:         "https://techdocs.akamai.com/linode-api/reference/api",
	}, func(raw json.RawMessage) (Provider, error) {
		tok, err := parseToken(raw)
		if err != nil {
			return nil, err
		}
		return NewLinode("https://api.linode.com/v4", tok), nil
	})
}

func NewLinode(base, token string) *Linode { return &Linode{c: newREST(base, token)} }

func (l *Linode) Type() string { return "linode" }
func (l *Linode) Capabilities() Capabilities {
	return Capabilities{RotateMethod: RotateRecreate, DefaultImage: "linode/ubuntu24.04"}
}

type linodeInstance struct {
	ID     int64    `json:"id"`
	Label  string   `json:"label"`
	Status string   `json:"status"`
	IPv4   []string `json:"ipv4"`
	IPv6   string   `json:"ipv6"` // SLAAC address, "addr/128"
	Region string   `json:"region"`
}

func (s *linodeInstance) instance() *Instance {
	inst := &Instance{Ref: strconv.FormatInt(s.ID, 10), Name: s.Label, Status: s.Status, Region: s.Region}
	for _, ip := range s.IPv4 {
		// The list also holds private 192.168/17 addresses.
		if !strings.HasPrefix(ip, "192.168.") && !strings.HasPrefix(ip, "10.") {
			inst.IPv4 = ip
			break
		}
	}
	inst.IPv6 = hostIPv6(s.IPv6)
	return inst
}

func (l *Linode) Validate(ctx context.Context) error {
	return l.c.do(ctx, http.MethodGet, "/profile", nil, nil)
}

func (l *Linode) Regions(ctx context.Context) ([]Option, error) {
	var out struct {
		Data []struct {
			ID      string `json:"id"`
			Label   string `json:"label"`
			Country string `json:"country"`
		} `json:"data"`
	}
	if err := l.c.do(ctx, http.MethodGet, "/regions?page_size=500", nil, &out); err != nil {
		return nil, err
	}
	opts := make([]Option, 0, len(out.Data))
	for _, r := range out.Data {
		opts = append(opts, Option{ID: r.ID, Name: r.Label + " (" + r.ID + ")", Country: strings.ToUpper(r.Country)})
	}
	return opts, nil
}

func (l *Linode) Plans(ctx context.Context, _ string) ([]Option, error) {
	var out struct {
		Data []struct {
			ID     string `json:"id"`
			Label  string `json:"label"`
			VCPUs  int    `json:"vcpus"`
			Memory int    `json:"memory"`
			Disk   int    `json:"disk"`
			Price  struct {
				Hourly  float64 `json:"hourly"`
				Monthly float64 `json:"monthly"`
			} `json:"price"`
		} `json:"data"`
	}
	if err := l.c.do(ctx, http.MethodGet, "/linode/types?page_size=500", nil, &out); err != nil {
		return nil, err
	}
	opts := make([]Option, 0, len(out.Data))
	for _, t := range out.Data {
		opts = append(opts, Option{
			ID: t.ID, Name: fmt.Sprintf("%s — %d vCPU / %dMB / %dGB", t.Label, t.VCPUs, t.Memory, t.Disk/1024),
			PriceHourly: t.Price.Hourly, PriceMonth: t.Price.Monthly,
		})
	}
	return opts, nil
}

func (l *Linode) Create(ctx context.Context, req CreateRequest) (*Instance, error) {
	// root_pass is mandatory with an image; it is random and never used
	// because login is by key.
	pw := make([]byte, 24)
	if _, err := rand.Read(pw); err != nil {
		return nil, err
	}
	body := map[string]any{
		"label":     sanitizeLabel(req.Name),
		"region":    req.Region,
		"type":      req.Plan,
		"image":     firstNonEmpty(req.Image, "linode/ubuntu24.04"),
		"root_pass": hex.EncodeToString(pw) + "Aa1!",
		"booted":    true,
	}
	if req.SSHPublicKey != "" {
		body["authorized_keys"] = []string{strings.TrimSpace(req.SSHPublicKey)}
	}
	if req.UserData != "" {
		body["metadata"] = map[string]any{"user_data": base64.StdEncoding.EncodeToString([]byte(req.UserData))}
	}
	var out linodeInstance
	if err := l.c.do(ctx, http.MethodPost, "/linode/instances", body, &out); err != nil {
		return nil, err
	}
	return out.instance(), nil
}

func (l *Linode) Get(ctx context.Context, ref string) (*Instance, error) {
	var out linodeInstance
	if err := l.c.do(ctx, http.MethodGet, "/linode/instances/"+ref, nil, &out); err != nil {
		return nil, err
	}
	return out.instance(), nil
}

func (l *Linode) Delete(ctx context.Context, ref string) error {
	return l.c.do(ctx, http.MethodDelete, "/linode/instances/"+ref, nil, nil)
}

func (l *Linode) RotateIP(ctx context.Context, inst *Instance, opts RotateOptions) (*RotateResult, error) {
	opts.KeepOld = false
	opts.ReuseIPRef = ""
	return recreate(ctx, l, inst, opts, nil)
}

func (l *Linode) ReleaseIP(context.Context, string) error { return nil }

// sanitizeLabel fits Linode's label rules: 3-64 chars of [A-Za-z0-9-_.].
func sanitizeLabel(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := b.String()
	if len(out) > 64 {
		out = out[:64]
	}
	for len(out) < 3 {
		out += "0"
	}
	return out
}
