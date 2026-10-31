package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm/clause"

	"microserver/internal/models"
	"microserver/internal/services/providers"
	"microserver/internal/services/settings"
)

const gb = int64(1) << 30

// TrafficLimit is the cap that applies to a server, in bytes (0 = none): its
// own, else its location's, else the global setting.
func (o *Orchestrator) TrafficLimit(s *models.Server) int64 {
	g := s.TrafficLimitGB
	if g <= 0 && s.LocationID != 0 {
		var loc models.Location
		if o.DB.Select("traffic_limit_gb").First(&loc, s.LocationID).Error == nil {
			g = loc.TrafficLimitGB
		}
	}
	if g <= 0 {
		g = o.Settings.Int(settings.TrafficLimitGB)
	}
	if g <= 0 {
		return 0
	}
	return int64(g) * gb
}

// CollectAll reads traffic and online users from every live panel, a few at a
// time, and fires the traffic cap where it is reached.
func (o *Orchestrator) CollectAll(ctx context.Context) {
	var servers []models.Server
	o.DB.Where("status IN ? AND xui_port > 0", []string{models.ServerActive, models.ServerFiltered}).Find(&servers)
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for i := range servers {
		wg.Add(1)
		sem <- struct{}{}
		go func(s *models.Server) {
			defer wg.Done()
			defer func() { <-sem }()
			cctx, cancel := context.WithTimeout(ctx, 90*time.Second)
			defer cancel()
			if err := o.CollectStats(cctx, s); err != nil {
				return
			}
			o.enforceTrafficCap(s)
			o.checkLowUsage(s)
		}(&servers[i])
	}
	wg.Wait()
	PruneClientIPs(o.DB)
}

// CollectStats reads one panel's inbounds (cumulative up/down per inbound and
// client) and who is online, and stores the per-inbound and server totals.
func (o *Orchestrator) CollectStats(ctx context.Context, s *models.Server) (err error) {
	defer func() {
		if err != nil {
			o.DB.Model(s).Update("stats_error", truncate(err.Error(), 255))
		}
	}()
	remote, _, err := o.Connect(ctx, s)
	if err != nil {
		err = fmt.Errorf("ssh: %w", err)
		// The GeoIP report shows this note, so say why nothing was collected.
		o.recordIPCollect(s, "", err)
		return o.checkGone(ctx, s, err)
	}
	defer remote.Close()
	panel := o.PanelFor(s, remote)
	inbounds, err := panel.ListInbounds(ctx)
	if err != nil {
		o.recordIPCollect(s, "", fmt.Errorf("panel: %w", err))
		return err
	}
	tagRemark := map[string]string{}
	for _, in := range inbounds {
		if in.Tag != "" {
			tagRemark[in.Tag] = strings.TrimPrefix(in.Remark, ManagedPrefix)
		}
	}
	if err := o.ensureAccessLog(ctx, s, panel); err != nil {
		o.recordIPCollect(s, "", fmt.Errorf("enable access log: %w", err))
	} else {
		note, err := o.collectIPs(ctx, remote, panel, s, tagRemark)
		o.recordIPCollect(s, note, err)
	}
	online := map[string]bool{}
	if emails, err := panel.Onlines(ctx); err == nil {
		for _, e := range emails {
			online[e] = true
		}
	}

	var up, down int64
	onlineTotal := 0
	now := time.Now()
	seen := []int{}
	for _, in := range inbounds {
		n := 0
		for _, cs := range in.ClientStats {
			if online[cs.Email] {
				n++
			}
		}
		up += in.Up
		down += in.Down
		onlineTotal += n
		seen = append(seen, in.Port)
		row := models.InboundStat{
			ServerID: s.ID, Port: in.Port, Remark: strings.TrimPrefix(in.Remark, ManagedPrefix),
			Protocol: in.Protocol, Enabled: in.Enable, Up: in.Up, Down: in.Down,
			Clients: len(in.ClientStats), Online: n, UpdatedAt: now,
		}
		o.DB.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "server_id"}, {Name: "port"}},
			DoUpdates: clause.AssignmentColumns([]string{"remark", "protocol", "enabled", "up", "down", "clients", "online", "updated_at"}),
		}).Create(&row)
	}
	// Inbounds removed from the panel no longer count.
	q := o.DB.Where("server_id = ?", s.ID)
	if len(seen) > 0 {
		q = q.Where("port NOT IN ?", seen)
	}
	q.Delete(&models.InboundStat{})

	s.TrafficUp, s.TrafficDown, s.OnlineCount, s.StatsAt, s.StatsError = up, down, onlineTotal, &now, ""
	o.DB.Model(s).Select("traffic_up", "traffic_down", "online_count", "stats_at", "stats_error").Updates(s)
	o.Engine.Broker.Publish(serverEvent(s))
	return nil
}

