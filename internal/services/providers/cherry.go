package providers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Cherry Servers: https://api.cherryservers.com/doc/ (shapes follow the
// official Go SDK, github.com/cherryservers/cherrygo).
//
// Servers live in a project and are billed hourly from the team's balance.
// Floating IPs exist, but they are routed to the server and must be added to
// its network config by hand, so rotation recreates the server instead.
type Cherry struct {
	c       *restClient
	project int

	teamMu sync.Mutex
	team   int
}

type cherryCreds struct {
	Token   string          `json:"token"`
	Project json.RawMessage `json:"project_id"`
}

func init() {
	Register(Descriptor{
		Type: "cherry", Name: "Cherry Servers",
		Fields: []Field{
			{Key: "token", Label: "API token", Secret: true},
			{Key: "project_id", Label: "Project ID", Hint: "The number in the project URL: portal.cherryservers.com/projects/<id>"},
		},
		Capabilities: cherryCaps,
		Docs:         "https://api.cherryservers.com/doc/",
	}, func(raw json.RawMessage) (Provider, error) {
		var c cherryCreds
		if err := json.Unmarshal(raw, &c); err != nil {
			return nil, fmt.Errorf("credentials: %w", err)
		}
		if c.Token == "" {
			return nil, errors.New("credentials: token is required")
		}
		// The form sends strings; accept a JSON number too.
		pid, err := strconv.Atoi(strings.Trim(strings.TrimSpace(string(c.Project)), `"`))
		if err != nil || pid <= 0 {
			return nil, errors.New("credentials: project_id must be the numeric project ID")
		}
		return NewCherry("https://api.cherryservers.com/v1", c.Token, pid), nil
	})
}

var cherryCaps = Capabilities{RotateMethod: RotateRecreate, CanKeepOldIP: false, DefaultImage: "ubuntu_24_04_64bit"}

func NewCherry(base, token string, project int) *Cherry {
	return &Cherry{c: newREST(base, token), project: project}
}

func (ch *Cherry) Type() string               { return "cherry" }
func (ch *Cherry) Capabilities() Capabilities { return cherryCaps }

type cherryRegion struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Slug     string `json:"slug"`
	ISO2     string `json:"region_iso_2"`
	Location string `json:"location"`
}

type cherryPrice struct {
	Price    float64 `json:"price"`
	Currency string  `json:"currency"`
	Unit     string  `json:"unit"`
}

type cherryIP struct {
	ID            string `json:"id"`
	Address       string `json:"address"`
	AddressFamily int    `json:"address_family"`
	CIDR          string `json:"cidr"`
	Type          string `json:"type"`
}

type cherryServer struct {
	ID          int          `json:"id"`
	Name        string       `json:"name"`
	Hostname    string       `json:"hostname"`
	State       string       `json:"state"`
	Status      string       `json:"status"`
	Region      cherryRegion `json:"region"`
	Pricing     cherryPrice  `json:"pricing"`
	IPAddresses []cherryIP   `json:"ip_addresses"`
	Created     string       `json:"created_at"`
}

func (s *cherryServer) instance() *Instance {
	status := s.Status
	if status == "deployed" {
		status = "running"
	}
	name := s.Hostname
	if name == "" {
		name = s.Name
	}
	inst := &Instance{Ref: strconv.Itoa(s.ID), Name: name, Status: status, Region: s.Region.Slug}
	for _, ip := range s.IPAddresses {
		if strings.Contains(ip.Type, "private") {
			continue
		}
		addr := net.ParseIP(ip.Address)
		if addr == nil || addr.IsPrivate() {
			continue
		}
		switch {
		case addr.To4() != nil && ip.Type != "floating-ip" && inst.IPv4 == "":
			inst.IPv4 = ip.Address
		case addr.To4() == nil && inst.IPv6 == "":
			inst.IPv6 = hostIPv6(ip.Address)
		}
	}
	return inst
}

