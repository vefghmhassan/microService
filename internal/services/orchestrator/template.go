package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"

	"gorm.io/gorm"

	"microserver/internal/models"
	"microserver/internal/services/cleanip"
	"microserver/internal/services/linkgen"
	"microserver/internal/services/settings"
	"microserver/internal/services/xui"
)

// ManagedPrefix marks inbounds this panel owns on a 3x-ui server. Inbounds
// without it are left alone, so an operator can still add one by hand.
const ManagedPrefix = "[ms] "

// Snapshot is the shared template. Loaded whole it holds both IP families:
// Xray/Hash are the IPv4 ones, XrayV6/HashV6 the IPv6 ones, and Inbounds
// every inbound. For(s) narrows it to what one server gets.
type Snapshot struct {
	Xray     models.XrayTemplate
	XrayV6   models.XrayTemplate
	Inbounds []models.InboundTemplate
	Hash     string
	HashV6   string
}

// TemplateFamilies are the inbound template families a server of the given
// IP family receives.
func TemplateFamilies(family string) []string {
	return []string{models.FamilyBoth, models.NormFamily(family)}
}

// InboundFamily is an inbound template's family ("both" when unset).
func InboundFamily(t models.InboundTemplate) string {
	switch t.Family {
	case models.FamilyV4, models.FamilyV6:
		return t.Family
	}
	return models.FamilyBoth
}

// forFamily reports whether inbound t goes on servers of the given family.
func forFamily(t models.InboundTemplate, family string) bool {
	f := InboundFamily(t)
	return f == models.FamilyBoth || f == models.NormFamily(family)
}

// HasXray reports whether an xray row carries anything; an empty IPv6 row
// means IPv6 servers use the IPv4 one.
func HasXray(x models.XrayTemplate) bool {
	return strings.TrimSpace(x.Full+x.Outbounds+x.Routing+x.DNS+x.Observatory) != ""
}

// XrayFor is the xray template servers of the family get.
func (snap *Snapshot) XrayFor(family string) models.XrayTemplate {
	if models.NormFamily(family) == models.FamilyV6 && HasXray(snap.XrayV6) {
		return snap.XrayV6
	}
	return snap.Xray
}

// HashFor is the template hash of one IP family. Servers whose AppliedHash
// differs from their family's hash have drifted.
func (snap *Snapshot) HashFor(family string) string {
	if models.NormFamily(family) == models.FamilyV6 {
		return snap.HashV6
	}
	return snap.Hash
}

// LoadSnapshot reads the shared template and its per-family content hashes.
func LoadSnapshot(db *gorm.DB) (*Snapshot, error) {
	var snap Snapshot
	if err := db.FirstOrCreate(&snap.Xray, models.XrayTemplate{ID: models.XrayV4ID}).Error; err != nil {
		return nil, err
	}
	if err := db.FirstOrCreate(&snap.XrayV6, models.XrayTemplate{ID: models.XrayV6ID}).Error; err != nil {
		return nil, err
	}
	if err := db.Order("port").Find(&snap.Inbounds).Error; err != nil {
		return nil, err
	}
	snap.Hash = snap.hash(models.FamilyV4)
	snap.HashV6 = snap.hash(models.FamilyV6)
	return &snap, nil
}

