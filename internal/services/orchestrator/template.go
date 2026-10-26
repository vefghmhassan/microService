package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"gorm.io/gorm"

	"microserver/internal/models"
	"microserver/internal/services/linkgen"
	"microserver/internal/services/xui"
)

// ManagedPrefix marks inbounds this panel owns on a 3x-ui server. Inbounds
// without it are left alone, so an operator can still add one by hand.
const ManagedPrefix = "[ms] "

type Snapshot struct {
	Xray     models.XrayTemplate
	Inbounds []models.InboundTemplate
	Hash     string
}

// LoadSnapshot reads the shared template and its content hash. Servers whose
// AppliedHash differs have drifted.
func LoadSnapshot(db *gorm.DB) (*Snapshot, error) {
	var snap Snapshot
	if err := db.FirstOrCreate(&snap.Xray, models.XrayTemplate{ID: 1}).Error; err != nil {
		return nil, err
	}
	if err := db.Order("port").Find(&snap.Inbounds).Error; err != nil {
		return nil, err
	}
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s\x00", canonical(snap.Xray.Outbounds), canonical(snap.Xray.Routing), canonical(snap.Xray.DNS), canonical(snap.Xray.Observatory))
	ins := append([]models.InboundTemplate{}, snap.Inbounds...)
	sort.Slice(ins, func(i, j int) bool { return ins[i].Port < ins[j].Port })
	for _, in := range ins {
		fmt.Fprintf(h, "%d|%s|%s|%t|%s|%s|%s\x00", in.Port, in.Remark, in.Protocol, in.Enabled,
			canonical(in.Settings), canonical(in.StreamSettings), canonical(in.Sniffing))
	}
	snap.Hash = hex.EncodeToString(h.Sum(nil))[:16]
	return &snap, nil
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
	db.Model(&models.Server{}).
		Where("applied_hash <> '' AND applied_hash <> ? AND status NOT IN ?", snap.Hash, []string{models.ServerDeleted, models.ServerDeleting}).
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

	if snap.Xray.Outbounds != "" || snap.Xray.Routing != "" || snap.Xray.DNS != "" || snap.Xray.Observatory != "" {
		setting, err := p.XraySetting(ctx)
		if err != nil {
			return fmt.Errorf("read xray template: %w", err)
		}
		for key, val := range map[string]string{"outbounds": snap.Xray.Outbounds, "routing": snap.Xray.Routing, "dns": snap.Xray.DNS, "observatory": snap.Xray.Observatory} {
			if strings.TrimSpace(val) == "" {
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
		logf("applied outbounds/routing/dns (template v%d)", snap.Xray.Version)
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

// Links builds the share links of every enabled inbound marked for upload,
// pointing at host.
func Links(snap *Snapshot, host string) ([]linkgen.Link, error) {
	var out []linkgen.Link
	var errs []error
	for _, t := range snap.Inbounds {
		if !t.Enabled || !t.Upload {
			continue
		}
		ls, err := linkgen.Links(t.Protocol, t.Settings, t.StreamSettings, host, t.Port, t.Remark)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", t.Remark, err))
			continue
		}
		out = append(out, ls...)
	}
	return out, errors.Join(errs...)
}
