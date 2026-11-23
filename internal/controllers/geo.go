package controllers

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"

	"microserver/internal/models"
	"microserver/internal/services/orchestrator"
	"microserver/internal/services/settings"
)

// OnlineWindow is how recently an address must have opened a connection to
// count as online.
const OnlineWindow = 10 * time.Minute

type geoGroup struct {
	Country string `json:"country"`
	Region  string `json:"region"`
	City    string `json:"city"`
	ASN     uint   `json:"asn"`
	ISP     string `json:"isp"`
	Key     string `json:"key"`
	IPs     int64  `json:"ips"`
	Online  int64  `json:"online"`
	Users   int64  `json:"users"`
	Conns   int64  `json:"conns"`
}

type geoUser struct {
	Email    string `json:"email"`
	IPs      int64  `json:"ips"`
	Online   int64  `json:"online"`
	Conns    int64  `json:"conns"`
	Servers  int64  `json:"servers"`
	Cities   string `json:"cities"`
	ISPs     string `json:"isps"`
	LastSeen string `json:"last_seen"`
}

type geoIP struct {
	IP       string `json:"ip"`
	Country  string `json:"country"`
	Region   string `json:"region"`
	City     string `json:"city"`
	ASN      uint   `json:"asn"`
	ISP      string `json:"isp"`
	Emails   string `json:"emails"`
	Servers  string `json:"servers"`
	Inbounds string `json:"inbounds"`
	Conns    int64  `json:"conns"`
	LastSeen string `json:"last_seen"`
	Online   bool   `json:"online"`
}

// GeoReportTimeout caps the report's queries. SQLite has one connection, so
// while they run every other request waits; on a large client_ips table an
// uncapped report held the whole panel for many minutes.
var GeoReportTimeout = 20 * time.Second

// geoCacheTTL is how long a built report is served again as is.
const geoCacheTTL = time.Minute

var geoState struct {
	build sync.Mutex // one report at a time
	mu    sync.Mutex
	cache map[string]geoCached
}

type geoCached struct {
	at   time.Time
	body fiber.Map
}

// GeoReport summarises the client addresses seen on every server by
// country, province, city, ISP, server, inbound and user. Reports are built
// one at a time, under GeoReportTimeout, and kept for geoCacheTTL.
func (a *App) GeoReport(c *fiber.Ctx) error {
	key := string(c.Request().URI().QueryString())
	if body, ok := geoCacheGet(key); ok {
		return c.JSON(body)
	}
	if !geoState.build.TryLock() {
		return errJSON(c, fiber.StatusServiceUnavailable, "گزارش دیگری در حال ساخت است؛ چند ثانیه بعد دوباره امتحان کنید")
	}
	defer geoState.build.Unlock()
	if body, ok := geoCacheGet(key); ok {
		return c.JSON(body)
	}
	ctx, cancel := context.WithTimeout(context.Background(), GeoReportTimeout)
	defer cancel()
	body := a.geoReport(c, a.DB.WithContext(ctx))
	if ctx.Err() != nil {
		return errJSON(c, fiber.StatusServiceUnavailable, "ساخت گزارش بیش از حد طول کشید و متوقف شد؛ بازه کوتاه‌تر یا یک سرور را انتخاب کنید")
	}
	geoState.mu.Lock()
	if geoState.cache == nil {
		geoState.cache = map[string]geoCached{}
	}
	for k, v := range geoState.cache {
		if time.Since(v.at) > geoCacheTTL {
			delete(geoState.cache, k)
		}
	}
	geoState.cache[key] = geoCached{at: time.Now(), body: body}
	geoState.mu.Unlock()
	return c.JSON(body)
}

func geoCacheGet(key string) (fiber.Map, bool) {
	geoState.mu.Lock()
	defer geoState.mu.Unlock()
	v, ok := geoState.cache[key]
	if !ok || time.Since(v.at) > geoCacheTTL {
		return nil, false
	}
	return v.body, true
}