// hash is the content hash of what a server of the family gets. The IPv4
// hash is computed as before the families existed, so IPv4 servers stay in
// sync across the upgrade.
func (snap *Snapshot) hash(family string) string {
	x := snap.XrayFor(family)
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s\x00", canonical(x.Outbounds), canonical(x.Routing), canonical(x.DNS), canonical(x.Observatory))
	if x.Full != "" {
		fmt.Fprintf(h, "full:%s\x00", canonical(x.Full))
	}
	ins := []models.InboundTemplate{}
	for _, in := range snap.Inbounds {
		if forFamily(in, family) {
			ins = append(ins, in)
		}
	}
	sort.Slice(ins, func(i, j int) bool { return ins[i].Port < ins[j].Port })
	for _, in := range ins {
		fmt.Fprintf(h, "%d|%s|%s|%t|%s|%s|%s\x00", in.Port, in.Remark, in.Protocol, in.Enabled,
			canonical(in.Settings), canonical(in.StreamSettings), canonical(in.Sniffing))
		if in.CDN {
			fmt.Fprint(h, "cdn\x00")
		}
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// NeedsCert reports whether t only works with the zone certificate on the
// server: a CDN inbound with TLS points at /root/cert/*.
func NeedsCert(t models.InboundTemplate) bool {
	return t.CDN && linkgen.Security(t.StreamSettings) == "tls"
}

// For is the snapshot as it applies to one server: the inbounds and xray
// template of its IP family, with Hash its family's hash. CDN inbounds that
// need the zone certificate are left out where the server has none, since
// xray would not start with a missing certificate file; the hash stays the
// family's, so such a server is still in sync.
func (snap *Snapshot) For(s *models.Server) *Snapshot {
	fam := s.Family()
	certOK := s.SSL && s.Hostname != ""
	out := *snap
	out.Xray = snap.XrayFor(fam)
	out.XrayV6 = out.Xray
	out.Hash = snap.HashFor(fam)
	out.HashV6 = out.Hash
	out.Inbounds = nil
	for _, t := range snap.Inbounds {
		if forFamily(t, fam) && (certOK || !NeedsCert(t)) {
			out.Inbounds = append(out.Inbounds, t)
		}
	}
	return &out
}

// sniHost is what a CDN inbound's links send as SNI and Host: the server's
// hostname, which Cloudflare routes by. Other inbounds keep their own.
func sniHost(t models.InboundTemplate, host string) string {
	if t.CDN && host != "" && net.ParseIP(host) == nil {
		return host
	}
	return ""
}

// canonical re-encodes JSON so whitespace and key order do not count as drift.
func canonical(s string) string {
	if strings.TrimSpace(s) == "" {
		return ""
	}
	var v any
	if json.Unmarshal([]byte(s), &v) != nil {
		return s
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// BumpTemplate records a template edit: version++ and the new hash, and marks
// every server that no longer matches as drifted.
func BumpTemplate(db *gorm.DB) (*Snapshot, error) {
	snap, err := LoadSnapshot(db)
	if err != nil {
		return nil, err
	}
	snap.Xray.Version++
	snap.Xray.Hash = snap.Hash
	if err := db.Save(&snap.Xray).Error; err != nil {
		return nil, err
	}
	snap.XrayV6.Version = snap.Xray.Version
	snap.XrayV6.Hash = snap.HashV6
	if err := db.Save(&snap.XrayV6).Error; err != nil {
		return nil, err
	}
	gone := []string{models.ServerDeleted, models.ServerDeleting}
	db.Model(&models.Server{}).
		Where("applied_hash <> '' AND applied_hash <> ? AND COALESCE(ip_family, 'v4') <> ? AND status NOT IN ?", snap.Hash, models.FamilyV6, gone).
		Update("sync_status", models.SyncDrift)
	db.Model(&models.Server{}).
		Where("applied_hash <> '' AND applied_hash <> ? AND ip_family = ? AND status NOT IN ?", snap.HashV6, models.FamilyV6, gone).
		Update("sync_status", models.SyncDrift)
	return snap, nil
}

// Apply makes a panel match the snapshot: managed inbounds are created,
// updated in place (matched by port) or removed, and the shared outbounds,
// routing and DNS replace those keys in the panel's xray template. Every other
// key of the template (api, policy, stats, …) stays as 3x-ui wrote it.
func Apply(ctx context.Context, p Panel, snap *Snapshot, logf func(string, ...any)) error {
	existing, err := p.ListInbounds(ctx)
	if err != nil {
		return fmt.Errorf("list inbounds: %w", err)
	}
	byPort := map[int]xui.Inbound{}
	for _, in := range existing {
		byPort[in.Port] = in
	}
	want := map[int]bool{}
	for _, t := range snap.Inbounds {
		if !t.Enabled {
			continue
		}
		want[t.Port] = true
		in := xui.Inbound{
			Remark: ManagedPrefix + t.Remark, Enable: true, Port: t.Port, Protocol: t.Protocol,
			Settings:       xui.JSONText(linkgen.NormalizeSettings(orDefault(t.Settings, "{}"))),
			StreamSettings: xui.JSONText(orDefault(t.StreamSettings, `{"network":"tcp","security":"none"}`)),
			Sniffing:       xui.JSONText(orDefault(t.Sniffing, `{"enabled":true,"destOverride":["http","tls","quic"]}`)),
		}
		if cur, ok := byPort[t.Port]; ok {
			if !strings.HasPrefix(cur.Remark, ManagedPrefix) {
				return fmt.Errorf("port %d is used by unmanaged inbound %q on this server", t.Port, cur.Remark)
			}
			if err := p.UpdateInbound(ctx, cur.ID, in); err != nil {
				return fmt.Errorf("update inbound %d: %w", t.Port, err)
			}
			logf("updated inbound %s on port %d", t.Remark, t.Port)
		} else {
			if err := p.AddInbound(ctx, in); err != nil {
				return fmt.Errorf("add inbound %d: %w", t.Port, err)
			}
			logf("added inbound %s on port %d", t.Remark, t.Port)
		}
	}
	for _, cur := range existing {
		if strings.HasPrefix(cur.Remark, ManagedPrefix) && !want[cur.Port] {
			if err := p.DeleteInbound(ctx, cur.ID); err != nil {
				return fmt.Errorf("delete inbound %d: %w", cur.Port, err)
			}
			logf("removed inbound %s on port %d", cur.Remark, cur.Port)
		}
	}

	{
		setting, err := p.XraySetting(ctx)
		if err != nil {
			return fmt.Errorf("read xray template: %w", err)
		}
		if snap.Xray.Full != "" {
			var full map[string]any
			if err := json.Unmarshal([]byte(snap.Xray.Full), &full); err != nil {
				return fmt.Errorf("full xray template is not valid JSON: %w", err)
			}
			setting = full
		}
		// The GeoIP report reads client addresses from the access log.
		withAccessLog(setting)
		for key, val := range map[string]string{"outbounds": snap.Xray.Outbounds, "routing": snap.Xray.Routing, "dns": snap.Xray.DNS, "observatory": snap.Xray.Observatory} {
			if snap.Xray.Full != "" || strings.TrimSpace(val) == "" {
				continue
			}
			var v any
			if err := json.Unmarshal([]byte(val), &v); err != nil {
				return fmt.Errorf("template %s is not valid JSON: %w", key, err)
			}
			setting[key] = v
		}
		if err := p.UpdateXraySetting(ctx, setting); err != nil {
			return fmt.Errorf("update xray template: %w", err)
		}
		logf("applied xray template v%d", snap.Xray.Version)
	}
	if err := p.RestartXray(ctx); err != nil {
		return fmt.Errorf("restart xray: %w", err)
	}
	return nil
}

func orDefault(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

// UploadEntries is what goes to vpn-pannel for host: one share link per
// client, or a compact client JSON config for inbounds marked UploadJSON. A
// JSON conversion that fails falls back to the plain link and is reported in
// the returned error.
func UploadEntries(snap *Snapshot, host string) ([]string, error) {
	var out []string
	var errs []error
	for _, t := range snap.Inbounds {
		if !t.Enabled || !t.Upload {
			continue
		}
		ls, err := linkgen.LinksVia(t.Protocol, t.Settings, t.StreamSettings, host, sniHost(t, host), t.Port, t.Remark)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", t.Remark, err))
			continue
		}
		for _, l := range ls {
			entry := l.URL
			if t.UploadJSON {
				if j, err := linkgen.ClientJSON(l.URL); err != nil {
					errs = append(errs, fmt.Errorf("%s: json config: %w", t.Remark, err))
				} else {
					entry = j
				}
			}
			out = append(out, entry)
		}
	}
	return out, errors.Join(errs...)
}

// Links builds the share links of every enabled inbound marked for upload,
// pointing at host.
func Links(snap *Snapshot, host string) ([]linkgen.Link, error) {
	return buildLinks(snap, host, "", true)
}

// SubLinks builds the share links of every enabled inbound, uploaded or not,
// for the all-servers subscription; prefix goes in front of each label so
// the client shows which server a config belongs to.
func SubLinks(snap *Snapshot, host, prefix string) ([]linkgen.Link, error) {
	return buildLinks(snap, host, prefix, false)
}

func buildLinks(snap *Snapshot, host, prefix string, onlyUpload bool) ([]linkgen.Link, error) {
	var out []linkgen.Link
	var errs []error
	for _, t := range snap.Inbounds {
		if !t.Enabled || (onlyUpload && !t.Upload) {
			continue
		}
		ls, err := linkgen.LinksVia(t.Protocol, t.Settings, t.StreamSettings, host, sniHost(t, host), t.Port, prefix+t.Remark)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", t.Remark, err))
			continue
		}
		out = append(out, ls...)
	}
	return out, errors.Join(errs...)
}

// CleanLinks are the extra links of a proxied server: every CDN inbound
// once per clean Cloudflare address, with the server's hostname as SNI/Host
// so Cloudflare still routes them to this server. They are shown in the
// panel and the bot only; vpn-pannel finds configs by address, which clean
// IPs shared by every server would make ambiguous.
func CleanLinks(snap *Snapshot, s *models.Server, ips []models.CleanIP) ([]linkgen.Link, error) {
	if !s.DNSProxied || s.Hostname == "" || len(ips) == 0 {
		return nil, nil
	}
	var out []linkgen.Link
	var errs []error
	for _, t := range snap.For(s).Inbounds {
		if !t.Enabled || !t.Upload || !t.CDN {
			continue
		}
		for _, ip := range ips {
			remark := strings.TrimSpace(t.Remark + " " + ip.Operator + " " + ip.Address)
			ls, err := linkgen.LinksVia(t.Protocol, t.Settings, t.StreamSettings, ip.Address, s.Hostname, t.Port, remark)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", t.Remark, err))
				break
			}
			out = append(out, ls...)
		}
	}
	return out, errors.Join(errs...)
}

// ServerCleanLinks is CleanLinks with the clean IPs picked from the list.
func (o *Orchestrator) ServerCleanLinks(snap *Snapshot, s *models.Server) ([]linkgen.Link, error) {
	if !s.DNSProxied || s.Hostname == "" {
		return nil, nil
	}
	ips, err := cleanip.Pick(o.DB, o.Settings.Int(settings.CleanIPPerLink))
	if err != nil {
		return nil, err
	}
	return CleanLinks(snap, s, ips)
}
