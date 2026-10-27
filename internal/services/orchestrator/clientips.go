package orchestrator

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"microserver/internal/models"
)

// AccessLogPath is where every managed xray writes its access log; client
// addresses for the GeoIP report come from it. 3x-ui (v3+) moves any log path
// into its log folder, /var/log/x-ui, when it builds xray's config, so a path
// anywhere else is never written; the script still follows whatever path the
// running xray was given.
const AccessLogPath = "/var/log/x-ui/access.log"

// ClientIPRetention is how long client addresses are kept.
const ClientIPRetention = 30 * 24 * time.Hour

// GeoLookup resolves a client address (geoip.DB satisfies it).
type GeoLookup interface {
	Lookup(ip string) (models.IPGeo, bool)
}

// accessLogScript prints the server's UTC offset, then takes the access log
// (truncating it so it never grows) and aggregates it on the server into one
// "ip email tag count first last" line per address, user and inbound, so only
// a summary crosses SSH. It sticks to POSIX awk (Ubuntu ships mawk).
const accessLogScript = `F=` + AccessLogPath + `
echo "TZ $(date +%z)"
# Read the access log path from the config the running xray was started with.
RUN=xray-not-running
P=$(pgrep -f 'xray-linux' 2>/dev/null | head -n1)
if [ -n "$P" ]; then
  D=$(readlink /proc/$P/cwd)
  C=$(tr '\0' ' ' < /proc/$P/cmdline | sed -n 's/.* -c \([^ ]*\).*/\1/p')
  case "$C" in /*) ;; *) C="$D/$C" ;; esac
  A=$(grep -o '"access": *"[^"]*"' "$C" 2>/dev/null | head -n1 | tr -d ' ')
  RUN="cwd=$D config=$C $A"
  A=${A#*:}; A=${A#\"}; A=${A%\"}
  case "$A" in /*) F="$A" ;; esac
fi
if [ -e "$F" ]; then echo "SIZE $(wc -c < "$F" | tr -d ' ')"; else
  echo "SIZE missing"
  echo "RUN $RUN"
fi
[ -s "$F" ] || exit 0
T=$(mktemp) || exit 0
cp "$F" "$T" && : > "$F"
awk '/ accepted / {
  a = ""; e = ""; t = ""
  for (i = 3; i <= NF; i++) {
    if ($i == "from" && a == "") a = $(i+1)
    else if ($i == "email:") e = $(i+1)
    else if (t == "" && substr($i, 1, 1) == "[" && ($(i+1) == ">>" || $(i+1) == "->")) t = substr($i, 2)
  }
  if (a == "") next
  sub(/^(tcp|udp):/, "", a); sub(/:[0-9]+$/, "", a); gsub(/[][]/, "", a)
  k = a "\t" e "\t" t; n[k]++
  ts = $1 " " $2
  if (!(k in f)) f[k] = ts
  l[k] = ts
}
END { for (k in n) print k "\t" n[k] "\t" f[k] "\t" l[k] }' "$T"
rm -f "$T"
`

// seenIP is one aggregated access-log row.
type seenIP struct {
	IP, Email, Tag string
	Conns          int64
	First, Last    time.Time
}

// accessSummary is accessLogScript's output.
type accessSummary struct {
	Size string // access log size in bytes before it was taken, or "missing"
	Run  string // when missing: the running xray's cwd, config and access setting
	Rows []seenIP
	Junk []string // lines that were not understood (e.g. an awk error)
}

// parseAccessSummary reads accessLogScript's output.
func parseAccessSummary(out string) (*accessSummary, error) {
	sum := &accessSummary{}
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	var loc *time.Location
	for sc.Scan() {
		line := sc.Text()
		if z, ok := strings.CutPrefix(line, "TZ "); ok && loc == nil {
			var err error
			if loc, err = zoneOf(strings.TrimSpace(z)); err != nil {
				return nil, err
			}
			continue
		}
		if sz, ok := strings.CutPrefix(line, "SIZE "); ok && sum.Size == "" {
			sum.Size = strings.TrimSpace(sz)
			continue
		}
		if r, ok := strings.CutPrefix(line, "RUN "); ok && sum.Run == "" {
			sum.Run = strings.TrimSpace(r)
			continue
		}
		p := strings.Split(line, "\t")
		if len(p) != 6 || loc == nil {
			if strings.TrimSpace(line) != "" && len(sum.Junk) < 5 {
				sum.Junk = append(sum.Junk, line)
			}
			continue
		}
		if len(p) != 6 || p[0] == "" {
			continue
		}
		if p[0] == "" {
			continue
		}
		n, _ := strconv.ParseInt(p[3], 10, 64)
		first, err1 := time.ParseInLocation("2006/01/02 15:04:05.999999", p[4], loc)
		last, err2 := time.ParseInLocation("2006/01/02 15:04:05.999999", p[5], loc)
		if err1 != nil || err2 != nil {
			continue
		}
		sum.Rows = append(sum.Rows, seenIP{IP: p[0], Email: p[1], Tag: p[2], Conns: n, First: first.UTC(), Last: last.UTC()})
	}
	if loc == nil {
		return nil, fmt.Errorf("unexpected output: %.200q", out)
	}
	return sum, sc.Err()
}

