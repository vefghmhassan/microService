package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm/clause"

	"microserver/internal/models"
	"microserver/internal/services/settings"
)

const gb = int64(1) << 30

// TrafficLimit is the cap that applies to a server, in bytes (0 = none).
func (o *Orchestrator) TrafficLimit(s *models.Server) int64 {
	g := s.TrafficLimitGB
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
				o.DB.Model(s).Update("stats_error", truncate(err.Error(), 255))
				return
			}
			o.enforceTrafficCap(s)
		}(&servers[i])
	}
	wg.Wait()
}

// CollectStats reads one panel's inbounds (cumulative up/down per inbound and
// client) and who is online, and stores the per-inbound and server totals.
func (o *Orchestrator) CollectStats(ctx context.Context, s *models.Server) error {
	remote, _, err := o.Connect(ctx, s)
	if err != nil {
		return fmt.Errorf("ssh: %w", err)
	}
	defer remote.Close()
	panel := o.PanelFor(s, remote)
	inbounds, err := panel.ListInbounds(ctx)
	if err != nil {
		return err
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

// QueueReplace builds a replacement for s (same location first, then the
// priority list), moves its vpn-pannel configs over and deletes s.
func (o *Orchestrator) QueueReplace(s *models.Server, reason, source string) (*models.Job, error) {
	if s.ReplacingAt != nil {
		return nil, fmt.Errorf("a replacement for %s is already queued", s.Name)
	}
	up := o.Settings.Bool(settings.VPNPanelAuto)
	j, err := o.Engine.Enqueue(KindProvisionAuto, nil, AutoParams{
		Count: 1, Upload: &up, ReplaceServerID: s.ID, PreferServerLocation: true, Reason: reason,
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
