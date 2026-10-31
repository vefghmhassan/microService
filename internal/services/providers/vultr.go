package providers

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Vultr: https://www.vultr.com/api/
//
// Rotation recreates the instance. When the old IP should be kept it is first
// converted to a Reserved IP, which survives the instance being destroyed and
// can be handed to a later instance at create time.
type Vultr struct{ c *restClient }

const vultrUbuntu2404 = "2284"

func init() {
	Register(Descriptor{
		Type: "vultr", Name: "Vultr", Fields: tokenField,
		Capabilities: Capabilities{RotateMethod: RotateRecreate, CanKeepOldIP: true, DefaultImage: vultrUbuntu2404},
		Docs:         "https://www.vultr.com/api/",
	}, func(raw json.RawMessage) (Provider, error) {
		tok, err := parseToken(raw)
		if err != nil {
			return nil, err
		}
		return NewVultr("https://api.vultr.com/v2", tok), nil
	})
}

func NewVultr(base, token string) *Vultr { return &Vultr{c: newREST(base, token)} }

func (v *Vultr) Type() string { return "vultr" }
func (v *Vultr) Capabilities() Capabilities {
	return Capabilities{RotateMethod: RotateRecreate, CanKeepOldIP: true, DefaultImage: vultrUbuntu2404}
}

type vultrInstance struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	MainIP      string `json:"main_ip"`
	V6MainIP    string `json:"v6_main_ip"`
	Status      string `json:"status"`
	PowerStatus string `json:"power_status"`
	Region      string `json:"region"`
}

func (s *vultrInstance) instance() *Instance {
	status := s.Status
	if s.Status == "active" && s.PowerStatus == "running" {
		status = "running"
	}
	ip := s.MainIP
	if ip == "0.0.0.0" {
		ip = ""
	}
	return &Instance{Ref: s.ID, Name: s.Label, Status: status, IPv4: ip, IPv6: hostIPv6(s.V6MainIP), Region: s.Region}
}

func (v *Vultr) Validate(ctx context.Context) error {
	return v.c.do(ctx, http.MethodGet, "/account", nil, nil)
}

func (v *Vultr) Regions(ctx context.Context) ([]Option, error) {
	var out struct {
		Regions []struct {
			ID      string `json:"id"`
			City    string `json:"city"`
			Country string `json:"country"`
		} `json:"regions"`
	}
	if err := v.c.do(ctx, http.MethodGet, "/regions?per_page=500", nil, &out); err != nil {
		return nil, err
	}
	opts := make([]Option, 0, len(out.Regions))
	for _, r := range out.Regions {
		opts = append(opts, Option{ID: r.ID, Name: r.City + " (" + r.ID + ")", Country: r.Country})
	}
	return opts, nil
}

func (v *Vultr) Plans(ctx context.Context, region string) ([]Option, error) {
	var out struct {
		Plans []struct {
			ID          string   `json:"id"`
			VCPU        int      `json:"vcpu_count"`
			RAM         int      `json:"ram"`
			Disk        int      `json:"disk"`
			MonthlyCost float64  `json:"monthly_cost"`
			Locations   []string `json:"locations"`
		} `json:"plans"`
	}
	if err := v.c.do(ctx, http.MethodGet, "/plans?type=vc2&per_page=500", nil, &out); err != nil {
		return nil, err
	}
	var opts []Option
	for _, p := range out.Plans {
		if region != "" && !contains(p.Locations, region) {
			continue
		}
		opts = append(opts, Option{
			ID:          p.ID,
			Name:        fmt.Sprintf("%s — %d vCPU / %dMB / %dGB", p.ID, p.VCPU, p.RAM, p.Disk),
			PriceMonth:  p.MonthlyCost,
			PriceHourly: p.MonthlyCost / 730,
		})
	}
	return opts, nil
}

func (v *Vultr) ensureSSHKey(ctx context.Context, pub string) (string, error) {
	pub = strings.TrimSpace(pub)
	var list struct {
		SSHKeys []struct {
			ID     string `json:"id"`
			SSHKey string `json:"ssh_key"`
		} `json:"ssh_keys"`
	}
	if err := v.c.do(ctx, http.MethodGet, "/ssh-keys?per_page=500", nil, &list); err != nil {
		return "", err
	}
	want := keyBody(pub)
	for _, k := range list.SSHKeys {
		if keyBody(k.SSHKey) == want {
			return k.ID, nil
		}
	}
	var created struct {
		SSHKey struct {
			ID string `json:"id"`
		} `json:"ssh_key"`
	}
	if err := v.c.do(ctx, http.MethodPost, "/ssh-keys", map[string]any{"name": "microserver", "ssh_key": pub}, &created); err != nil {
		return "", err
	}
	return created.SSHKey.ID, nil
}