// zoneOf turns `date +%z` ("+0330") into a fixed zone.
func zoneOf(z string) (*time.Location, error) {
	t, err := time.Parse("-0700", z)
	if err != nil {
		return nil, fmt.Errorf("bad server timezone %q", z)
	}
	_, off := t.Zone()
	return time.FixedZone(z, off), nil
}

// collectIPs reads the client addresses seen since the last run and stores
// them, with their GeoIP answer, keyed by server, day, address and user. It
// returns a short note for the report's per-server status.
func (o *Orchestrator) collectIPs(ctx context.Context, remote Remote, panel Panel, s *models.Server, tagRemark map[string]string) (string, error) {
	out, err := remote.Run(ctx, accessLogScript, nil)
	if err != nil {
		return "", fmt.Errorf("access log: %w: %.200s", err, strings.TrimSpace(out))
	}
	sum, err := parseAccessSummary(out)
	if err != nil {
		return "", err
	}
	if sum.Size == "missing" {
		// The template has the access log but the running xray does not:
		// 3x-ui did not regenerate its config. Restart xray, then the whole
		// panel if that was not enough.
		if !strings.Contains(sum.Run, `"access":"/`) {
			switch o.kickXray(s.ID) {
			case 1:
				if err := panel.RestartXray(ctx); err != nil {
					return "", fmt.Errorf("access log missing (%s); restarting xray: %w", sum.Run, err)
				}
				return "", fmt.Errorf("access log missing (%s); xray restarted", sum.Run)
			case 2:
				if out, err := remote.Run(ctx, "systemctl restart x-ui", nil); err != nil {
					return "", fmt.Errorf("access log missing (%s); restarting x-ui: %w %s", sum.Run, err, out)
				}
				return "", fmt.Errorf("access log missing (%s); x-ui restarted", sum.Run)
			}
		}
		return "", fmt.Errorf("xray has not written its access log yet (%s)", sum.Run)
	}
	if len(sum.Rows) == 0 && len(sum.Junk) > 0 {
		return "", fmt.Errorf("access log not understood: %.200s", strings.Join(sum.Junk, " | "))
	}
	note := fmt.Sprintf("log %s bytes, %d rows", sum.Size, len(sum.Rows))
	if len(sum.Rows) == 0 {
		return note, nil
	}
	return note, StoreClientIPs(o.DB, o.Geo, s.ID, sum.Rows, tagRemark)
}

// kickXray counts restart attempts for a server whose xray runs without the
// access log (1 = restart xray, 2 = restart x-ui, then give up until the
// process restarts).
func (o *Orchestrator) kickXray(id uint) int {
	v, _ := o.xrayKicks.LoadOrStore(id, new(int))
	n := v.(*int)
	*n++
	return *n
}

// recordIPCollect stores how the last address collection went on s.
func (o *Orchestrator) recordIPCollect(s *models.Server, note string, err error) {
	if err != nil {
		note = "error: " + err.Error()
		log.Printf("stats %s: client ips: %v", s.Name, err)
	}
	now := time.Now()
	s.IPCollectAt, s.IPCollectNote = &now, truncate(note, 255)
	o.DB.Model(s).Select("ip_collect_at", "ip_collect_note").Updates(s)
}

