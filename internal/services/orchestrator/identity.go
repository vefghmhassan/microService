package orchestrator

import (
	"context"
	"crypto/hmac"
	crand "crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net"
	"regexp"
	"strings"
	"time"

	"microserver/internal/jobs"
	"microserver/internal/models"
	"microserver/internal/services/linkgen"
	"microserver/internal/services/secret"
	"microserver/internal/services/smart"
	"microserver/internal/services/xui"
)

// Per-server identity. The shared template gives every server the same
// Reality keys, short IDs, client IDs, passwords and paths, so one config taken out of
// the app is enough to recognise the whole fleet with a valid handshake. A
// server with an IdentitySeed gets its own, derived from the seed, and the
// Reality target picked from its location's list. Servers without a seed
// (built before this existed) keep the template's values untouched.

var profileRe = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)

// ValidProfile checks a profile name; empty is fine.
func ValidProfile(p string) error {
	if p != "" && !profileRe.MatchString(p) {
		return fmt.Errorf("نام پروفایل فقط حروف کوچک انگلیسی، عدد، - و _ است (حداکثر ۳۲ نویسه)")
	}
	return nil
}

// ProfileList is a comma/newline separated profile list, normalised.
func ProfileList(v string) []string {
	var out []string
	for _, p := range smart.Lines(strings.ToLower(v)) {
		for _, f := range strings.Fields(p) {
			out = append(out, f)
		}
	}
	return out
}

// forProfile reports whether inbound t goes on servers of the profile: an
// inbound without profiles goes everywhere.
func forProfile(t models.InboundTemplate, profile string) bool {
	list := ProfileList(t.Profiles)
	if len(list) == 0 {
		return true
	}
	for _, p := range list {
		if p == profile {
			return true
		}
	}
	return false
}

// SNIList is a location's Reality targets, one per entry.
func SNIList(v string) []string {
	var out []string
	for _, l := range smart.Lines(v) {
		for _, f := range strings.Fields(strings.ToLower(l)) {
			f = strings.TrimSuffix(f, ":443")
			if f != "" {
				out = append(out, f)
			}
		}
	}
	return out
}

// Identity versions (see Server.IdentityVer). identityAll covers every
// inbound's client IDs and passwords, not only the Reality ones.
// identityByEmail derives them from the client's email instead of the
// inbound: the template puts the same client (same email) on two inbounds,
// and a panel that then holds two different IDs under one email accepts
// only one of them, which left one inbound of each such pair dead.
const (
	identityAll     = 2
	identityByEmail = 3
)

// newIdentity gives s a fresh seed, and the profile and one Reality target
// of its location (loc may be nil: a manual server).
func newIdentity(s *models.Server, loc *models.Location) {
	b := make([]byte, 32)
	_, _ = crand.Read(b)
	s.IdentitySeed = secret.String(hex.EncodeToString(b))
	s.IdentityVer = identityByEmail
	s.RealitySNI = ""
	if loc == nil {
		return
	}
	s.Profile = loc.Profile
	if loc.NodeMode && s.Role == "" {
		s.Role = models.RoleNode
	}
	if snis := SNIList(loc.RealitySNIs); len(snis) > 0 {
		s.RealitySNI = snis[int(derive(string(s.IdentitySeed), "sni")[0])%len(snis)]
	}
}

// renewIdentity is newIdentity for a server that is being reinstalled.
func (o *Orchestrator) renewIdentity(s *models.Server) {
	var loc *models.Location
	if s.LocationID != 0 {
		var l models.Location
		if o.DB.First(&l, s.LocationID).Error == nil {
			loc = &l
		}
	}
	profile := s.Profile
	newIdentity(s, loc)
	if loc == nil {
		s.Profile = profile
	}
}

func derive(seed string, label string) []byte {
	m := hmac.New(sha256.New, []byte(seed))
	m.Write([]byte(label))
	return m.Sum(nil)
}

func deriveHex(seed, label string, n int) string {
	return hex.EncodeToString(derive(seed, label))[:n]
}

