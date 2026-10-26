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

// Hetzner Cloud: https://docs.hetzner.cloud/reference/cloud
//
// A new IPv4 is obtained by swapping the server's Primary IP, which Hetzner
// only allows while the server is powered off. The disk survives, so 3x-ui
// keeps running after the swap.
type Hetzner struct{ c *restClient }

func init() {
	Register(Descriptor{
		Type:   "hetzner",
		Name:   "Hetzner Cloud",
		Fields: tokenField,
		Capabilities: Capabilities{
			RotateMethod: RotatePrimaryIPSwap, CanKeepOldIP: true, DefaultImage: "ubuntu-24.04",
		},
		Docs: "https://docs.hetzner.cloud/reference/cloud",
	}, func(raw json.RawMessage) (Provider, error) {
		tok, err := parseToken(raw)
		if err != nil {
			return nil, err
		}
		return NewHetzner("https://api.hetzner.cloud/v1", tok), nil
	})
}

func NewHetzner(base, token string) *Hetzner { return &Hetzner{c: newREST(base, token)} }

func (h *Hetzner) Type() string { return "hetzner" }
func (h *Hetzner) Capabilities() Capabilities {
	return Capabilities{RotateMethod: RotatePrimaryIPSwap, CanKeepOldIP: true, DefaultImage: "ubuntu-24.04"}
}

type hzAction struct {
	ID     int64  `json:"id"`
	Status string `json:"status"`
	Error  *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type hzServer struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	PublicNet struct {
		IPv4 *struct {
			ID int64  `json:"id"`
			IP string `json:"ip"`
		} `json:"ipv4"`
	} `json:"public_net"`
	Datacenter struct {
		Location struct {
			Name string `json:"name"`
		} `json:"location"`
	} `json:"datacenter"`
}

func (s *hzServer) instance() *Instance {
	inst := &Instance{Ref: strconv.FormatInt(s.ID, 10), Name: s.Name, Status: s.Status, Region: s.Datacenter.Location.Name}
	if s.PublicNet.IPv4 != nil {
		inst.IPv4 = s.PublicNet.IPv4.IP
		inst.IPRef = strconv.FormatInt(s.PublicNet.IPv4.ID, 10)
	}
	return inst
}

func (h *Hetzner) Validate(ctx context.Context) error {
	return h.c.do(ctx, http.MethodGet, "/locations", nil, nil)
}

func (h *Hetzner) Regions(ctx context.Context) ([]Option, error) {
	var out struct {
		Locations []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			Country     string `json:"country"`
			City        string `json:"city"`
		} `json:"locations"`
	}
	if err := h.c.do(ctx, http.MethodGet, "/locations", nil, &out); err != nil {
		return nil, err
	}
	opts := make([]Option, 0, len(out.Locations))
	for _, l := range out.Locations {
		opts = append(opts, Option{ID: l.Name, Name: l.City + " (" + l.Name + ")", Description: l.Description, Country: l.Country})
	}
	return opts, nil
}

func (h *Hetzner) Plans(ctx context.Context, region string) ([]Option, error) {
	var out struct {
		ServerTypes []struct {
			Name         string  `json:"name"`
			Description  string  `json:"description"`
			Cores        int     `json:"cores"`
			Memory       float64 `json:"memory"`
			Disk         int     `json:"disk"`
			Architecture string  `json:"architecture"`
			Deprecated   bool    `json:"deprecated"`
			// Per-location orderability. A type can still have a price in a
			// location where it can no longer be ordered.
			Locations []struct {
				Name      string `json:"name"`
				Available bool   `json:"available"`
			} `json:"locations"`
			Prices []struct {
				Location     string                 `json:"location"`
				PriceHourly  struct{ Gross string } `json:"price_hourly"`
				PriceMonthly struct{ Gross string } `json:"price_monthly"`
			} `json:"prices"`
		} `json:"server_types"`
	}
	if err := h.c.do(ctx, http.MethodGet, "/server_types?per_page=50", nil, &out); err != nil {
		return nil, err
	}
	var opts []Option
	for _, t := range out.ServerTypes {
		if t.Deprecated || (region != "" && !hzAvailableIn(t.Locations, region)) {
			continue
		}
		for _, p := range t.Prices {
			if region != "" && p.Location != region {
				continue
			}
			hr, _ := strconv.ParseFloat(p.PriceHourly.Gross, 64)
			mo, _ := strconv.ParseFloat(p.PriceMonthly.Gross, 64)
			opts = append(opts, Option{
				ID:          t.Name,
				Name:        fmt.Sprintf("%s — %d vCPU / %gGB / %dGB (%s)", t.Name, t.Cores, t.Memory, t.Disk, t.Architecture),
				PriceHourly: hr, PriceMonth: mo,
			})
			break
		}
	}
	return opts, nil
}

