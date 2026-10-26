package orchestrator

import (
	"bufio"
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"microserver/internal/models"
)

// AccessLogPath is where every managed xray writes its access log; client
// addresses for the GeoIP report come from it.
const AccessLogPath = "/usr/local/x-ui/access.log"

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
date +%z
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

// parseAccessSummary reads accessLogScript's output.
func parseAccessSummary(out string) ([]seenIP, error) {
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	if !sc.Scan() {
		return nil, nil
	}
	loc, err := zoneOf(strings.TrimSpace(sc.Text()))
	if err != nil {
		return nil, err
	}
	var rows []seenIP
	for sc.Scan() {
		p := strings.Split(sc.Text(), "\t")
		if len(p) != 6 || p[0] == "" {
			continue
		}
		n, _ := strconv.ParseInt(p[3], 10, 64)
		first, err1 := time.ParseInLocation("2006/01/02 15:04:05.999999", p[4], loc)
		last, err2 := time.ParseInLocation("2006/01/02 15:04:05.999999", p[5], loc)
		if err1 != nil || err2 != nil {
			continue
		}
		rows = append(rows, seenIP{IP: p[0], Email: p[1], Tag: p[2], Conns: n, First: first.UTC(), Last: last.UTC()})
	}
	return rows, sc.Err()
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
// them, with their GeoIP answer, keyed by server, day, address and user.
func (o *Orchestrator) collectIPs(ctx context.Context, remote Remote, s *models.Server, tagRemark map[string]string) error {
	out, err := remote.Run(ctx, accessLogScript, nil)
	if err != nil {
		return fmt.Errorf("access log: %w", err)
	}
	rows, err := parseAccessSummary(out)
	if err != nil || len(rows) == 0 {
		return err
	}
	return StoreClientIPs(o.DB, o.Geo, s.ID, rows, tagRemark)
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
