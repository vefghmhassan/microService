package orchestrator

import (
	"context"
	"fmt"
	"math"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"microserver/internal/models"
	"microserver/internal/services/settings"
)

// Connection quality is read from the kernel's view of the users' own TCP
// connections (ss -ti) during the stats round: no test traffic is sent. Only
// connections that moved enough data say anything about speed.
const qualityMinAcked = 256 << 10

// ssConn is one established TCP connection as ss -tin prints it.
type ssConn struct {
	LocalPort  int
	Peer       string
	RTTMs      float64
	Kbps       float64 // delivery_rate; 0 when ss did not print one
	BytesAcked int64
	Retrans    int64 // total retransmitted segments
	SegsOut    int64
}

// qualitySum is the summary of a set of connections.
type qualitySum struct {
	Conns      int
	RTTMs      float64 // median
	Kbps       float64 // 75th percentile of delivery rate
	RetransPct float64 // retransmitted / sent segments
}

// qualityCmd lists the established TCP connections on the inbound ports.
func qualityCmd(ports []int) string {
	var f []string
	for _, p := range ports {
		f = append(f, fmt.Sprintf("sport = :%d", p))
	}
	return fmt.Sprintf("ss -tinH state established '( %s )' 2>&1", strings.Join(f, " or "))
}

// parseSS reads ss -tinH output: an address line per connection (Recv-Q,
// Send-Q, local, peer; a State column first when no state filter was used)
// followed by an indented line of TCP info.
func parseSS(out string) []ssConn {
	var conns []ssConn
	var cur *ssConn
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		if line[0] != ' ' && line[0] != '\t' {
			if c, rest, ok := parseSSAddrLine(fields); ok {
				conns = append(conns, c)
				cur = &conns[len(conns)-1]
				fields = rest
			} else {
				cur = nil
				continue
			}
		}
		if cur != nil {
			parseSSInfo(cur, fields)
		}
	}
	return conns
}

// parseSSAddrLine finds the local and peer address of a connection line and
// returns the fields after them (some ss versions print the info there).
func parseSSAddrLine(fields []string) (ssConn, []string, bool) {
	for i := 0; i+3 < len(fields); i++ {
		if _, err := strconv.Atoi(fields[i]); err != nil {
			continue
		}
		if _, err := strconv.Atoi(fields[i+1]); err != nil {
			continue
		}
		_, lport, ok1 := splitSSAddr(fields[i+2])
		peer, _, ok2 := splitSSAddr(fields[i+3])
		if ok1 && ok2 {
			return ssConn{LocalPort: lport, Peer: peer}, fields[i+4:], true
		}
	}
	return ssConn{}, nil, false
}

// splitSSAddr splits "1.2.3.4:443", "[::ffff:1.2.3.4]:443", "[2a01::1]:443"
// or "1.2.3.4%eth0:443" into a plain IP and a port.
func splitSSAddr(s string) (string, int, bool) {
	i := strings.LastIndex(s, ":")
	if i <= 0 {
		return "", 0, false
	}
	port, err := strconv.Atoi(s[i+1:])
	if err != nil {
		return "", 0, false
	}
	host := strings.Trim(s[:i], "[]")
	if j := strings.Index(host, "%"); j >= 0 {
		host = host[:j]
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return "", 0, false
	}
	if v4 := ip.To4(); v4 != nil {
		return v4.String(), port, true
	}
	return ip.String(), port, true
}

func parseSSInfo(c *ssConn, fields []string) {
	for i, f := range fields {
		k, v, hasColon := strings.Cut(f, ":")
		switch {
		case hasColon && k == "rtt":
			a, _, _ := strings.Cut(v, "/")
			c.RTTMs, _ = strconv.ParseFloat(a, 64)
		case hasColon && k == "bytes_acked":
			c.BytesAcked, _ = strconv.ParseInt(v, 10, 64)
		case hasColon && k == "segs_out":
			c.SegsOut, _ = strconv.ParseInt(v, 10, 64)
		case hasColon && k == "retrans":
			_, total, _ := strings.Cut(v, "/")
			c.Retrans, _ = strconv.ParseInt(total, 10, 64)
		case f == "delivery_rate" && i+1 < len(fields):
			c.Kbps = parseRate(fields[i+1])
		}
	}
}

// parseRate turns "12.5Mbps", "800Kbps", "950bps" into kilobits per second.
func parseRate(s string) float64 {
	units := []struct {
		suffix string
		mult   float64
	}{{"Gbps", 1e6}, {"Mbps", 1e3}, {"Kbps", 1}, {"bps", 1e-3}}
	for _, u := range units {
		if n, ok := strings.CutSuffix(s, u.suffix); ok {
			v, err := strconv.ParseFloat(n, 64)
			if err != nil {
				return 0
			}
			return v * u.mult
		}
	}
	return 0
}

// summarize takes the connections that moved enough data.
func summarize(conns []ssConn) qualitySum {
	var rtts, rates []float64
	var retrans, segs int64
	for _, c := range conns {
		if c.BytesAcked < qualityMinAcked {
			continue
		}
		if c.RTTMs > 0 {
			rtts = append(rtts, c.RTTMs)
		}
		if c.Kbps > 0 {
			rates = append(rates, c.Kbps)
		}
		retrans += c.Retrans
		segs += c.SegsOut
	}
	sum := qualitySum{Conns: len(rates), RTTMs: percentile(rtts, 0.5), Kbps: percentile(rates, 0.75)}
	if segs > 0 {
		sum.RetransPct = round1(float64(retrans) * 100 / float64(segs))
	}
	return sum
}