// ensureSSHKey returns the id of our key in the project, uploading it once.
// Passing a key at create time also stops Hetzner from emailing a root password.
func (h *Hetzner) ensureSSHKey(ctx context.Context, pub string) (int64, error) {
	pk, _, _, _, err := ssh.ParseAuthorizedKey([]byte(pub))
	if err != nil {
		return 0, fmt.Errorf("ssh key: %w", err)
	}
	fp := ssh.FingerprintLegacyMD5(pk)
	var list struct {
		SSHKeys []struct {
			ID int64 `json:"id"`
		} `json:"ssh_keys"`
	}
	if err := h.c.do(ctx, http.MethodGet, "/ssh_keys?fingerprint="+fp, nil, &list); err != nil {
		return 0, err
	}
	if len(list.SSHKeys) > 0 {
		return list.SSHKeys[0].ID, nil
	}
	var created struct {
		SSHKey struct {
			ID int64 `json:"id"`
		} `json:"ssh_key"`
	}
	name := "microserver-" + strings.ReplaceAll(fp[len(fp)-11:], ":", "")
	if err := h.c.do(ctx, http.MethodPost, "/ssh_keys", map[string]any{"name": name, "public_key": strings.TrimSpace(pub)}, &created); err != nil {
		return 0, err
	}
	return created.SSHKey.ID, nil
}

func (h *Hetzner) Create(ctx context.Context, req CreateRequest) (*Instance, error) {
	body := map[string]any{
		"name":        req.Name,
		"server_type": req.Plan,
		"image":       firstNonEmpty(req.Image, "ubuntu-24.04"),
		"location":    req.Region,
		"user_data":   req.UserData,
	}
	publicNet := map[string]any{"enable_ipv4": true, "enable_ipv6": true}
	if req.ReuseIPRef != "" {
		id, err := strconv.ParseInt(req.ReuseIPRef, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("primary ip ref: %w", err)
		}
		publicNet["ipv4"] = id
	}
	body["public_net"] = publicNet
	if req.SSHPublicKey != "" {
		id, err := h.ensureSSHKey(ctx, req.SSHPublicKey)
		if err != nil {
			return nil, err
		}
		body["ssh_keys"] = []int64{id}
	}
	var out struct {
		Server hzServer `json:"server"`
	}
	if err := h.c.do(ctx, http.MethodPost, "/servers", body, &out); err != nil {
		return nil, err
	}
	return out.Server.instance(), nil
}

func (h *Hetzner) get(ctx context.Context, ref string) (*hzServer, error) {
	var out struct {
		Server hzServer `json:"server"`
	}
	if err := h.c.do(ctx, http.MethodGet, "/servers/"+ref, nil, &out); err != nil {
		return nil, err
	}
	return &out.Server, nil
}

func (h *Hetzner) Get(ctx context.Context, ref string) (*Instance, error) {
	s, err := h.get(ctx, ref)
	if err != nil {
		return nil, err
	}
	return s.instance(), nil
}

func (h *Hetzner) Delete(ctx context.Context, ref string) error {
	return h.c.do(ctx, http.MethodDelete, "/servers/"+ref, nil, nil)
}

// waitAction follows an async action to completion.
func (h *Hetzner) waitAction(ctx context.Context, a *hzAction) error {
	if a == nil || a.ID == 0 {
		return nil
	}
	return poll(ctx, pollInterval, 10*time.Minute, func() (bool, error) {
		var out struct {
			Action hzAction `json:"action"`
		}
		if err := h.c.do(ctx, http.MethodGet, fmt.Sprintf("/actions/%d", a.ID), nil, &out); err != nil {
			return false, err
		}
		switch out.Action.Status {
		case "success":
			return true, nil
		case "error":
			msg := "action failed"
			if out.Action.Error != nil {
				msg = out.Action.Error.Code + ": " + out.Action.Error.Message
			}
			return false, fmt.Errorf("hetzner action %d: %s", a.ID, msg)
		}
		return false, nil
	})
}

func (h *Hetzner) action(ctx context.Context, path string, body any) error {
	var out struct {
		Action *hzAction `json:"action"`
	}
	if err := h.c.do(ctx, http.MethodPost, path, body, &out); err != nil {
		return err
	}
	return h.waitAction(ctx, out.Action)
}