// StoreClientIPs upserts rows and fills the GeoIP cache for new addresses.
func StoreClientIPs(db *gorm.DB, geo GeoLookup, serverID uint, rows []seenIP, tagRemark map[string]string) error {
	recs := make([]models.ClientIP, 0, len(rows))
	ips := map[string]bool{}
	for _, r := range rows {
		inbound := r.Tag
		if rm, ok := tagRemark[r.Tag]; ok {
			inbound = rm
		}
		recs = append(recs, models.ClientIP{
			ServerID: serverID, Day: r.Last.Format("2006-01-02"), IP: r.IP, Email: r.Email, Inbound: inbound,
			Conns: r.Conns, FirstSeen: r.First, LastSeen: r.Last,
		})
		ips[r.IP] = true
	}
	err := db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "server_id"}, {Name: "day"}, {Name: "ip"}, {Name: "email"}},
		DoUpdates: clause.Assignments(map[string]any{
			"conns":     gorm.Expr("client_ips.conns + excluded.conns"),
			"last_seen": gorm.Expr("excluded.last_seen"),
			"inbound":   gorm.Expr("excluded.inbound"),
		}),
	}).CreateInBatches(recs, 200).Error
	if err != nil {
		return err
	}
	if geo == nil {
		return nil
	}
	list := make([]string, 0, len(ips))
	for ip := range ips {
		list = append(list, ip)
	}
	var known []string
	db.Model(&models.IPGeo{}).Where("ip IN ? AND updated_at > ?", list, time.Now().Add(-ClientIPRetention)).Pluck("ip", &known)
	have := map[string]bool{}
	for _, ip := range known {
		have[ip] = true
	}
	var geos []models.IPGeo
	for _, ip := range list {
		if have[ip] {
			continue
		}
		if g, ok := geo.Lookup(ip); ok {
			g.UpdatedAt = time.Now()
			geos = append(geos, g)
		}
	}
	if len(geos) == 0 {
		return nil
	}
	return db.Clauses(clause.OnConflict{UpdateAll: true}).CreateInBatches(geos, 200).Error
}

// FillMissingGeo resolves every stored address that has no GeoIP answer yet,
// e.g. after the database was downloaded for the first time.
func FillMissingGeo(db *gorm.DB, geo GeoLookup) int {
	var ips []string
	db.Model(&models.ClientIP{}).Distinct("ip").
		Where("ip NOT IN (?)", db.Model(&models.IPGeo{}).Select("ip").Where("country <> ''")).
		Pluck("ip", &ips)
	var geos []models.IPGeo
	for _, ip := range ips {
		if g, ok := geo.Lookup(ip); ok {
			g.UpdatedAt = time.Now()
			geos = append(geos, g)
		}
	}
	if len(geos) > 0 {
		db.Clauses(clause.OnConflict{UpdateAll: true}).CreateInBatches(geos, 200)
	}
	return len(geos)
}

// ensureAccessLog turns on xray's access log (with real, unmasked addresses)
// on a panel that does not have it yet. It is checked once per server per
// process; Apply sets it on every sync anyway.
func (o *Orchestrator) ensureAccessLog(ctx context.Context, s *models.Server, p Panel) error {
	if _, done := o.accessLogOK.Load(s.ID); done {
		return nil
	}
	setting, err := p.XraySetting(ctx)
	if err != nil {
		return err
	}
	if withAccessLog(setting) {
		if err := p.UpdateXraySetting(ctx, setting); err != nil {
			return err
		}
		if err := p.RestartXray(ctx); err != nil {
			return err
		}
	}
	o.accessLogOK.Store(s.ID, true)
	return nil
}

// withAccessLog points the template's access log at AccessLogPath and
// reports whether anything changed.
func withAccessLog(setting map[string]any) bool {
	lg, _ := setting["log"].(map[string]any)
	if lg == nil {
		lg = map[string]any{"loglevel": "warning", "error": "", "dnsLog": false}
	}
	if lg["access"] == AccessLogPath && (lg["maskAddress"] == nil || lg["maskAddress"] == "") {
		return false
	}
	lg["access"] = AccessLogPath
	lg["maskAddress"] = ""
	setting["log"] = lg
	return true
}

// PruneClientIPs drops addresses not seen within the retention period.
func PruneClientIPs(db *gorm.DB) {
	cut := time.Now().Add(-ClientIPRetention)
	db.Where("last_seen < ?", cut).Delete(&models.ClientIP{})
	db.Where("updated_at < ? AND ip NOT IN (?)", cut, db.Model(&models.ClientIP{}).Select("ip")).Delete(&models.IPGeo{})
}
