package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/ovh/go-ovh/ovh"
)

// OVHcloud Public Cloud: https://eu.api.ovh.com/console/
//
// Requests are signed with an application key/secret and a consumer key.
// Rotation recreates the instance (floating IPs need a private network and a
// gateway, which is more moving parts than a fresh hourly instance).
type OVH struct {
	c       *ovh.Client
	project string
}

type ovhCreds struct {
	Endpoint          string `json:"endpoint"`
	ApplicationKey    string `json:"application_key"`
	ApplicationSecret string `json:"application_secret"`
	ConsumerKey       string `json:"consumer_key"`
	ProjectID         string `json:"project_id"`
}

func init() {
	Register(Descriptor{
		Type: "ovh", Name: "OVHcloud Public Cloud",
		Fields: []Field{
			{Key: "endpoint", Label: "Endpoint", Hint: "ovh-eu, ovh-ca or ovh-us"},
			{Key: "application_key", Label: "Application key"},
			{Key: "application_secret", Label: "Application secret", Secret: true},
			{Key: "consumer_key", Label: "Consumer key", Secret: true},
			{Key: "project_id", Label: "Project ID (serviceName)"},
		},
		Capabilities: Capabilities{RotateMethod: RotateRecreate, DefaultImage: "Ubuntu 24.04"},
		Docs:         "https://eu.api.ovh.com/console/",
	}, func(raw json.RawMessage) (Provider, error) {
		var c ovhCreds
		if err := json.Unmarshal(raw, &c); err != nil {
			return nil, err
		}
		if c.ApplicationKey == "" || c.ApplicationSecret == "" || c.ConsumerKey == "" || c.ProjectID == "" {
			return nil, errors.New("credentials: application_key, application_secret, consumer_key and project_id are required")
		}
		client, err := ovh.NewClient(firstNonEmpty(c.Endpoint, "ovh-eu"), c.ApplicationKey, c.ApplicationSecret, c.ConsumerKey)
		if err != nil {
			return nil, err
		}
		return &OVH{c: client, project: c.ProjectID}, nil
	})
}

func (o *OVH) Type() string { return "ovh" }
func (o *OVH) Capabilities() Capabilities {
	return Capabilities{RotateMethod: RotateRecreate, DefaultImage: "Ubuntu 24.04"}
}

func (o *OVH) path(p string) string { return "/cloud/project/" + url.PathEscape(o.project) + p }

func ovhErr(err error) error {
	var apiErr *ovh.APIError
	if errors.As(err, &apiErr) && apiErr.Code == 404 {
		return fmt.Errorf("%w: %s", ErrNotFound, apiErr.Message)
	}
	return err
}

type ovhInstance struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	Region      string `json:"region"`
	IPAddresses []struct {
		IP      string `json:"ip"`
		Type    string `json:"type"`
		Version int    `json:"version"`
	} `json:"ipAddresses"`
}

func (s *ovhInstance) instance() *Instance {
	status := strings.ToLower(s.Status)
	if s.Status == "ACTIVE" {
		status = "running"
	}
	inst := &Instance{Ref: s.ID, Name: s.Name, Status: status, Region: s.Region}
	for _, a := range s.IPAddresses {
		switch {
		case a.Type != "public":
		case a.Version == 4 && inst.IPv4 == "":
			inst.IPv4 = a.IP
		case a.Version == 6 && inst.IPv6 == "":
			inst.IPv6 = hostIPv6(a.IP)
		}
	}
	return inst
}

func (o *OVH) Validate(ctx context.Context) error {
	return ovhErr(o.c.GetWithContext(ctx, o.path(""), nil))
}

func (o *OVH) Regions(ctx context.Context) ([]Option, error) {
	var names []string
	if err := o.c.GetWithContext(ctx, o.path("/region"), &names); err != nil {
		return nil, ovhErr(err)
	}
	opts := make([]Option, 0, len(names))
	for _, n := range names {
		opts = append(opts, Option{ID: n, Name: n})
	}
	return opts, nil
}

