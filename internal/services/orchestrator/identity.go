package orchestrator

import (
	"context"
	"crypto/hmac"
	crand "crypto/rand"
	"crypto/sha256"
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
// Reality keys, short IDs, client IDs and paths, so one config taken out of
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

// newIdentity gives s a fresh seed, and the profile and one Reality target
// of its location (loc may be nil: a manual server).
func newIdentity(s *models.Server, loc *models.Location) {
	b := make([]byte, 32)
	_, _ = crand.Read(b)
	s.IdentitySeed = secret.String(hex.EncodeToString(b))
	s.RealitySNI = ""
	if loc == nil {
		return
	}
	s.Profile = loc.Profile
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

// personalize is inbound t as server s gets it. Only Reality inbounds of a
// server with a seed change; anything that cannot be parsed is left alone.
func personalize(t models.InboundTemplate, s *models.Server) models.InboundTemplate {
	seed := string(s.IdentitySeed)
	if seed == "" || linkgen.Security(t.StreamSettings) != "reality" {
		return t
	}
	var stream, settings map[string]any
	if json.Unmarshal([]byte(t.StreamSettings), &stream) != nil {
		return t
	}
	r, _ := stream["realitySettings"].(map[string]any)
	if r == nil {
		return t
	}
	label := func(field string) string { return fmt.Sprintf("%d|%s", t.ID, field) }

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

	if json.Unmarshal([]byte(t.Settings), &settings) == nil {
		if cs, ok := settings["clients"].([]any); ok {
			for i, c := range cs {
				cm, _ := c.(map[string]any)
				if cm == nil {
					continue
				}
				if _, ok := cm["id"]; ok {
					cm["id"] = deriveUUID(seed, label(fmt.Sprintf("uuid%d", i)))
				}
				if _, ok := cm["password"]; ok {
					cm["password"] = deriveHex(seed, label(fmt.Sprintf("pass%d", i)), 32)
				}
			}
			if b, err := json.Marshal(settings); err == nil {
				t.Settings = string(b)
			}
		}
	}
	b, err := json.Marshal(stream)
	if err != nil {
		return t
	}
	t.StreamSettings = string(b)
	return t
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
// and not one Xray warns against as a target.
func sniOK(name string) bool {
	if !domainRe.MatchString(name) {
		return false
	}
	for _, bad := range []string{"apple.com", "icloud.com"} {
		if name == bad || strings.HasSuffix(name, "."+bad) {
			return false
		}
	}
	return true
}

func sameSlash24(a, b string) bool {
	x, y := net.ParseIP(a).To4(), net.ParseIP(b).To4()
	return x != nil && y != nil && x[0] == y[0] && x[1] == y[1] && x[2] == y[2]
}

// chooseSNI picks a Reality target for a server by probing from it. First
// its own /24: a site hosted next door shares the server's network, so the
// borrowed certificate fits the address. A neighbour's certificate name only
// counts when the name itself resolves back into that /24 from the server;
// that leaves out machines that merely relay someone else's certificate.
// Without such a neighbour it falls back to 3x-ui's built-in candidates.
// The pick is random among what passed. source says where it came from.
func chooseSNI(ctx context.Context, sc sniScanner, ipv4 string) (sni, source string) {
	if ip := net.ParseIP(ipv4).To4(); ip != nil {
		// The panel client gives up after 90s; a /24 that takes longer
		// counts as no neighbour and the built-in list is used.
		sctx, cancel := context.WithTimeout(ctx, 90*time.Second)
		near, _ := sc.ScanRealityTargets(sctx, fmt.Sprintf("%d.%d.%d.0/24", ip[0], ip[1], ip[2]))
		cancel()
		seen := map[string]bool{}
		var names []string
		for _, rs := range near {
			if !rs.Feasible || rs.PrivateTarget || rs.IP == ipv4 {
				continue
			}
			for _, n := range append([]string{rs.Host}, rs.ServerNames...) {
				n = strings.ToLower(strings.TrimSpace(n))
				if sniOK(n) && !seen[n] {
					seen[n] = true
					names = append(names, n)
				}
			}
		}
		rand.Shuffle(len(names), func(i, j int) { names[i], names[j] = names[j], names[i] })
		if len(names) > sniVerifyMax {
			names = names[:sniVerifyMax]
		}
		for _, n := range names {
			vctx, cancel := context.WithTimeout(ctx, 20*time.Second)
			v, err := sc.ScanRealityTarget(vctx, n+":443", n, 0)
			cancel()
			if err == nil && v != nil && v.Feasible && !v.PrivateTarget && sameSlash24(v.IP, ipv4) {
				return n, "همسایهٔ سرور"
			}
		}
	}
	sctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	list, _ := sc.ScanRealityTargets(sctx, "")
	var ok []string
	for _, rs := range list { // best first
		n := strings.ToLower(strings.TrimSpace(rs.Host))
		if rs.Feasible && !rs.PrivateTarget && sniOK(n) {
			if ok = append(ok, n); len(ok) == sniTopN {
				break
			}
		}
	}
	if len(ok) == 0 {
		return "", ""
	}
	return ok[rand.IntN(len(ok))], "فهرست پیش‌فرض"
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
	sni, source := chooseSNI(ctx, sc, s.IPv4)
	if sni == "" {
		r.Logf("warn", "Reality scan found no usable target; keeping the template's SNI")
		return
	}
	s.RealitySNI = sni
	o.save(r, s, "reality_sni")
	r.Info("Reality SNI %s (%s)", sni, source)
}
