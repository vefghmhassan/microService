package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// DigitalOcean: https://docs.digitalocean.com/reference/api/
//
// A new IPv4 comes from a Reserved IP attached to the droplet; no reboot, and
// reserved IPs are free while attached.
type DigitalOcean struct{ c *restClient }

func init() {
	caps := Capabilities{RotateMethod: RotateReservedIP, CanKeepOldIP: true, DefaultImage: "ubuntu-24-04-x64"}
	Register(Descriptor{
		Type: "digitalocean", Name: "DigitalOcean", Fields: tokenField, Capabilities: caps,
		Docs: "https://docs.digitalocean.com/reference/api/",
	}, func(raw json.RawMessage) (Provider, error) {
		tok, err := parseToken(raw)
		if err != nil {
			return nil, err
		}
		return NewDigitalOcean("https://api.digitalocean.com/v2", tok), nil
	})
}

func NewDigitalOcean(base, token string) *DigitalOcean {
	return &DigitalOcean{c: newREST(base, token)}
}

func (d *DigitalOcean) Type() string { return "digitalocean" }
func (d *DigitalOcean) Capabilities() Capabilities {
	return Capabilities{RotateMethod: RotateReservedIP, CanKeepOldIP: true, DefaultImage: "ubuntu-24-04-x64"}
}

type doDroplet struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Status   string `json:"status"`
	Networks struct {
		V4 []struct {
			IP   string `json:"ip_address"`
			Type string `json:"type"`
		} `json:"v4"`
		V6 []struct {
			IP   string `json:"ip_address"`
			Type string `json:"type"`
		} `json:"v6"`
	} `json:"networks"`
	Region struct {
		Slug string `json:"slug"`
	} `json:"region"`
}

func (s *doDroplet) instance() *Instance {
	status := s.Status
	if status == "active" {
		status = "running"
	}
	inst := &Instance{Ref: strconv.FormatInt(s.ID, 10), Name: s.Name, Status: status, Region: s.Region.Slug}
	for _, n := range s.Networks.V4 {
		if n.Type == "public" {
			inst.IPv4 = n.IP
			break
		}
	}
	for _, n := range s.Networks.V6 {
		if n.Type == "public" {
			inst.IPv6 = hostIPv6(n.IP)
			break
		}
	}
	return inst
}

func (d *DigitalOcean) Validate(ctx context.Context) error {
	return d.c.do(ctx, http.MethodGet, "/account", nil, nil)
}

func (d *DigitalOcean) Regions(ctx context.Context) ([]Option, error) {
	var out struct {
		Regions []struct {
			Slug      string `json:"slug"`
			Name      string `json:"name"`
			Available bool   `json:"available"`
		} `json:"regions"`
	}
	if err := d.c.do(ctx, http.MethodGet, "/regions?per_page=200", nil, &out); err != nil {
		return nil, err
	}
	var opts []Option
	for _, r := range out.Regions {
		if r.Available {
			opts = append(opts, Option{ID: r.Slug, Name: r.Name + " (" + r.Slug + ")"})
		}
	}
	return opts, nil
}

func (d *DigitalOcean) Plans(ctx context.Context, region string) ([]Option, error) {
	var out struct {
		Sizes []struct {
			Slug         string   `json:"slug"`
			Memory       int      `json:"memory"`
			VCPUs        int      `json:"vcpus"`
			Disk         int      `json:"disk"`
			PriceHourly  float64  `json:"price_hourly"`
			PriceMonthly float64  `json:"price_monthly"`
			Regions      []string `json:"regions"`
			Available    bool     `json:"available"`
		} `json:"sizes"`
	}
	if err := d.c.do(ctx, http.MethodGet, "/sizes?per_page=200", nil, &out); err != nil {
		return nil, err
	}
	var opts []Option
	for _, s := range out.Sizes {
		if !s.Available || (region != "" && !contains(s.Regions, region)) {
			continue
		}
		opts = append(opts, Option{
			ID:          s.Slug,
			Name:        fmt.Sprintf("%s — %d vCPU / %dMB / %dGB", s.Slug, s.VCPUs, s.Memory, s.Disk),
			PriceHourly: s.PriceHourly, PriceMonth: s.PriceMonthly,
		})
	}
	return opts, nil
}

func (d *DigitalOcean) ensureSSHKey(ctx context.Context, pub string) (string, error) {
	pk, _, _, _, err := ssh.ParseAuthorizedKey([]byte(pub))
	if err != nil {
		return "", err
	}
	fp := ssh.FingerprintLegacyMD5(pk)
	err = d.c.do(ctx, http.MethodGet, "/account/keys/"+fp, nil, nil)
	if err == nil {
		return fp, nil
	}
	if !isNotFound(err) {
		return "", err
	}
	body := map[string]any{"name": "microserver", "public_key": strings.TrimSpace(pub)}
	if err := d.c.do(ctx, http.MethodPost, "/account/keys", body, nil); err != nil {
		return "", err
	}
	return fp, nil
}

