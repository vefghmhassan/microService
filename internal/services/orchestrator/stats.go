package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm/clause"

	"microserver/internal/jobs"
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
	o.DB.Where("status IN ? AND xui_port > 0 AND role <> ?", []string{models.ServerActive, models.ServerFiltered}, models.RoleMaster).Find(&servers)
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
	o.QueueIdleSync()
	PruneClientIPs(o.DB)
	if j, c := PruneHistory(o.DB, o.Settings.Int(settings.HistoryKeepDays)); j+c > 0 {
		log.Printf("history: removed %d old jobs and %d old checks", j, c)
	}
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

	var ports []int
	for _, in := range inbounds {
		if in.Enable && in.Port > 0 {
			ports = append(ports, in.Port)
		}
	}
	// Online is the users' distinct IPs from ss: a 3x-ui client is shared
	// by many people, so online emails stop at the number of clients. When
	// ss cannot be read, the email count stands in.
	live, qNote, connsOK := o.readConns(ctx, remote, ports)
	qOK := connsOK && o.Settings.Bool(settings.QualityEnabled)
	if !o.Settings.Bool(settings.QualityEnabled) {
		qNote = ""
	}
	prevIdle := map[int]*time.Time{}
	var prev []models.InboundStat
	o.DB.Select("port", "idle_since").Where("server_id = ?", s.ID).Find(&prev)
	for _, p := range prev {
		prevIdle[p.Port] = p.IdleSince
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
		if connsOK {
			n = live.PortIPs[in.Port]
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
		cols := []string{"remark", "protocol", "enabled", "up", "down", "clients", "online", "updated_at"}
		if qOK {
			q := live.PerPort[in.Port]
			row.QConns, row.QRTTMs, row.QKbps, row.QRetransPct = q.Conns, q.RTTMs, q.Kbps, q.RetransPct
			cols = append(cols, "q_conns", "q_rtt_ms", "q_kbps", "q_retrans_pct")
		}
		if connsOK {
			// Only a good read moves the idle clock: an ssh or ss error is
			// not "nobody connected".
			row.IdleSince = nextIdleSince(prevIdle[in.Port], in.Enable && n == 0, now)
			cols = append(cols, "idle_since")
		}
		o.DB.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "server_id"}, {Name: "port"}},
			DoUpdates: clause.AssignmentColumns(cols),
		}).Create(&row)
	}
	if connsOK {
		onlineTotal = live.IPs
	}
	// Inbounds removed from the panel no longer count.
	q := o.DB.Where("server_id = ?", s.ID)
	if len(seen) > 0 {
		q = q.Where("port NOT IN ?", seen)
	}
	q.Delete(&models.InboundStat{})

	s.TrafficUp, s.TrafficDown, s.OnlineCount, s.StatsAt, s.StatsError = up, down, onlineTotal, &now, ""
	o.DB.Model(s).Select("traffic_up", "traffic_down", "online_count", "stats_at", "stats_error").Updates(s)
	switch {
	case qOK:
		o.applyQuality(s, live.All, "")
	case qNote != "":
		s.QualityNote = qNote
		o.DB.Model(s).Update("quality_note", qNote)
	}
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
	if limit <= 0 || s.ReplacingAt != nil || s.DeleteAt != nil || s.TrafficUp+s.TrafficDown < limit {
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
// onlineBase is the online count before the change (0 = no history).
func (o *Orchestrator) startWatch(s *models.Server, onlineBase int) {
	now := time.Now()
	s.WatchFrom, s.WatchBase, s.WatchOnlineBase = &now, s.TrafficUp+s.TrafficDown, onlineBase
	o.DB.Model(s).Select("watch_from", "watch_base", "watch_online_base").Updates(s)
}

// lowOnline says whether too few users came back after the change, and why.
func (o *Orchestrator) lowOnline(s *models.Server) (bool, string) {
	online, base := s.OnlineCount, s.WatchOnlineBase
	if pct := o.Settings.Int(settings.WatchOnlinePct); base > 0 && pct > 0 {
		if online*100 < base*pct {
			return true, fmt.Sprintf("👥 آنلاین: %d (قبل از تعویض %d) · کمتر از %d٪", online, base, pct)
		}
		return false, ""
	}
	if min := o.Settings.Int(settings.WatchMinOnline); base == 0 && min > 0 && online < min {
		return true, fmt.Sprintf("👥 آنلاین: %d · کمتر از حداقل %d", online, min)
	}
	return false, ""
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
	lowTraffic := used < threshold
	lowOn, onlineNote := o.lowOnline(s)
	if !lowTraffic && !lowOn {
		return
	}
	host := s.PublicIP()
	if s.Hostname != "" {
		host += " · " + s.Hostname
	}
	mins := int(time.Since(from).Round(time.Minute).Minutes())
	var lines []string
	if lowTraffic {
		lines = append(lines, fmt.Sprintf("در %d دقیقه پس از راه‌اندازی فقط %s مصرف شد (کمتر از %s)", mins, humanBytes(used), humanBytes(threshold)))
	} else {
		lines = append(lines, fmt.Sprintf("مصرف %d دقیقه: %s", mins, humanBytes(used)))
	}
	if lowOn {
		lines = append(lines, onlineNote)
	} else {
		lines = append(lines, fmt.Sprintf("👥 آنلاین: %d", s.OnlineCount))
	}
	text := fmt.Sprintf("⚠️ احتمال مشکل IP در سرور %s\nIP: %s\n%s\nممکن است IP فیلتر شده باشد.",
		s.Name, host, strings.Join(lines, "\n"))
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
	// The replacement must not land on the address being replaced.
	o.banServerIPs(nil, s, "replaced: "+reason)
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

// onlineBefore is the online count a job saved before its change (0 = none).
func onlineBefore(r *jobs.Run) int {
	n, _ := strconv.Atoi(r.GetString("online_before"))
	return n
}