func deriveUUID(seed, label string) string {
	b := derive(seed, label)[:16]
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// personalize is inbound t as server s gets it. A server with a seed gets
// its own client IDs and passwords on every inbound (a Shadowsocks key or a
// plain VLESS ID taken from the app would otherwise confirm every server),
// and on Reality inbounds its own keys, short IDs, spiderX, paths and
// target. Anything that cannot be parsed is left alone. A VLESS Encryption
// pair (decryption) stays the template's: only xray can generate one.
func personalize(t models.InboundTemplate, s *models.Server) models.InboundTemplate {
	seed := string(s.IdentitySeed)
	if seed == "" {
		return t
	}
	label := func(field string) string { return fmt.Sprintf("%d|%s", t.ID, field) }
	reality := linkgen.Security(t.StreamSettings) == "reality"
	if reality || s.IdentityVer >= identityAll {
		t.Settings = personalSettings(t, seed, label, s.IdentityVer >= identityByEmail)
	}
	if !reality {
		return t
	}
	var stream map[string]any
	if json.Unmarshal([]byte(t.StreamSettings), &stream) != nil {
		return t
	}
	r, _ := stream["realitySettings"].(map[string]any)
	if r == nil {
		return t
	}
	priv, pub, err := linkgen.X25519From(derive(seed, label("x25519")))
	if err != nil {
		return t
	}
	r["privateKey"] = priv
	rs, _ := r["settings"].(map[string]any)
	if rs == nil {
		rs = map[string]any{}
		r["settings"] = rs
	}
	rs["publicKey"] = pub
	rs["spiderX"] = "/" + deriveHex(seed, label("spx"), 15)
	if ids, ok := r["shortIds"].([]any); ok {
		for i, v := range ids {
			if cur, _ := v.(string); cur != "" {
				ids[i] = deriveHex(seed, label(fmt.Sprintf("sid%d", i)), len(cur))
			}
		}
	}
	if s.RealitySNI != "" {
		r["serverNames"] = []any{s.RealitySNI}
		r["target"] = s.RealitySNI + ":443"
		if _, ok := r["dest"]; ok {
			r["dest"] = s.RealitySNI + ":443"
		}
	}
	if x, ok := stream["xhttpSettings"].(map[string]any); ok {
		x["path"] = "/" + deriveHex(seed, label("path"), 10)
	}
	if g, ok := stream["grpcSettings"].(map[string]any); ok {
		g["serviceName"] = deriveHex(seed, label("svc"), 8)
	}
	b, err := json.Marshal(stream)
	if err != nil {
		return t
	}
	t.StreamSettings = string(b)
	return t
}

// personalSettings is t's settings with the server's own client IDs and
// passwords. Shadowsocks 2022 keys keep the length the method needs.
func personalSettings(t models.InboundTemplate, seed string, label func(string) string, byEmail bool) string {
	var settings map[string]any
	if json.Unmarshal([]byte(t.Settings), &settings) != nil {
		return t.Settings
	}
	secretFor := func(l string) string { return deriveHex(seed, l, 32) }
	if t.Protocol == "shadowsocks" {
		method, _ := settings["method"].(string)
		if strings.HasPrefix(method, "2022-") {
			n := 32
			if strings.Contains(method, "aes-128") {
				n = 16
			}
			secretFor = func(l string) string {
				return base64.StdEncoding.EncodeToString(derive(seed, l)[:n])
			}
		}
		if cur, _ := settings["password"].(string); cur != "" {
			settings["password"] = secretFor(label("sskey"))
		}
	}
	cs, _ := settings["clients"].([]any)
	for i, c := range cs {
		cm, _ := c.(map[string]any)
		if cm == nil {
			continue
		}
		// One client is one email: the same on every inbound it is on.
		idLabel, passLabel := label(fmt.Sprintf("uuid%d", i)), label(fmt.Sprintf("pass%d", i))
		if email, _ := cm["email"].(string); byEmail && email != "" {
			idLabel, passLabel = "client|"+email+"|uuid", "client|"+email+"|pass|"+t.Protocol
		}
		if cur, _ := cm["id"].(string); cur != "" {
			cm["id"] = deriveUUID(seed, idLabel)
		}
		if cur, _ := cm["password"].(string); cur != "" {
			cm["password"] = secretFor(passLabel)
		}
	}
	b, err := json.Marshal(settings)
	if err != nil {
		return t.Settings
	}
	return string(b)
}

// ---- automatic Reality target ----

// sniScanner is the part of the 3x-ui client that probes Reality targets
// from the server itself (3x-ui v3.7+).
type sniScanner interface {
	ScanRealityTarget(ctx context.Context, target, sni string, xver int) (*xui.RealityScan, error)
	ScanRealityTargets(ctx context.Context, targets string) ([]xui.RealityScan, error)
}

// Limits of chooseSNI: how many neighbour names are verified, and how many
// of the built-in list's best results the pick is made from.
const (
	sniVerifyMax = 8
	sniTopN      = 5
)

var domainRe = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,}$`)

// sniOK reports whether name can be a Reality server name: a plain domain,
// not one Xray warns against as a target, and not Cloudflare's own.
func sniOK(name string) bool {
	if !domainRe.MatchString(name) {
		return false
	}
	for _, bad := range []string{"apple.com", "icloud.com", "cloudflare.com", "cloudflare.net", "cloudflare-dns.com"} {
		if name == bad || strings.HasSuffix(name, "."+bad) {
			return false
		}
	}
	return true
}

// cloudflareNets are Cloudflare's published IPv4 ranges. A Reality target
// behind them makes the server a free relay for the whole CDN (Xray's docs
// advise against it), and Cloudflare is restricted from Iran as it is.
var cloudflareNets = func() []*net.IPNet {
	var out []*net.IPNet
	for _, c := range []string{
		"173.245.48.0/20", "103.21.244.0/22", "103.22.200.0/22", "103.31.4.0/22", "141.101.64.0/18",
		"108.162.192.0/18", "190.93.240.0/20", "188.114.96.0/20", "197.234.240.0/22", "198.41.128.0/17",
		"162.158.0.0/15", "104.16.0.0/13", "104.24.0.0/14", "172.64.0.0/13", "131.0.72.0/22",
	} {
		if _, n, err := net.ParseCIDR(c); err == nil {
			out = append(out, n)
		}
	}
	return out
}()

func onCloudflare(ip string) bool {
	p := net.ParseIP(ip)
	if p == nil {
		return false
	}
	for _, n := range cloudflareNets {
		if n.Contains(p) {
			return true
		}
	}
	return false
}

func sameSlash24(a, b string) bool {
	x, y := net.ParseIP(a).To4(), net.ParseIP(b).To4()
	return x != nil && y != nil && x[0] == y[0] && x[1] == y[1] && x[2] == y[2]
}

// chooseSNI picks a Reality target for a server by probing from it. First
// its own /24, a quarter at a time (one scan has to finish within the panel
// client's 90s): a site hosted next door shares the server's network, so
// the borrowed certificate fits the address. A neighbour's certificate name
// only counts when the name itself resolves back into that /24 (resolve)
// and the server reaches it by name; that leaves out machines that merely
// relay someone else's certificate. Without such a neighbour it falls back to 3x-ui's built-in
// candidates, leaving out the ones served by Cloudflare. The pick is random
// among what passed, leaving out the names in skip (targets that were
// tried and did not work). source says where it came from and notes what
// the scans saw, for the job log.
func chooseSNI(ctx context.Context, sc sniScanner, resolve func(ctx context.Context, name string) []string, ipv4 string, skip map[string]bool) (sni, source string, notes []string) {
	if ip := net.ParseIP(ipv4).To4(); ip != nil {
		quarters := []int{0, 64, 128, 192}
		rand.Shuffle(len(quarters), func(i, j int) { quarters[i], quarters[j] = quarters[j], quarters[i] })
		seen := map[string]bool{}
		verified, elsewhere := 0, 0
		for _, q := range quarters {
			cidr := fmt.Sprintf("%d.%d.%d.%d/26", ip[0], ip[1], ip[2], q)
			sctx, cancel := context.WithTimeout(ctx, 90*time.Second)
			near, err := sc.ScanRealityTargets(sctx, cidr)
			cancel()
			if err != nil {
				notes = append(notes, fmt.Sprintf("%s: scan failed: %v", cidr, err))
				continue
			}
			var names []string
			feasible := 0
			for _, rs := range near {
				if !rs.Feasible || rs.PrivateTarget || rs.IP == ipv4 {
					continue
				}
				feasible++
				for _, n := range append([]string{rs.Host}, rs.ServerNames...) {
					n = strings.ToLower(strings.TrimSpace(n))
					if sniOK(n) && !seen[n] && !skip[n] {
						seen[n] = true
						names = append(names, n)
					}
				}
			}
			notes = append(notes, fmt.Sprintf("%s: %d answered, %d usable, %d names", cidr, len(near), feasible, len(names)))
			rand.Shuffle(len(names), func(i, j int) { names[i], names[j] = names[j], names[i] })
			for _, n := range names {
				if verified++; verified > sniVerifyMax {
					break
				}
				// 3x-ui does not say where a name resolved, so that is looked
				// up here; the server then confirms it reaches the name.
				local := false
				for _, a := range resolve(ctx, n) {
					if sameSlash24(a, ipv4) {
						local = true
						break
					}
				}
				if !local {
					elsewhere++
					continue
				}
				vctx, cancel := context.WithTimeout(ctx, 20*time.Second)
				v, err := sc.ScanRealityTarget(vctx, n+":443", n, 0)
				cancel()
				if err == nil && v != nil && v.Feasible && !v.PrivateTarget {
					return n, "همسایهٔ سرور", notes
				}
			}
			if verified > sniVerifyMax {
				break
			}
		}
		notes = append(notes, fmt.Sprintf("no neighbour hosts a site of its own (%d names resolve elsewhere); using the built-in list", elsewhere))
	}
	sctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	list, err := sc.ScanRealityTargets(sctx, "")
	if err != nil {
		notes = append(notes, fmt.Sprintf("built-in list: scan failed: %v", err))
	}
	var ok []string
	skipped := 0
	for _, rs := range list { // best first
		n := strings.ToLower(strings.TrimSpace(rs.Host))
		if !rs.Feasible || rs.PrivateTarget || !sniOK(n) || skip[n] {
			continue
		}
		if onCloudflare(rs.IP) {
			skipped++
			continue
		}
		if ok = append(ok, n); len(ok) == sniTopN {
			break
		}
	}
	if skipped > 0 {
		notes = append(notes, fmt.Sprintf("built-in list: left out %d targets served by Cloudflare", skipped))
	}
	if len(ok) == 0 {
		return "", "", notes
	}
	return ok[rand.IntN(len(ok))], "فهرست پیش‌فرض", notes
}

// lookupIPv4 is the addresses a name resolves to, as seen from the panel.
func lookupIPv4(ctx context.Context, name string) []string {
	lctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	addrs, _ := net.DefaultResolver.LookupHost(lctx, name)
	return addrs
}

// autoSNI gives a server with an identity but no Reality target one that
// was probed from the server itself; its location listed none. It never
// fails the job: without a result the template's target stays. It runs on
// the first apply only: once links are out, a new target would break them.
func (o *Orchestrator) autoSNI(ctx context.Context, r *jobs.Run, s *models.Server, panel Panel, snap *Snapshot) {
	if s.IdentitySeed == "" || s.RealitySNI != "" || s.AppliedHash != "" {
		return
	}
	sc, ok := panel.(sniScanner)
	if !ok {
		return
	}
	reality := false
	for _, t := range snap.For(s).Inbounds {
		if t.Enabled && linkgen.Security(t.StreamSettings) == "reality" {
			reality = true
			break
		}
	}
	if !reality {
		return
	}
	r.Info("no Reality SNI listed for this location; scanning from the server")
	o.pickSNI(ctx, r, s, sc, nil)
}

// pickSNI scans for a Reality target other than those in skip and stores
// it on s. It reports whether one was found.
func (o *Orchestrator) pickSNI(ctx context.Context, r *jobs.Run, s *models.Server, sc sniScanner, skip map[string]bool) bool {
	sni, source, notes := chooseSNI(ctx, sc, lookupIPv4, s.IPv4, skip)
	for _, n := range notes {
		r.Info("Reality scan: %s", n)
	}
	if sni == "" {
		r.Logf("warn", "Reality scan found no usable target; keeping %s", firstNonEmpty(s.RealitySNI, "the template's SNI"))
		return false
	}
	s.RealitySNI, s.RealitySNIAuto = sni, true
	o.save(r, s, "reality_sni", "reality_sni_auto")
	r.Info("Reality target: %s (%s)", sni, source)
	return true
}

// sniRetries is how many other targets a new server tries when none of its
// Reality inbounds passes the config test with the one it picked.
const sniRetries = 2

// realityDead reports whether s has Reality inbounds and none of them is
// among the inbounds that passed the config test.
func realityDead(snap *Snapshot, s *models.Server, passed []uint) bool {
	ok := map[uint]bool{}
	for _, id := range passed {
		ok[id] = true
	}
	n := 0
	for _, t := range snap.For(s).Inbounds {
		if !t.Enabled || linkgen.Security(t.StreamSettings) != "reality" {
			continue
		}
		if ok[t.ID] {
			return false
		}
		n++
	}
	return n > 0
}

// retrySNI gives a new server whose scanned Reality target does not work
// (a scan saying "feasible" is no proof: www.microsoft.com passes it and
// then fails every handshake) another one and applies it. Only before the
// first upload: afterwards the links are out. It reports whether the
// server now has a different target to test.
func (o *Orchestrator) retrySNI(ctx context.Context, r *jobs.Run, s *models.Server, tried map[string]bool) bool {
	if !s.RealitySNIAuto || s.UploadedAt != nil {
		return false
	}
	snap, err := LoadSnapshot(o.DB)
	if err != nil {
		return false
	}
	tried[s.RealitySNI] = true
	r.Step("Reality target " + s.RealitySNI + " does not work; trying another")
	remote, _, err := o.Connect(ctx, s)
	if err != nil {
		r.Logf("warn", "ssh: %v", err)
		return false
	}
	defer remote.Close()
	panel := o.PanelFor(s, remote)
	sc, ok := panel.(sniScanner)
	if !ok || !o.pickSNI(ctx, r, s, sc, tried) {
		return false
	}
	if err := Apply(ctx, panel, snap.For(s), r.Info); err != nil {
		r.Logf("warn", "apply new Reality target: %v", err)
		return false
	}
	return true
}
