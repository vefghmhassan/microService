package orchestrator

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"microserver/internal/models"
	"microserver/internal/services/settings"
)

// DefaultIdleMinutes is how long an inbound may have no user before a smart
// config or mode with DropIdle leaves it out, when it does not say.
const DefaultIdleMinutes = 30

// nextIdleSince moves an inbound's idle clock after a good read: it starts
// when the inbound is first seen without users, and stops when it has some.
func nextIdleSince(prev *time.Time, idle bool, now time.Time) *time.Time {
	if !idle {
		return nil
	}
	if prev != nil {
		return prev
	}
	t := now
	return &t
}

// idleRule is when a smart config or mode leaves an idle inbound out.
// After 0 = never.
type idleRule struct {
	After time.Duration
	// Retry puts a left-out inbound back for one After window this long
	// after it went out, so one that works again can win users back
	// (0 = it stays out until it has users).
	Retry time.Duration
}

func (o *Orchestrator) idleRuleFor(drop bool, minutes int) idleRule {
	if !drop {
		return idleRule{}
	}
	if minutes <= 0 {
		minutes = DefaultIdleMinutes
	}
	return idleRule{
		After: time.Duration(minutes) * time.Minute,
		Retry: time.Duration(o.Settings.Int(settings.IdleRetryHours)) * time.Hour,
	}
}

// out says whether an inbound idle since since is left out at now. It goes
// out After past since, then cycles: out for Retry, back in for After.
func (r idleRule) out(since *time.Time, now time.Time) bool {
	if r.After <= 0 || since == nil {
		return false
	}
	d := now.Sub(*since) - r.After
	if d < 0 {
		return false
	}
	if r.Retry <= 0 {
		return true
	}
	return d%(r.Retry+r.After) < r.Retry
}

// idleOut lists the ports of server s that rule r leaves out now. An inbound
// is left out only while the server has users on other inbounds (so it is
// the inbound that does not work, not a quiet or new server), and never all
// of them.
func (o *Orchestrator) idleOut(s *models.Server, r idleRule, ports []int, now time.Time) map[int]bool {
	if r.After <= 0 || len(ports) == 0 {
		return nil
	}
	var stats []models.InboundStat
	o.DB.Where("server_id = ? AND port IN ?", s.ID, ports).Find(&stats)
	byPort := map[int]models.InboundStat{}
	for _, st := range stats {
		byPort[st.Port] = st
	}
	busy := false
	out := map[int]bool{}
	for _, p := range ports {
		st, ok := byPort[p]
		if ok && st.Online > 0 {
			busy = true
		}
		if ok && r.out(st.IdleSince, now) {
			out[p] = true
		}
	}
	if !busy || len(out) == 0 {
		return nil
	}
	kept := 0
	for _, p := range ports {
		if !out[p] {
			kept++
		}
	}
	if kept == 0 {
		return nil
	}
	return out
}

// inboundScores are the measured speeds (Kbps, 75th percentile of users)
// of server s's inbounds by port, for the smart fallback. A server whose
// quality has been bad for quality.bad_rounds gets none.
func (o *Orchestrator) inboundScores(s *models.Server) map[int]float64 {
	if s.QualityBad >= max(1, o.Settings.Int(settings.QualityBadRounds)) {
		return nil
	}
	var stats []models.InboundStat
	o.DB.Select("port", "q_kbps").Where("server_id = ?", s.ID).Find(&stats)
	out := map[int]float64{}
	for _, st := range stats {
		out[st.Port] = st.QKbps
	}
	return out
}

// QueueIdleSync rebuilds the smart and balancer configs that drop idle
// inbounds when, after a stats round, the set they leave out changed.
// Called from CollectAll only, so idleSig needs no lock.
func (o *Orchestrator) QueueIdleSync() {
	var profiles []models.SmartProfile
	o.DB.Where("enabled = ? AND drop_idle = ?", true, true).Find(&profiles)
	var modes []models.BalancerMode
	o.DB.Where("enabled = ? AND drop_idle = ?", true, true).Find(&modes)
	if len(profiles)+len(modes) == 0 {
		o.idleSig = ""
		return
	}
	rules := map[idleRule]bool{}
	for _, p := range profiles {
		rules[o.idleRuleFor(true, p.IdleMinutes)] = true
	}
	for _, m := range modes {
		rules[o.idleRuleFor(true, m.IdleMinutes)] = true
	}
	sig := o.idleSignature(rules, time.Now())
	if sig == o.idleSig {
		return
	}
	o.idleSig = sig
	if len(profiles) > 0 {
		o.QueueSmartSync(false)
	}
	if len(modes) > 0 {
		o.QueueNodeSync()
	}
}

// idleSignature lists, for every rule, the ports each live server leaves out.
func (o *Orchestrator) idleSignature(rules map[idleRule]bool, now time.Time) string {
	var servers []models.Server
	o.DB.Where("status = ? AND role <> ?", models.ServerActive, models.RoleMaster).Order("id").Find(&servers)
	var b strings.Builder
	for r := range rules {
		fmt.Fprintf(&b, "%v/%v:", r.After, r.Retry)
		for i := range servers {
			s := &servers[i]
			var ports []int
			o.DB.Model(&models.InboundStat{}).Where("server_id = ? AND enabled = ?", s.ID, true).Order("port").Pluck("port", &ports)
			out := o.idleOut(s, r, ports, now)
			for _, p := range ports {
				if out[p] {
					fmt.Fprintf(&b, "%d.%d,", s.ID, p)
				}
			}
		}
		b.WriteString(";")
	}
	parts := strings.Split(b.String(), ";")
	sort.Strings(parts)
	return strings.Join(parts, ";")
}