func (v *Vultr) Create(ctx context.Context, req CreateRequest) (*Instance, error) {
	osID, err := strconv.Atoi(firstNonEmpty(req.Image, vultrUbuntu2404))
	if err != nil {
		return nil, fmt.Errorf("vultr image must be a numeric os_id: %w", err)
	}
	body := map[string]any{
		"region":      req.Region,
		"plan":        req.Plan,
		"os_id":       osID,
		"label":       req.Name,
		"hostname":    req.Name,
		"enable_ipv6": true,
	}
	if req.UserData != "" {
		body["user_data"] = base64.StdEncoding.EncodeToString([]byte(req.UserData))
	}
	if req.ReuseIPRef != "" {
		body["reserved_ipv4"] = req.ReuseIPRef
	}
	if req.SSHPublicKey != "" {
		id, err := v.ensureSSHKey(ctx, req.SSHPublicKey)
		if err != nil {
			return nil, err
		}
		body["sshkey_id"] = []string{id}
	}
	var out struct {
		Instance vultrInstance `json:"instance"`
	}
	if err := v.c.do(ctx, http.MethodPost, "/instances", body, &out); err != nil {
		return nil, err
	}
	return out.Instance.instance(), nil
}

func (v *Vultr) Get(ctx context.Context, ref string) (*Instance, error) {
	var out struct {
		Instance vultrInstance `json:"instance"`
	}
	if err := v.c.do(ctx, http.MethodGet, "/instances/"+ref, nil, &out); err != nil {
		return nil, err
	}
	return out.Instance.instance(), nil
}

func (v *Vultr) Delete(ctx context.Context, ref string) error {
	return v.c.do(ctx, http.MethodDelete, "/instances/"+ref, nil, nil)
}

func (v *Vultr) RotateIP(ctx context.Context, inst *Instance, opts RotateOptions) (*RotateResult, error) {
	return recreate(ctx, v, inst, opts, func(ctx context.Context) (string, error) {
		if inst.IPRef != "" {
			// Already a reserved IP: destroying the instance just detaches it.
			return inst.IPRef, nil
		}
		var out struct {
			ReservedIP struct {
				ID string `json:"id"`
			} `json:"reserved_ip"`
		}
		err := v.c.do(ctx, http.MethodPost, "/reserved-ips/convert",
			map[string]any{"ip_address": inst.IPv4, "label": "kept-" + inst.IPv4}, &out)
		return out.ReservedIP.ID, err
	})
}

func (v *Vultr) ReleaseIP(ctx context.Context, ipRef string) error {
	return v.c.do(ctx, http.MethodDelete, "/reserved-ips/"+ipRef, nil, nil)
}

// recreate is rotation for providers whose only cheap way to a new IP is a new
// machine: create the replacement, wait for it, optionally keep the old IP via
// keep(), then delete the old machine.
func recreate(ctx context.Context, p Provider, inst *Instance, opts RotateOptions, keep func(context.Context) (string, error)) (*RotateResult, error) {
	req := opts.Create
	req.ReuseIPRef = opts.ReuseIPRef
	if req.Name == "" {
		req.Name = inst.Name
	}
	// The old machine still exists while the replacement is created, and
	// Hetzner and Linode refuse duplicate names.
	req.Name = replacementName(req.Name)
	if req.Region == "" {
		req.Region = inst.Region
	}
	created, err := p.Create(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("create replacement: %w", err)
	}
	ready, err := WaitRunning(ctx, p, created.Ref, 15*time.Minute)
	if err != nil {
		_ = p.Delete(context.WithoutCancel(ctx), created.Ref)
		return nil, err
	}
	res := &RotateResult{Instance: ready, OldIP: inst.IPv4, OldIPRef: inst.IPRef, Recreated: true}
	if opts.KeepOld && keep != nil && inst.IPv4 != "" {
		ref, err := keep(ctx)
		if err == nil && ref != "" {
			res.OldIPRef = ref
			res.OldKept = true
		}
	}
	if err := p.Delete(ctx, inst.Ref); err != nil && !isNotFound(err) {
		return res, fmt.Errorf("replacement %s is up but deleting old %s failed: %w", ready.Ref, inst.Ref, err)
	}
	if !res.OldKept && inst.IPRef != "" {
		// The old machine sat on an IP the account owns separately.
		if err := p.ReleaseIP(ctx, inst.IPRef); err != nil {
			res.OldKept = true
		}
	}
	if opts.ReuseIPRef != "" {
		res.Instance.IPRef = opts.ReuseIPRef
	}
	return res, nil
}

var rotationSuffix = regexp.MustCompile(`-r[0-9a-f]{4}$`)

// replacementName gives a recreated server a name of its own, replacing the
// suffix of an earlier rotation instead of stacking another one.
func replacementName(name string) string {
	base := rotationSuffix.ReplaceAllString(name, "")
	if len(base) > 50 {
		base = base[:50]
	}
	b := make([]byte, 2)
	_, _ = rand.Read(b)
	return base + "-r" + hex.EncodeToString(b)
}

// WaitRunning polls until the server is running with a public IPv4.
func WaitRunning(ctx context.Context, p Provider, ref string, timeout time.Duration) (*Instance, error) {
	var last *Instance
	err := poll(ctx, pollInterval*2, timeout, func() (bool, error) {
		inst, err := p.Get(ctx, ref)
		if err != nil {
			return false, err
		}
		last = inst
		return inst.Running(), nil
	})
	return last, err
}

func keyBody(k string) string {
	f := strings.Fields(k)
	if len(f) >= 2 {
		return f[1]
	}
	return k
}