func (d *DigitalOcean) Create(ctx context.Context, req CreateRequest) (*Instance, error) {
	body := map[string]any{
		"name":      req.Name,
		"region":    req.Region,
		"size":      req.Plan,
		"image":     firstNonEmpty(req.Image, "ubuntu-24-04-x64"),
		"user_data": req.UserData,
		"ipv6":      true,
	}
	if req.SSHPublicKey != "" {
		fp, err := d.ensureSSHKey(ctx, req.SSHPublicKey)
		if err != nil {
			return nil, err
		}
		body["ssh_keys"] = []string{fp}
	}
	var out struct {
		Droplet doDroplet `json:"droplet"`
	}
	if err := d.c.do(ctx, http.MethodPost, "/droplets", body, &out); err != nil {
		return nil, err
	}
	return out.Droplet.instance(), nil
}

func (d *DigitalOcean) Get(ctx context.Context, ref string) (*Instance, error) {
	var out struct {
		Droplet doDroplet `json:"droplet"`
	}
	if err := d.c.do(ctx, http.MethodGet, "/droplets/"+ref, nil, &out); err != nil {
		return nil, err
	}
	return out.Droplet.instance(), nil
}

func (d *DigitalOcean) Delete(ctx context.Context, ref string) error {
	return d.c.do(ctx, http.MethodDelete, "/droplets/"+ref, nil, nil)
}

func (d *DigitalOcean) ipAction(ctx context.Context, ip string, body map[string]any) error {
	var out struct {
		Action struct {
			ID     int64  `json:"id"`
			Status string `json:"status"`
		} `json:"action"`
	}
	if err := d.c.do(ctx, http.MethodPost, "/reserved_ips/"+ip+"/actions", body, &out); err != nil {
		return err
	}
	id := out.Action.ID
	return poll(ctx, pollInterval, 5*time.Minute, func() (bool, error) {
		var a struct {
			Action struct {
				Status string `json:"status"`
			} `json:"action"`
		}
		if err := d.c.do(ctx, http.MethodGet, fmt.Sprintf("/actions/%d", id), nil, &a); err != nil {
			return false, err
		}
		switch a.Action.Status {
		case "completed":
			return true, nil
		case "errored":
			return false, fmt.Errorf("reserved ip action %d errored", id)
		}
		return false, nil
	})
}

// RotateIP moves the droplet onto a new (or kept) reserved IP. The IP the
// droplet was created with stays as its anchor; clients only ever see the
// reserved one.
func (d *DigitalOcean) RotateIP(ctx context.Context, inst *Instance, opts RotateOptions) (*RotateResult, error) {
	if opts.IPv6 {
		// Reserved IPs are IPv4 only; the droplet's IPv6 changes with the droplet.
		opts.KeepOld = false
		opts.ReuseIPRef = ""
		return recreate(ctx, d, inst, opts, nil)
	}
	// Once the swap starts it has to finish: a cancel between unassigning the
	// old IP and assigning the new one would leave the server without an IPv4.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Minute)
	defer cancel()
	res := &RotateResult{OldIP: inst.IPv4, OldIPRef: inst.IPRef}
	dropletID, _ := strconv.ParseInt(inst.Ref, 10, 64)

	if inst.IPRef != "" {
		if err := d.ipAction(ctx, inst.IPRef, map[string]any{"type": "unassign"}); err != nil && !isNotFound(err) {
			return nil, fmt.Errorf("unassign old reserved ip: %w", err)
		}
	}

	newIP := opts.ReuseIPRef
	if newIP == "" {
		region := inst.Region
		if region == "" {
			cur, err := d.Get(ctx, inst.Ref)
			if err != nil {
				return nil, err
			}
			region = cur.Region
		}
		var created struct {
			ReservedIP struct {
				IP string `json:"ip"`
			} `json:"reserved_ip"`
		}
		if err := d.c.do(ctx, http.MethodPost, "/reserved_ips", map[string]any{"region": region}, &created); err != nil {
			return nil, fmt.Errorf("create reserved ip: %w", err)
		}
		newIP = created.ReservedIP.IP
	}
	if err := d.ipAction(ctx, newIP, map[string]any{"type": "assign", "droplet_id": dropletID}); err != nil {
		return nil, fmt.Errorf("assign reserved ip: %w", err)
	}

	if inst.IPRef != "" {
		if opts.KeepOld {
			res.OldKept = true
		} else if err := d.ReleaseIP(ctx, inst.IPRef); err != nil {
			res.OldKept = true
		}
	}
	// The first rotation leaves the droplet's own IP in place: it is not ours
	// to release, so report it as not kept.
	res.Instance = &Instance{Ref: inst.Ref, Name: inst.Name, Status: "running", IPv4: newIP, IPRef: newIP, Region: inst.Region}
	return res, nil
}

func (d *DigitalOcean) ReleaseIP(ctx context.Context, ipRef string) error {
	return d.c.do(ctx, http.MethodDelete, "/reserved_ips/"+ipRef, nil, nil)
}
