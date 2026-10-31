// Package providers talks to cloud provider APIs behind one interface.
package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
)

// How a provider gets a server a new public IPv4.
const (
	RotatePrimaryIPSwap = "primary_ip_swap" // Hetzner: power off, swap the Primary IP, power on
	RotateReservedIP    = "reserved_ip"     // DigitalOcean: attach a new reserved IP, no reboot
	RotateRecreate      = "recreate"        // create a new server and delete the old one
)

type Option struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Description string  `json:"description,omitempty"`
	Country     string  `json:"country,omitempty"`
	PriceHourly float64 `json:"price_hourly,omitempty"`
	PriceMonth  float64 `json:"price_monthly,omitempty"`
}

type CreateRequest struct {
	Name         string
	Region       string
	Plan         string
	Image        string
	SSHPublicKey string
	UserData     string
	// ReuseIPRef asks the provider to give the new server an IP the account
	// already owns, when the provider supports that at create time.
	ReuseIPRef string
}

type Instance struct {
	Ref    string `json:"ref"`
	Name   string `json:"name"`
	Status string `json:"status"` // provider status, normalised to "running" when ready
	IPv4   string `json:"ipv4"`
	IPRef  string `json:"ip_ref"`
	// IPv6 is the server's public IPv6 address (empty when it has none).
	IPv6   string `json:"ipv6"`
	Region string `json:"region"`
}

// hostIPv6 turns what providers report for IPv6 into one usable address:
// "addr/128" loses its prefix length, and a routed prefix ("2a01::/64", as
// Hetzner assigns) becomes its ::1, the address the server's image configures.
func hostIPv6(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	ip, n, err := net.ParseCIDR(s)
	if err != nil {
		if p := net.ParseIP(s); p != nil && p.To4() == nil {
			return p.String()
		}
		return ""
	}
	if ip.To4() != nil {
		return ""
	}
	if ones, _ := n.Mask.Size(); ones < 128 && ip.Equal(n.IP) {
		ip = append(net.IP(nil), n.IP...)
		ip[15] |= 1
	}
	return ip.String()
}

func (i *Instance) Running() bool { return i != nil && i.Status == "running" && i.IPv4 != "" }

type RotateOptions struct {
	// KeepOld keeps the old IP owned by the account instead of releasing it,
	// so it can be retried once Iran stops filtering it.
	KeepOld bool
	// ReuseIPRef attaches an IP the account already owns instead of a new one.
	ReuseIPRef string
	// Create carries the original create parameters for recreate-based rotation.
	Create CreateRequest
	// IPv6 is set for servers used over IPv6: swapping the IPv4 would not
	// change the address clients use, so providers rotate by recreating.
	IPv6 bool
}

type RotateResult struct {
	Instance *Instance
	OldIP    string
	OldIPRef string
	// OldKept is true when the old IP is still owned (and can be reused).
	OldKept bool
	// Recreated means a different machine now serves the IP: everything on
	// the old disk (3x-ui included) is gone and must be installed again.
	Recreated bool
}

type Capabilities struct {
	RotateMethod string `json:"rotate_method"`
	CanKeepOldIP bool   `json:"can_keep_old_ip"`
	DefaultImage string `json:"default_image"`
}

type Provider interface {
	Type() string
	Capabilities() Capabilities
	// Validate checks the credentials with a cheap read-only call.
	Validate(ctx context.Context) error
	Regions(ctx context.Context) ([]Option, error)
	Plans(ctx context.Context, region string) ([]Option, error)
	Create(ctx context.Context, req CreateRequest) (*Instance, error)
	Get(ctx context.Context, ref string) (*Instance, error)
	Delete(ctx context.Context, ref string) error
	RotateIP(ctx context.Context, inst *Instance, opts RotateOptions) (*RotateResult, error)
	// ReleaseIP gives back an IP kept by an earlier rotation.
	ReleaseIP(ctx context.Context, ipRef string) error
}

// ErrNotFound is returned by Get and Delete when the server no longer exists.
var ErrNotFound = errors.New("not found at provider")

// Factory builds a provider from the decrypted credentials JSON.
type Factory func(credentials json.RawMessage) (Provider, error)

type Field struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Secret bool   `json:"secret"`
	Hint   string `json:"hint,omitempty"`
}

type Descriptor struct {
	Type         string       `json:"type"`
	Name         string       `json:"name"`
	Fields       []Field      `json:"fields"`
	Capabilities Capabilities `json:"capabilities"`
	Docs         string       `json:"docs"`
	new          Factory
}

var (
	regMu    sync.RWMutex
	registry = map[string]Descriptor{}
)

func Register(d Descriptor, f Factory) {
	regMu.Lock()
	d.new = f
	registry[d.Type] = d
	regMu.Unlock()
}

func Descriptors() []Descriptor {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]Descriptor, 0, len(registry))
	for _, d := range registry {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func New(typ string, credentials json.RawMessage) (Provider, error) {
	regMu.RLock()
	d, ok := registry[typ]
	regMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown provider %q", typ)
	}
	return d.new(credentials)
}

// tokenCreds is the credentials shape of every bearer-token provider.
type tokenCreds struct {
	Token string `json:"token"`
}

func parseToken(raw json.RawMessage) (string, error) {
	var c tokenCreds
	if err := json.Unmarshal(raw, &c); err != nil {
		return "", fmt.Errorf("credentials: %w", err)
	}
	if c.Token == "" {
		return "", errors.New("credentials: token is required")
	}
	return c.Token, nil
}

var tokenField = []Field{{Key: "token", Label: "API token", Secret: true}}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

func isNotFound(err error) bool { return errors.Is(err, ErrNotFound) }

// capacityHints are fragments providers use when an account or location has
// no room: Hetzner resource_limit_exceeded / resource_unavailable, Vultr
// "maximum", DigitalOcean "droplet limit", OVH "quota", Linode "limit".
var capacityHints = []string{"limit", "unavailable", "quota", "maximum", "insufficient", "capacity", "no available"}

// IsCapacityErr reports whether a create failed because there is no room
// (account limit, quota, or the location being sold out), as opposed to a
// bad request. Automatic placement treats it as "this location is full".
func IsCapacityErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, h := range capacityHints {
		if strings.Contains(msg, h) {
			return true
		}
	}
	return false
}