func (o *OVH) Plans(ctx context.Context, region string) ([]Option, error) {
	var flavors []struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		VCPUs     int    `json:"vcpus"`
		RAM       int    `json:"ram"`
		Disk      int    `json:"disk"`
		Available bool   `json:"available"`
		OSType    string `json:"osType"`
	}
	q := ""
	if region != "" {
		q = "?region=" + url.QueryEscape(region)
	}
	if err := o.c.GetWithContext(ctx, o.path("/flavor"+q), &flavors); err != nil {
		return nil, ovhErr(err)
	}
	var opts []Option
	for _, f := range flavors {
		if !f.Available || f.OSType == "windows" {
			continue
		}
		opts = append(opts, Option{ID: f.ID, Name: fmt.Sprintf("%s — %d vCPU / %dMB / %dGB", f.Name, f.VCPUs, f.RAM, f.Disk)})
	}
	return opts, nil
}

// imageID resolves a name such as "Ubuntu 24.04" to the region's image id.
func (o *OVH) imageID(ctx context.Context, region, image string) (string, error) {
	var images []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := o.c.GetWithContext(ctx, o.path("/image?osType=linux&region="+url.QueryEscape(region)), &images); err != nil {
		return "", ovhErr(err)
	}
	for _, im := range images {
		if im.ID == image || strings.EqualFold(im.Name, image) {
			return im.ID, nil
		}
	}
	return "", fmt.Errorf("image %q not found in %s", image, region)
}

func (o *OVH) sshKeyID(ctx context.Context, pub string) (string, error) {
	var keys []struct {
		ID        string `json:"id"`
		PublicKey string `json:"publicKey"`
	}
	if err := o.c.GetWithContext(ctx, o.path("/sshkey"), &keys); err != nil {
		return "", ovhErr(err)
	}
	for _, k := range keys {
		if keyBody(k.PublicKey) == keyBody(pub) {
			return k.ID, nil
		}
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := o.c.PostWithContext(ctx, o.path("/sshkey"), map[string]any{"name": "microserver", "publicKey": strings.TrimSpace(pub)}, &created); err != nil {
		return "", ovhErr(err)
	}
	return created.ID, nil
}

func (o *OVH) Create(ctx context.Context, req CreateRequest) (*Instance, error) {
	img, err := o.imageID(ctx, req.Region, firstNonEmpty(req.Image, "Ubuntu 24.04"))
	if err != nil {
		return nil, err
	}
	body := map[string]any{
		"name":           req.Name,
		"flavorId":       req.Plan,
		"imageId":        img,
		"region":         req.Region,
		"monthlyBilling": false,
	}
	if req.UserData != "" {
		body["userData"] = req.UserData
	}
	if req.SSHPublicKey != "" {
		id, err := o.sshKeyID(ctx, req.SSHPublicKey)
		if err != nil {
			return nil, err
		}
		body["sshKeyId"] = id
	}
	var out ovhInstance
	if err := o.c.PostWithContext(ctx, o.path("/instance"), body, &out); err != nil {
		return nil, ovhErr(err)
	}
	return out.instance(), nil
}

func (o *OVH) Get(ctx context.Context, ref string) (*Instance, error) {
	var out ovhInstance
	if err := o.c.GetWithContext(ctx, o.path("/instance/"+url.PathEscape(ref)), &out); err != nil {
		return nil, ovhErr(err)
	}
	return out.instance(), nil
}

func (o *OVH) Delete(ctx context.Context, ref string) error {
	return ovhErr(o.c.DeleteWithContext(ctx, o.path("/instance/"+url.PathEscape(ref)), nil))
}

func (o *OVH) RotateIP(ctx context.Context, inst *Instance, opts RotateOptions) (*RotateResult, error) {
	opts.KeepOld = false
	opts.ReuseIPRef = ""
	return recreate(ctx, o, inst, opts, nil)
}

func (o *OVH) ReleaseIP(context.Context, string) error { return nil }
