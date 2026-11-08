package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"

	"microserver/internal/jobs"
	"microserver/internal/models"
	"microserver/internal/services/cleanip"
	"microserver/internal/services/destination"
)

// CDN add-on: a location that uses one gives every server it builds two
// Cloudflare names for the same IP (DirectPattern unproxied, CDNPattern
// proxied), the zone certificate before 3x-ui is installed, and the add-on's
// own inbounds on top of the shared template.

// ApplyAddon copies an add-on onto a server being built, in place of the
// location's own DNS settings.
func ApplyAddon(s *models.Server, a *models.CDNAddon) {
	s.CDNAddonID = a.ID
	s.DNS = models.DNS{
		CFAccountID: a.CFAccountID, CFZoneID: a.CFZoneID, CFZoneName: a.CFZoneName,
		DNSPattern: a.DirectPattern, DNSProxied: false, SSL: true,
	}
	s.CDNPattern = a.CDNPattern
}

// locationAddon is the enabled add-on of a location, if any.
func (o *Orchestrator) locationAddon(loc *models.Location) *models.CDNAddon {
	if loc.CDNAddonID == 0 {
		return nil
	}
	var a models.CDNAddon
	if o.DB.First(&a, loc.CDNAddonID).Error != nil || !a.On() {
		return nil
	}
	return &a
}

// addonClean picks the clean Cloudflare addresses a server's add-on
// inbounds get extra configs for.
func (o *Orchestrator) addonClean(s *models.Server) []models.CleanIP {
	if s.CDNAddonID == 0 || s.CDNHostname == "" {
		return nil
	}
	var a models.CDNAddon
	if o.DB.First(&a, s.CDNAddonID).Error != nil || a.CleanIPCount <= 0 {
		return nil
	}
	ips, err := cleanip.Pick(o.DB, a.CleanIPCount)
	if err != nil {
		return nil
	}
	return ips
}

// forUpload is snap narrowed to s with the add-on's clean addresses filled.
func (o *Orchestrator) forUpload(snap *Snapshot, s *models.Server) *Snapshot {
	ns := snap.For(s)
	ns.Clean = o.addonClean(s)
	return ns
}

// ensureAddonXray switches the server to Xray 26+ when its add-on has a
// Hysteria2 inbound (FinalMask and hysteria need it). Without it the
// Hysteria2 inbound is simply left off this server.
func (o *Orchestrator) ensureAddonXray(ctx context.Context, r *jobs.Run, s *models.Server, remote Remote) error {
	if s.CDNAddonID == 0 || XrayAtLeast(s.XrayVersion, 26) {
		return nil
	}
	var n int64
	o.DB.Model(&models.InboundTemplate{}).Where("addon_id = ? AND protocol = ? AND enabled = ?", s.CDNAddonID, "hysteria", true).Count(&n)
	if n == 0 {
		return nil
	}
	panel := o.PanelFor(s, remote)
	vers, err := panel.XrayVersions(ctx)
	if err != nil {
		return fmt.Errorf("hysteria2 needs xray 26+; could not list versions: %w", err)
	}
	want := ""
	for _, v := range vers {
		if XrayAtLeast(v, 26) {
			want = v
			break
		}
	}
	if want == "" {
		return fmt.Errorf("hysteria2 needs xray 26+, which this panel does not offer; hysteria2 is skipped")
	}
	r.Info("switching xray core to %s for hysteria2", want)
	if err := panel.InstallXray(ctx, want); err != nil {
		return fmt.Errorf("xray %s: %w (hysteria2 is skipped)", want, err)
	}
	s.XrayVersion = strings.TrimPrefix(want, "v")
	o.save(r, s, "xray_version")
	return nil
}

// retireCleanConfigs deactivates (or deletes) the clean-IP configs of a
// retired add-on server in vpn-pannel. Their addresses are clean IPs every
// server shares, so they are matched by the orange name in SNI/Host; a
// vpn-pannel without that route is left alone rather than matched by address.
func (o *Orchestrator) retireCleanConfigs(ctx context.Context, r *jobs.Run, host string, purge bool) {
	for _, d := range o.Dests() {
		hd, ok := d.Destination.(HostDestination)
		if !ok {
			continue
		}
		var err error
		if purge {
			err = hd.DeleteHost(ctx, host)
		} else {
			err = hd.SetActiveHost(ctx, host, false)
		}
		switch {
		case errors.Is(err, destination.ErrNoHostRoute):
			r.Logf("warn", "%s: clean-IP configs for %s stay as they are: %v", d.Name, host, err)
		case err != nil:
			r.Logf("warn", "%s: clean-IP configs for %s: %v", d.Name, host, err)
		default:
			r.Info("%s: clean-IP configs for %s %s", d.Name, host, map[bool]string{true: "deleted", false: "deactivated"}[purge])
		}
	}
}

// BumpAddon stores an add-on's new inbound hash and marks its servers that
// no longer match as drifted.
func BumpAddon(db *gorm.DB, id uint) (string, error) {
	var ins []models.InboundTemplate
	if err := db.Where("addon_id = ?", id).Find(&ins).Error; err != nil {
		return "", err
	}
	h := AddonHash(ins)
	if err := db.Model(&models.CDNAddon{}).Where("id = ?", id).Update("hash", h).Error; err != nil {
		return "", err
	}
	db.Model(&models.Server{}).
		Where("cdn_addon_id = ? AND applied_hash <> '' AND addon_hash <> ? AND status NOT IN ?", id, h,
			[]string{models.ServerDeleted, models.ServerDeleting}).
		Update("sync_status", models.SyncDrift)
	return h, nil
}