// geoReport builds the report with db (which carries the time limit).
func (a *App) geoReport(c *fiber.Ctx, db *gorm.DB) fiber.Map {
	now := time.Now().UTC()
	online := now.Add(-OnlineWindow)
	since := now.Add(-24 * time.Hour)
	switch c.Query("range") {
	case "online":
		since = online
	case "1h":
		since = now.Add(-time.Hour)
	case "7d":
		since = now.Add(-7 * 24 * time.Hour)
	case "30d":
		since = now.Add(-30 * 24 * time.Hour)
	}
	q := func() *gorm.DB {
		d := db.Table("client_ips c").
			Joins("LEFT JOIN ip_geos g ON g.ip = c.ip").
			Where("c.last_seen >= ?", since)
		if sid := c.QueryInt("server_id"); sid > 0 {
			d = d.Where("c.server_id = ?", sid)
		}
		if in := c.Query("inbound"); in != "" {
			d = d.Where("c.inbound = ?", in)
		}
		if co := c.Query("country"); co != "" {
			d = d.Where("g.country = ?", co)
		}
		if s := strings.TrimSpace(c.Query("q")); s != "" {
			like := "%" + s + "%"
			d = d.Where("c.ip LIKE ? OR c.email LIKE ? OR g.city LIKE ? OR g.region LIKE ? OR g.isp LIKE ?", like, like, like, like, like)
		}
		return d
	}
	const counts = "COUNT(DISTINCT c.ip) AS ips, COUNT(DISTINCT CASE WHEN c.last_seen >= ? THEN c.ip END) AS online, COUNT(DISTINCT c.email) AS users, COALESCE(SUM(c.conns), 0) AS conns"
	group := func(cols, groupBy string) []geoGroup {
		out := []geoGroup{} // never null in JSON: the page maps over every list
		q().Select(cols+", "+counts, online).Group(groupBy).Order("ips DESC").Limit(300).Scan(&out)
		return out
	}

	var totals struct {
		IPs         int64 `json:"ips"`
		Online      int64 `json:"online"`
		Users       int64 `json:"users"`
		Conns       int64 `json:"conns"`
		OnlineUsers int64 `json:"online_users"`
	}
	q().Select(counts+", COUNT(DISTINCT CASE WHEN c.last_seen >= ? THEN c.email END) AS online_users", online, online).Scan(&totals)

	users := []geoUser{}
	q().Select(`c.email AS email, COUNT(DISTINCT c.ip) AS ips,
		COUNT(DISTINCT CASE WHEN c.last_seen >= ? THEN c.ip END) AS online, COALESCE(SUM(c.conns), 0) AS conns,
		COUNT(DISTINCT c.server_id) AS servers, COALESCE(GROUP_CONCAT(DISTINCT g.city), '') AS cities,
		COALESCE(GROUP_CONCAT(DISTINCT g.isp), '') AS isps, MAX(c.last_seen) AS last_seen`, online).
		Group("c.email").Order("online DESC, ips DESC").Limit(500).Scan(&users)

	ips := []geoIP{}
	q().Joins("LEFT JOIN servers s ON s.id = c.server_id").
		Select(`c.ip AS ip, COALESCE(g.country, '') AS country, COALESCE(g.region, '') AS region, COALESCE(g.city, '') AS city,
		COALESCE(g.asn, 0) AS asn, COALESCE(g.isp, '') AS isp, GROUP_CONCAT(DISTINCT c.email) AS emails,
		GROUP_CONCAT(DISTINCT s.name) AS servers, GROUP_CONCAT(DISTINCT c.inbound) AS inbounds,
		COALESCE(SUM(c.conns), 0) AS conns, MAX(c.last_seen) AS last_seen, MAX(c.last_seen >= ?) AS online`, online).
		Group("c.ip").Order("last_seen DESC").Limit(1000).Scan(&ips)

	servers := []geoGroup{}
	q().Joins("LEFT JOIN servers s ON s.id = c.server_id").
		Select("COALESCE(s.name, 'server #' || c.server_id) AS key, "+counts, online).
		Group("c.server_id").Order("ips DESC").Scan(&servers)

	inbounds := []string{}
	db.Table("client_ips").Distinct("inbound").Where("last_seen >= ?", now.Add(-orchestrator.ClientIPRetention)).Order("inbound").Pluck("inbound", &inbounds)

	type collectRow struct {
		ID            uint       `json:"id"`
		Name          string     `json:"name"`
		IPv4          string     `json:"ipv4"`
		IPCollectAt   *time.Time `json:"ip_collect_at"`
		IPCollectNote string     `json:"ip_collect_note"`
	}
	collect := []collectRow{}
	db.Table("servers").Select("id, name, ipv4, ip_collect_at, ip_collect_note").
		Where("status IN ? AND xui_port > 0", []string{models.ServerActive, models.ServerFiltered}).Order("id").Scan(&collect)

	var geo any
	if a.Geo != nil {
		geo = a.Geo.Status()
	}
	return fiber.Map{
		"totals":            totals,
		"by_country":        group("COALESCE(g.country, '') AS country", "g.country"),
		"by_region":         group("COALESCE(g.country, '') AS country, COALESCE(g.region, '') AS region", "g.country, g.region"),
		"by_city":           group("COALESCE(g.country, '') AS country, COALESCE(g.region, '') AS region, COALESCE(g.city, '') AS city", "g.country, g.region, g.city"),
		"by_isp":            group("COALESCE(g.asn, 0) AS asn, COALESCE(g.isp, '') AS isp", "g.asn, g.isp"),
		"by_inbound":        group("c.inbound AS key", "c.inbound"),
		"by_server":         servers,
		"users":             users,
		"ips":               ips,
		"inbounds":          inbounds,
		"geoip":             geo,
		"collect":           collect,
		"online_window_min": int(OnlineWindow / time.Minute),
	}
}

// UpdateGeoIP downloads the GeoIP databases again in the background.
func (a *App) UpdateGeoIP(c *fiber.Ctx) error {
	if a.Geo == nil {
		return errJSON(c, fiber.StatusServiceUnavailable, "GeoIP is not available")
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		if err := a.Geo.Update(ctx, a.Settings.Get(settings.GeoIPCityURL), a.Settings.Get(settings.GeoIPASNURL)); err == nil {
			orchestrator.FillMissingGeo(a.DB, a.Geo)
		}
	}()
	return c.Status(fiber.StatusAccepted).JSON(fiber.Map{"ok": true})
}