// RotateIP swaps the Primary IPv4: poweroff → unassign old → assign new (or a
// kept one) → poweron, then deletes the old IP unless it should be kept.
func (h *Hetzner) RotateIP(ctx context.Context, inst *Instance, opts RotateOptions) (*RotateResult, error) {
	s, err := h.get(ctx, inst.Ref)
	if err != nil {
		return nil, err
	}
	// Once the swap starts it has to finish: a cancel between unassigning the
	// old IP and assigning the new one would leave the server without an IPv4.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Minute)
	defer cancel()
	res := &RotateResult{}
	if s.PublicNet.IPv4 != nil {
		res.OldIP = s.PublicNet.IPv4.IP
		res.OldIPRef = strconv.FormatInt(s.PublicNet.IPv4.ID, 10)
	}

	if s.Status != "off" {
		if err := h.action(ctx, "/servers/"+inst.Ref+"/actions/poweroff", nil); err != nil {
			return nil, fmt.Errorf("poweroff: %w", err)
		}
	}
	// Whatever happens next, try to leave the server running.
	poweredOn := false
	defer func() {
		if !poweredOn {
			_ = h.action(context.WithoutCancel(ctx), "/servers/"+inst.Ref+"/actions/poweron", nil)
		}
	}()

	if res.OldIPRef != "" {
		if opts.KeepOld {
			// auto_delete=false so deleting the server later cannot take the kept IP with it.
			if err := h.c.do(ctx, http.MethodPut, "/primary_ips/"+res.OldIPRef, map[string]any{"auto_delete": false}, nil); err != nil {
				return nil, fmt.Errorf("keep old ip: %w", err)
			}
		}
		if err := h.action(ctx, "/primary_ips/"+res.OldIPRef+"/actions/unassign", nil); err != nil {
			return nil, fmt.Errorf("unassign old ip: %w", err)
		}
	}

	serverID, _ := strconv.ParseInt(inst.Ref, 10, 64)
	if opts.ReuseIPRef != "" {
		if err := h.action(ctx, "/primary_ips/"+opts.ReuseIPRef+"/actions/assign",
			map[string]any{"assignee_id": serverID, "assignee_type": "server"}); err != nil {
			return nil, fmt.Errorf("assign kept ip: %w", err)
		}
		_ = h.c.do(ctx, http.MethodPut, "/primary_ips/"+opts.ReuseIPRef, map[string]any{"auto_delete": true}, nil)
	} else {
		var created struct {
			PrimaryIP struct {
				ID int64  `json:"id"`
				IP string `json:"ip"`
			} `json:"primary_ip"`
			Action *hzAction `json:"action"`
		}
		body := map[string]any{
			"name":          fmt.Sprintf("%s-%d", s.Name, time.Now().Unix()),
			"type":          "ipv4",
			"assignee_type": "server",
			"assignee_id":   serverID,
			"auto_delete":   true,
		}
		if err := h.c.do(ctx, http.MethodPost, "/primary_ips", body, &created); err != nil {
			return nil, fmt.Errorf("create primary ip: %w", err)
		}
		if err := h.waitAction(ctx, created.Action); err != nil {
			return nil, err
		}
	}

	if err := h.action(ctx, "/servers/"+inst.Ref+"/actions/poweron", nil); err != nil {
		return nil, fmt.Errorf("poweron: %w", err)
	}
	poweredOn = true

	if res.OldIPRef != "" {
		if opts.KeepOld {
			res.OldKept = true
		} else if err := h.c.do(ctx, http.MethodDelete, "/primary_ips/"+res.OldIPRef, nil, nil); err != nil {
			// The new IP is live; a leftover old IP only costs money, so report
			// it as kept rather than failing the rotation.
			res.OldKept = true
		}
	}

	fresh, err := h.get(ctx, inst.Ref)
	if err != nil {
		return nil, err
	}
	res.Instance = fresh.instance()
	return res, nil
}

func (h *Hetzner) ReleaseIP(ctx context.Context, ipRef string) error {
	return h.c.do(ctx, http.MethodDelete, "/primary_ips/"+ipRef, nil, nil)
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

// hzAvailableIn reports whether a server type can be ordered in location. Old
// API responses without the locations list are treated as available.
func hzAvailableIn(locs []struct {
	Name      string `json:"name"`
	Available bool   `json:"available"`
}, location string) bool {
	if len(locs) == 0 {
		return true
	}
	for _, l := range locs {
		if l.Name == location {
			return l.Available
		}
	}
	return false
}