// teamID finds the team that owns the configured project (plans, prices and
// balance are per team).
func (ch *Cherry) teamID(ctx context.Context) (int, error) {
	ch.teamMu.Lock()
	defer ch.teamMu.Unlock()
	if ch.team != 0 {
		return ch.team, nil
	}
	teams, err := ch.teams(ctx)
	if err != nil {
		return 0, err
	}
	for _, t := range teams {
		for _, p := range t.Projects {
			if p.ID == ch.project {
				ch.team = t.ID
				return t.ID, nil
			}
		}
	}
	return 0, fmt.Errorf("project %d not found in any team this token can see", ch.project)
}

type cherryCredit struct {
	Remaining float64 `json:"remaining"`
	Usage     float64 `json:"usage"`
	Currency  string  `json:"currency"`
}

type cherryTeam struct {
	ID       int `json:"id"`
	Projects []struct {
		ID int `json:"id"`
	} `json:"projects"`
	Credit struct {
		Account cherryCredit `json:"account"`
		Promo   cherryCredit `json:"promo"`
	} `json:"credit"`
	Billing struct {
		Currency string `json:"currency"`
	} `json:"billing"`
}

func (ch *Cherry) teams(ctx context.Context) ([]cherryTeam, error) {
	var out []cherryTeam
	err := ch.c.do(ctx, http.MethodGet, "/teams", nil, &out)
	return out, err
}

// Validate checks the token and that the project belongs to one of its teams.
func (ch *Cherry) Validate(ctx context.Context) error {
	_, err := ch.teamID(ctx)
	return err
}

func (ch *Cherry) Regions(ctx context.Context) ([]Option, error) {
	var out []cherryRegion
	if err := ch.c.do(ctx, http.MethodGet, "/regions", nil, &out); err != nil {
		return nil, err
	}
	opts := make([]Option, 0, len(out))
	for _, r := range out {
		opts = append(opts, cherryRegionOption(r))
	}
	return opts, nil
}

func cherryRegionOption(r cherryRegion) Option {
	name := r.Location
	if name == "" {
		name = r.Name
	}
	return Option{ID: r.Slug, Name: name + " (" + r.Slug + ")", Country: strings.ToUpper(r.ISO2)}
}

type cherryPlan struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Slug     string `json:"slug"`
	Type     string `json:"type"`
	Category string `json:"category"`
	Specs    struct {
		CPUs struct {
			Cores int `json:"cores"`
		} `json:"cpus"`
		Memory struct {
			Total int    `json:"total"`
			Unit  string `json:"unit"`
		} `json:"memory"`
		Storage []struct {
			Size float64 `json:"size"`
			Unit string  `json:"unit"`
		} `json:"storage"`
		Bandwidth struct {
			Name string `json:"name"`
		} `json:"bandwidth"`
	} `json:"specs"`
	Pricing          []cherryPrice `json:"pricing"`
	AvailableRegions []struct {
		cherryRegion
		StockQty int `json:"stock_qty"`
	} `json:"available_regions"`
}

// virtual reports whether a plan is a VPS rather than a dedicated server:
// dedicated ones take far longer to deploy and cost a monthly fee.
func (p *cherryPlan) virtual() bool {
	t := strings.ToLower(p.Type + " " + p.Category + " " + p.Slug)
	if strings.Contains(t, "baremetal") || strings.Contains(t, "bare-metal") || strings.Contains(t, "bare_metal") {
		return false
	}
	return strings.Contains(t, "vps") || strings.Contains(t, "resources") || strings.Contains(t, "shared")
}