func percentile(v []float64, p float64) float64 {
	if len(v) == 0 {
		return 0
	}
	sort.Float64s(v)
	i := int(math.Ceil(p*float64(len(v)))) - 1
	return round1(v[max(0, min(i, len(v)-1))])
}

func round1(f float64) float64 { return math.Round(f*10) / 10 }

// userPeer says whether a peer is one of the users: public, and from Iran
// when the GeoIP database knows the address. That leaves out Cloudflare's
// edges on CDN inbounds and the master in front of a node.
func (o *Orchestrator) userPeer(peer string) bool {
	ip := net.ParseIP(peer)
	if ip == nil || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
		return false
	}
	if o.Geo != nil {
		if g, ok := o.Geo.Lookup(peer); ok && g.Country != "" && g.Country != "IR" {
			return false
		}
	}
	return true
}

// liveConns is what one read of the users' connections gives: who is
// connected (distinct source IPs) and how well their connections move data.
type liveConns struct {
	// IPs is the number of distinct user IPs on any inbound port; one IP on
	// two inbounds counts once.
	IPs     int
	PortIPs map[int]int
	All     qualitySum
	PerPort map[int]qualitySum
}

// distinctPeers counts the distinct peers overall and per local port.
func distinctPeers(conns []ssConn) (int, map[int]int) {
	all := map[string]bool{}
	byPort := map[int]map[string]bool{}
	for _, c := range conns {
		all[c.Peer] = true
		if byPort[c.LocalPort] == nil {
			byPort[c.LocalPort] = map[string]bool{}
		}
		byPort[c.LocalPort][c.Peer] = true
	}
	per := map[int]int{}
	for p, set := range byPort {
		per[p] = len(set)
	}
	return len(all), per
}

// readConns reads the users' connections on the inbound ports: the distinct
// IPs (the online count) and the quality summaries. ok is false when ss
// could not be read; note then says why.
func (o *Orchestrator) readConns(ctx context.Context, remote Remote, ports []int) (lc liveConns, note string, ok bool) {
	if len(ports) == 0 {
		return lc, "", false
	}
	out, err := remote.Run(ctx, qualityCmd(ports), nil)
	if err != nil {
		return lc, truncate(fmt.Sprintf("ss: %v %s", err, strings.TrimSpace(out)), 255), false
	}
	var users []ssConn
	byPort := map[int][]ssConn{}
	for _, c := range parseSS(out) {
		if !o.userPeer(c.Peer) {
			continue
		}
		users = append(users, c)
		byPort[c.LocalPort] = append(byPort[c.LocalPort], c)
	}
	lc.IPs, lc.PortIPs = distinctPeers(users)
	lc.PerPort = map[int]qualitySum{}
	for p, cs := range byPort {
		lc.PerPort[p] = summarize(cs)
	}
	lc.All = summarize(users)
	return lc, "", true
}

// QualityBad says whether a summary is bad under the settings; enough is
// false when too few connections moved data to judge.
func (o *Orchestrator) qualityBad(q qualitySum) (bad, enough bool) {
	if q.Conns < max(1, o.Settings.Int(settings.QualityMinConns)) {
		return false, false
	}
	if minK := o.Settings.Int(settings.QualityMinKbps); minK > 0 && q.Kbps < float64(minK) {
		return true, true
	}
	if maxR := o.Settings.Int(settings.QualityMaxRetrans); maxR > 0 && q.RetransPct > float64(maxR) {
		return true, true
	}
	return false, true
}

// applyQuality stores a server's summary, counts bad rounds in a row and
// warns once when they reach the setting. A round with too little data
// leaves the count as it was.
func (o *Orchestrator) applyQuality(s *models.Server, q qualitySum, note string) {
	now := time.Now()
	s.QualityAt, s.QualityNote = &now, note
	s.QualityConns, s.QualityRTTMs, s.QualityKbps, s.QualityRetransPct = q.Conns, q.RTTMs, q.Kbps, q.RetransPct
	bad, enough := o.qualityBad(q)
	switch {
	case bad:
		s.QualityBad++
	case enough:
		s.QualityBad = 0
	}
	o.DB.Model(s).Select("quality_at", "quality_note", "quality_conns", "quality_rtt_ms", "quality_kbps", "quality_retrans_pct", "quality_bad").Updates(s)

	rounds := max(1, o.Settings.Int(settings.QualityBadRounds))
	if !bad || s.QualityBad != rounds || !o.Settings.Bool(settings.QualityAlert) || s.Status != models.ServerActive || s.ReplacingAt != nil {
		return
	}
	host := s.PublicIP()
	if s.Hostname != "" {
		host += " · " + s.Hostname
	}
	text := fmt.Sprintf("🐢 کیفیت اتصال پایین در سرور %s\nIP: %s\nسرعت (p75): %s · پینگ کاربران: %.0f ms · retransmit: %.1f٪\nاتصال‌های بررسی‌شده: %d · %d بار پشت سر هم\nممکن است IP محدود (throttle) شده باشد.",
		s.Name, host, humanKbps(q.Kbps), q.RTTMs, q.RetransPct, q.Conns, rounds)
	if o.Alert != nil {
		o.Alert(s, text)
		return
	}
	o.notify("%s", text)
}

func humanKbps(k float64) string {
	if k >= 1000 {
		return fmt.Sprintf("%.1f Mbps", k/1000)
	}
	return fmt.Sprintf("%.0f Kbps", k)
}
