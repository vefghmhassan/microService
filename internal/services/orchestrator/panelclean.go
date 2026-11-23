package orchestrator

import (
	"context"
	"strings"
	"time"

	"gorm.io/gorm/clause"

	"microserver/internal/jobs"
	"microserver/internal/models"
)

// panelAddrs lists every address a server's configs may sit on in a
// destination panel: the recorded upload address, the current link host, both
// IPs, its grey name and its orange (CDN) name. vpn-pannel keys a config by
// the host of its link, which has been each of these at some point.
func panelAddrs(s *models.Server) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, a := range []string{s.UploadedIP, LinkHost(s), s.PublicIP(), s.IPv4, s.IPv6, s.Hostname, s.CDNHostname} {
		a = strings.TrimSpace(a)
		if a != "" && !seen[a] {
			seen[a] = true
			out = append(out, a)
		}
	}
	return out
}

// addressInUse reports whether a server other than except, not deleted,
// still serves addr: a replacement that took over the name, or the IP moved
// to another server. Its configs must then stay.
func (o *Orchestrator) addressInUse(addr string, except uint) bool {
	var n int64
	o.DB.Model(&models.Server{}).
		Where("id <> ? AND status <> ?", except, models.ServerDeleted).
		Where("uploaded_ip = ? OR ipv4 = ? OR ipv6 = ? OR hostname = ? OR cdn_hostname = ?", addr, addr, addr, addr, addr).
		Count(&n)
	return n > 0
}

// deleteOnPanels deletes every config on each address from every
// destination. An address another live server still uses is skipped. A
// deletion that fails is stored and retried by RunPanelCleanups, so a panel
// that was down at the time does not keep the configs of a deleted server.
func (o *Orchestrator) deleteOnPanels(ctx context.Context, r *jobs.Run, serverID uint, addrs []string) {
	dests := o.Dests()
	for _, addr := range addrs {
		if addr == "" {
			continue
		}
		if o.addressInUse(addr, serverID) {
			r.Info("%s is used by another server; its configs stay", addr)
			continue
		}
		for _, d := range dests {
			if err := d.DeleteAddress(ctx, addr); err != nil {
				r.Logf("warn", "%s: could not delete configs on %s: %v (retried later)", d.Name, addr, err)
				o.queuePanelCleanup(d.ID, addr, err)
				continue
			}
			r.Info("%s: configs on %s deleted", d.Name, addr)
		}
	}
}

func (o *Orchestrator) queuePanelCleanup(destID uint, addr string, err error) {
	row := models.PanelCleanup{DestID: destID, Address: addr, LastError: truncate(err.Error(), 500)}
	o.DB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "dest_id"}, {Name: "address"}},
		DoUpdates: clause.AssignmentColumns([]string{"last_error", "updated_at"}),
	}).Create(&row)
}

// panelCleanupEvery is the pause between two tries of one cleanup.
var panelCleanupEvery = 2 * time.Minute

// RunPanelCleanups retries the destination deletions that failed. A row is
// dropped once the deletion went through, when its destination was removed or
// disabled, or when a live server uses the address again.
func (o *Orchestrator) RunPanelCleanups(ctx context.Context) {
	var rows []models.PanelCleanup
	o.DB.Where("updated_at <= ?", time.Now().Add(-panelCleanupEvery)).Order("id").Limit(50).Find(&rows)
	if len(rows) == 0 {
		return
	}
	dests := map[uint]NamedDest{}
	for _, d := range o.Dests() {
		dests[d.ID] = d
	}
	for _, row := range rows {
		d, ok := dests[row.DestID]
		if !ok || o.addressInUse(row.Address, 0) {
			o.DB.Delete(&row)
			continue
		}
		dctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		err := d.DeleteAddress(dctx, row.Address)
		cancel()
		if err == nil {
			o.DB.Delete(&row)
			continue
		}
		o.DB.Model(&row).Updates(map[string]any{
			"attempts": row.Attempts + 1, "last_error": truncate(err.Error(), 500), "updated_at": time.Now(),
		})
		if row.Attempts+1 == 10 {
			o.notify("⚠️ حذف کانفیگ‌های %s از پنل مقصد %s بعد از ۱۰ تلاش هنوز انجام نشده: %s", row.Address, d.Name, truncate(err.Error(), 200))
		}
	}
}