// Plans lists VPS plans in stock; with a region, only those in stock there.
func (ch *Cherry) Plans(ctx context.Context, region string) ([]Option, error) {
	team, err := ch.teamID(ctx)
	if err != nil {
		return nil, err
	}
	var out []cherryPlan
	if err := ch.c.do(ctx, http.MethodGet, fmt.Sprintf("/teams/%d/plans", team), nil, &out); err != nil {
		return nil, err
	}
	var opts []Option
	for _, p := range out {
		if !p.virtual() {
			continue
		}
		stock := 0
		for _, r := range p.AvailableRegions {
			if region == "" || strings.EqualFold(r.Slug, region) || strings.EqualFold(r.Name, region) {
				stock += r.StockQty
			}
		}
		if stock <= 0 {
			continue
		}
		o := Option{ID: p.Slug, Description: fmt.Sprintf("%d in stock", stock)}
		for _, pr := range p.Pricing {
			switch strings.ToLower(pr.Unit) {
			case "hourly":
				o.PriceHourly = pr.Price
			case "monthly":
				o.PriceMonth = pr.Price
			}
		}
		if o.PriceMonth == 0 && o.PriceHourly > 0 {
			o.PriceMonth = o.PriceHourly * 730
		}
		disk := 0.0
		for _, s := range p.Specs.Storage {
			disk += s.Size
		}
		o.Name = fmt.Sprintf("%s — %d vCPU / %d%s / %gGB", firstNonEmpty(p.Name, p.Slug),
			p.Specs.CPUs.Cores, p.Specs.Memory.Total, firstNonEmpty(p.Specs.Memory.Unit, "GB"), disk)
		if p.Specs.Bandwidth.Name != "" {
			o.Name += " / " + p.Specs.Bandwidth.Name
		}
		opts = append(opts, o)
	}
	sort.SliceStable(opts, func(i, j int) bool { return opts[i].PriceMonth < opts[j].PriceMonth })
	return opts, nil
}

// ensureSSHKey returns the id of the account key matching pub, adding it when
// missing.
func (ch *Cherry) ensureSSHKey(ctx context.Context, pub string) (string, error) {
	pub = strings.TrimSpace(pub)
	var keys []struct {
		ID  int    `json:"id"`
		Key string `json:"key"`
	}
	if err := ch.c.do(ctx, http.MethodGet, "/ssh-keys", nil, &keys); err != nil {
		return "", err
	}
	for _, k := range keys {
		if keyBody(k.Key) == keyBody(pub) {
			return strconv.Itoa(k.ID), nil
		}
	}
	var created struct {
		ID int `json:"id"`
	}
	body := map[string]any{"label": "microserver", "key": pub}
	if err := ch.c.do(ctx, http.MethodPost, "/ssh-keys", body, &created); err != nil {
		return "", fmt.Errorf("add ssh key: %w", err)
	}
	return strconv.Itoa(created.ID), nil
}

func (ch *Cherry) Create(ctx context.Context, req CreateRequest) (*Instance, error) {
	body := map[string]any{
		"plan":     req.Plan,
		"region":   req.Region,
		"image":    firstNonEmpty(req.Image, cherryCaps.DefaultImage),
		"hostname": req.Name,
		"cycle":    "hourly",
	}
	if req.UserData != "" {
		body["user_data"] = base64.StdEncoding.EncodeToString([]byte(req.UserData))
	}
	if req.SSHPublicKey != "" {
		id, err := ch.ensureSSHKey(ctx, req.SSHPublicKey)
		if err != nil {
			return nil, err
		}
		body["ssh_keys"] = []string{id}
	}
	var out cherryServer
	if err := ch.c.do(ctx, http.MethodPost, fmt.Sprintf("/projects/%d/servers", ch.project), body, &out); err != nil {
		return nil, err
	}
	return out.instance(), nil
}

func (ch *Cherry) Get(ctx context.Context, ref string) (*Instance, error) {
	var out cherryServer
	if err := ch.c.do(ctx, http.MethodGet, "/servers/"+ref, nil, &out); err != nil {
		return nil, err
	}
	if out.Status == "failed deployment" {
		return nil, fmt.Errorf("cherry server %s: deployment failed", ref)
	}
	return out.instance(), nil
}

func (ch *Cherry) Delete(ctx context.Context, ref string) error {
	return ch.c.do(ctx, http.MethodDelete, "/servers/"+ref, nil, nil)
}

// RotateIP recreates the server: a floating IP would need manual network
// config inside the server before it carries traffic.
func (ch *Cherry) RotateIP(ctx context.Context, inst *Instance, opts RotateOptions) (*RotateResult, error) {
	opts.KeepOld = false
	opts.ReuseIPRef = ""
	return recreate(ctx, ch, inst, opts, nil)
}

// ReleaseIP removes a floating IP. Rotation never keeps one, so this only
// runs for IPs added by hand.
func (ch *Cherry) ReleaseIP(ctx context.Context, ipRef string) error {
	return ch.c.do(ctx, http.MethodDelete, "/ips/"+ipRef, nil, nil)
}