// checkGone asks the provider about a server that does not answer. A server
// deleted outside the panel (e.g. in the provider's console) is marked as an
// error so it stops looking active, and the admin is told; otherwise err is
// returned as is.
func (o *Orchestrator) checkGone(ctx context.Context, s *models.Server, err error) error {
	if s.ProviderRef == "" || o.Provider == nil {
		return err
	}
	var acc models.ProviderAccount
	if o.DB.First(&acc, s.ProviderAccountID).Error != nil {
		return err
	}
	p, perr := o.Provider(&acc)
	if perr != nil {
		return err
	}
	gctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if _, gerr := p.Get(gctx, s.ProviderRef); !errors.Is(gerr, providers.ErrNotFound) {
		return err
	}
	gone := fmt.Errorf("server %s no longer exists at %s (deleted outside the panel)", s.ProviderRef, acc.Name)
	s.Status, s.StatsError = models.ServerError, gone.Error()
	o.DB.Model(s).Select("status", "stats_error").Updates(s)
	o.Engine.Broker.Publish(serverEvent(s))
	o.notify("⚠️ سرور %s (%s) دیگر در %s وجود ندارد؛ احتمالاً بیرون از پنل حذف شده است. وضعیتش «خطا» شد.", s.Name, s.PublicIP(), acc.Name)
	return gone
}

// enforceTrafficCap queues a replacement once a server's users have used
// its cap, if automatic replacement is on. It fires once per server.
func (o *Orchestrator) enforceTrafficCap(s *models.Server) {
	limit := o.TrafficLimit(s)
	if limit <= 0 || s.ReplacingAt != nil || s.TrafficUp+s.TrafficDown < limit {
		return
	}
	if !o.Settings.Bool(settings.TrafficAuto) {
		return
	}
	if _, err := o.QueueReplace(s, fmt.Sprintf("traffic cap: %s of %s used", humanBytes(s.TrafficUp+s.TrafficDown), humanBytes(limit)), "auto"); err != nil {
		return
	}
}

// watchWindow is how long a server is watched after going live, clamped to
// 10–60 minutes.
func (o *Orchestrator) watchWindow() time.Duration {
	m := o.Settings.Int(settings.WatchMinutes)
	m = max(10, min(60, m))
	return time.Duration(m) * time.Minute
}

// startWatch opens the low-usage window of a server that just went live on
// a new IP.
func (o *Orchestrator) startWatch(s *models.Server) {
	now := time.Now()
	s.WatchFrom, s.WatchBase = &now, s.TrafficUp+s.TrafficDown
	o.DB.Model(s).Select("watch_from", "watch_base").Updates(s)
}

// checkLowUsage closes a server's low-usage window once it has passed and,
// if its users moved less than the threshold over all inbounds, warns that
// the IP may be blocked, with buttons to change the IP, replace or delete.
func (o *Orchestrator) checkLowUsage(s *models.Server) {
	if s.WatchFrom == nil || time.Since(*s.WatchFrom) < o.watchWindow() {
		return
	}
	from := *s.WatchFrom
	s.WatchFrom = nil
	o.DB.Model(s).Update("watch_from", nil)
	if !o.Settings.Bool(settings.WatchLowUsage) || s.Status != models.ServerActive || s.ReplacingAt != nil {
		return
	}
	total := s.TrafficUp + s.TrafficDown
	used := total - s.WatchBase
	if used < 0 {
		// 3x-ui's counters were reset (e.g. an inbound was recreated).
		used = total
	}
	threshold := int64(o.Settings.Int(settings.WatchMinMB)) << 20
	if used >= threshold {
		return
	}
	host := s.PublicIP()
	if s.Hostname != "" {
		host += " · " + s.Hostname
	}
	text := fmt.Sprintf("⚠️ احتمال مشکل IP در سرور %s\nIP: %s\nدر %d دقیقه پس از راه‌اندازی فقط %s مصرف شد (کمتر از %s) · 👥 آنلاین: %d\nممکن است IP فیلتر شده باشد.",
		s.Name, host, int(time.Since(from).Round(time.Minute).Minutes()), humanBytes(used), humanBytes(threshold), s.OnlineCount)
	if o.Alert != nil {
		o.Alert(s, text)
		return
	}
	o.notify("%s", text)
}

// QueueReplace builds a replacement for s (same location first, then the
// priority list), moves its vpn-pannel configs over and deletes s.
func (o *Orchestrator) QueueReplace(s *models.Server, reason, source string) (*models.Job, error) {
	if s.ReplacingAt != nil {
		return nil, fmt.Errorf("a replacement for %s is already queued", s.Name)
	}
	up := o.Settings.Bool(settings.VPNPanelAuto)
	j, err := o.Engine.Enqueue(KindProvisionAuto, nil, AutoParams{
		Count: 1, Upload: &up, ReplaceServerID: s.ID, PreferServerLocation: true, Reason: reason,
		IPFamily: s.Family(),
	}, source)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	s.ReplacingAt = &now
	o.DB.Model(s).Update("replacing_at", now)
	o.notify("♻️ جایگزینی سرور %s شروع شد\nدلیل: %s", s.Name, reason)
	return j, nil
}

func humanBytes(n int64) string {
	switch {
	case n >= gb:
		return fmt.Sprintf("%.1f GB", float64(n)/float64(gb))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/float64(1<<20))
	}
	return fmt.Sprintf("%d B", n)
}
