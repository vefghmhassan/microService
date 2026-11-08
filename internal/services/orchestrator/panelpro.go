package orchestrator

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"

	"microserver/internal/jobs"
	"microserver/internal/models"
	"microserver/internal/services/linkgen"
	"microserver/internal/services/settings"
	"microserver/internal/services/xui"
)

// The 3x-ui panel's own side of a server, set up the way
// pdnczone/3x-ui-pro-configs does it on a panel: one client attached to every
// inbound, a Hosts row per address a config is reached at (Main, IPv6,
// Fragment, CDN, clean IPs), link names from those rows ({{HOST}}) and the
// panel's subscription with its JSON variant. Our template and vpn-pannel
// keep working as before; this only adds to the panel.

// ProPanel is the part of the 3x-ui API this needs; *xui.Client has it.
type ProPanel interface {
	Settings(ctx context.Context) (map[string]any, error)
	UpdateSettings(ctx context.Context, all map[string]any) error
	ListClients(ctx context.Context) ([]xui.PanelClient, error)
	AddClient(ctx context.Context, client map[string]any, inboundIDs []int) error
	AttachClient(ctx context.Context, email string, inboundIDs []int) error
	ListHosts(ctx context.Context) ([]xui.HostGroup, error)
	AddHost(ctx context.Context, h xui.HostGroup) error
	DeleteHost(ctx context.Context, groupID string) error
}

// HostTag marks the Hosts rows this app manages; rows without it are the
// operator's and are left alone.
const HostTag = "ms"

// fragMask is the script's client-side ClientHello fragment, as a Hosts
// row's finalMask.
const fragMask = `{"tcp":[{"type":"fragment","settings":{"packets":"tlshello","lengths":["10-30"],"delays":["5-15"]}}]}`

// cfHTTPSPorts are the ports Cloudflare proxies HTTPS on.
var cfHTTPSPorts = []int{443, 2053, 2083, 2087, 2096, 8443}

// ReservedPorts are the Cloudflare ports kept for the 3x-ui panel and its
// subscription: no inbound may use them.
func ReservedPorts(st *settings.Store) (panel, sub int) {
	return st.Int(settings.XUICFPanelPort), st.Int(settings.XUICFSubPort)
}

// panelHosts are the Hosts rows of one inbound (its id in the panel), the
// way the script's HOSTS() builds them. snap is narrowed to s by For, with
// Clean set; withClean adds the clean-IP rows (once per server).
func panelHosts(snap *Snapshot, s *models.Server, t models.InboundTemplate, id int, withClean bool) []xui.HostGroup {
	row := func(remark, addr string) xui.HostGroup {
		return xui.HostGroup{InboundIDs: []int{id}, Hosts: []string{addr}, Remark: remark, Tags: []string{HostTag}, Security: "same"}
	}
	addr, sni := snap.target(t, LinkHost(s))
	if addr == "" {
		return nil
	}
	named := net.ParseIP(addr) == nil
	if sni == "" && named {
		sni = addr
	}
	v6 := ""
	if s.Family() == models.FamilyV4 && s.IPv6 != "" {
		v6 = s.IPv6
	}
	tls := linkgen.Security(t.StreamSettings) == "tls"
	cdn := (t.AddonID != 0 && t.Route == models.RouteCDN) || (t.AddonID == 0 && t.CDN && s.DNSProxied)
	var out []xui.HostGroup
	switch {
	case cdn:
		h := row("☁️ "+t.Remark+" · CDN", addr)
		h.Sni, h.HostHeader, h.FinalMask = sni, sni, fragMask
		out = append(out, h)
		if withClean {
			for i, ip := range snap.Clean {
				c := row(fmt.Sprintf("🧊 Clean %02d · %s", i+1, ip.Address), ip.Address)
				c.Sni, c.HostHeader, c.FinalMask = sni, sni, fragMask
				out = append(out, c)
			}
		}
	case t.Protocol == "hysteria":
		h := row("🚄 "+t.Remark+" · Main", addr)
		h.Sni = sni
		out = append(out, h)
	case tls:
		h := row("🔐 "+t.Remark+" · Main", addr)
		h.Sni = sni
		f := row("🧩 "+t.Remark+" · Fragment", addr)
		f.Sni, f.FinalMask = sni, fragMask
		out = append(out, h, f)
		if v6 != "" {
			x := row("6️⃣ "+t.Remark+" · IPv6", v6)
			x.Sni = sni
			out = append(out, x)
		}
	default:
		out = append(out, row("⚡ "+t.Remark+" · Main", addr))
		if v6 != "" {
			out = append(out, row("6️⃣ "+t.Remark+" · IPv6", v6))
		}
	}
	for i := range out {
		out[i].SortOrder = i
	}
	return out
}

// isCleanCDN reports whether t gets the clean-IP rows: an add-on inbound
// marked CleanIPs on the orange name, or a shared CDN inbound of a proxied
// server.
func isCleanCDN(s *models.Server, t models.InboundTemplate) bool {
	if t.AddonID != 0 {
		return t.CleanIPs && t.Route == models.RouteCDN
	}
	return t.CDN && s.DNSProxied
}

// syncPanelPro sets up the panel side of s after the template was applied.
// Every step only warns: the server works without it.
func (o *Orchestrator) syncPanelPro(ctx context.Context, r *jobs.Run, s *models.Server, panel Panel, snap *Snapshot) {
	pp, ok := panel.(ProPanel)
	if !ok {
		return
	}
	existing, err := panel.ListInbounds(ctx)
	if err != nil {
		r.Logf("warn", "panel hosts: %v", err)
		return
	}
	byPort := map[int]int{}
	var ids []int
	for _, in := range existing {
		if strings.HasPrefix(in.Remark, ManagedPrefix) {
			byPort[in.Port] = in.ID
			ids = append(ids, in.ID)
		}
	}
	if len(ids) == 0 {
		return
	}
	if err := o.panelClient(ctx, r, s, pp, ids); err != nil {
		r.Logf("warn", "panel client: %v", err)
	}
	if n, err := o.panelHostRows(ctx, pp, s, snap, byPort); err != nil {
		r.Logf("warn", "panel hosts: %v", err)
	} else {
		r.Info("3x-ui Hosts: %d rows", n)
	}
	if err := panel.RestartXray(ctx); err != nil {
		r.Logf("warn", "restart xray: %v", err)
	}
	if err := o.panelSubscription(ctx, r, s, panel, pp, snap); err != nil {
		r.Logf("warn", "panel subscription: %v", err)
	}
}

// panelClient makes sure the server's one client exists and is attached to
// every managed inbound. Updating an inbound rewrites its settings, so it is
// attached again on every sync.
func (o *Orchestrator) panelClient(ctx context.Context, r *jobs.Run, s *models.Server, pp ProPanel, ids []int) error {
	if s.PanelClient == "" || s.SubID == "" {
		// Server names usually start with "ms-" already; don't double it.
		s.PanelClient = "ms-" + strings.TrimPrefix(strings.ToLower(s.Name), "ms-")
		s.SubID = strings.ToLower(randomSuffix(16))
		o.save(r, s, "panel_client", "sub_id")
	}
	clients, err := pp.ListClients(ctx)
	if err != nil {
		return err
	}
	for _, c := range clients {
		if c.Email == s.PanelClient {
			return pp.AttachClient(ctx, s.PanelClient, ids)
		}
	}
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	client := map[string]any{
		"email": s.PanelClient, "subId": s.SubID, "flow": "xtls-rprx-vision",
		// Also a valid Shadowsocks-2022 key, like the script's.
		"password": base64.StdEncoding.EncodeToString(key),
		"totalGB":  0, "expiryTime": 0, "limitIp": 0, "enable": true, "tgId": 0, "comment": "microServer",
	}
	if err := pp.AddClient(ctx, client, ids); err != nil {
		return err
	}
	r.Info("panel client %s on %d inbounds", s.PanelClient, len(ids))
	return nil
}

// panelHostRows replaces this app's Hosts rows with the current ones.
func (o *Orchestrator) panelHostRows(ctx context.Context, pp ProPanel, s *models.Server, snap *Snapshot, byPort map[int]int) (int, error) {
	cur, err := pp.ListHosts(ctx)
	if err != nil {
		return 0, err
	}
	var errs []error
	for _, h := range cur {
		for _, tag := range h.Tags {
			if tag == HostTag && h.GroupID != "" {
				if err := pp.DeleteHost(ctx, h.GroupID); err != nil {
					errs = append(errs, err)
				}
				break
			}
		}
	}
	n, cleanDone := 0, false
	for _, t := range snap.Inbounds {
		id, ok := byPort[t.Port]
		if !t.Enabled || !ok {
			continue
		}
		withClean := !cleanDone && len(snap.Clean) > 0 && isCleanCDN(s, t)
		cleanDone = cleanDone || withClean
		for _, h := range panelHosts(snap, s, t, id, withClean) {
			if err := pp.AddHost(ctx, h); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", h.Remark, err))
				continue
			}
			n++
		}
	}
	return n, errors.Join(errs...)
}

// panelSubscription turns the panel's subscription on like the script
// (names from Hosts, JSON variant on) on the reserved Cloudflare port, with
// the zone certificate on SSL servers, and stores the client's links.
func (o *Orchestrator) panelSubscription(ctx context.Context, r *jobs.Run, s *models.Server, panel Panel, pp ProPanel, snap *Snapshot) error {
	_, subPort := ReservedPorts(o.Settings)
	for _, t := range snap.Inbounds {
		if t.Enabled && t.Port == subPort {
			return fmt.Errorf("port %d is used by inbound %s; the panel subscription stays off", subPort, t.Remark)
		}
	}
	all, err := pp.Settings(ctx)
	if err != nil {
		return err
	}
	want := map[string]any{
		"remarkTemplate": "{{HOST}}", "subEnable": true, "subJsonEnable": true, "subPort": subPort,
	}
	ssl := s.SSL && s.Hostname != "" && s.CertNotAfter != nil
	if ssl {
		want["subCertFile"], want["subKeyFile"], want["subDomain"] = CertFile, KeyFile, s.Hostname
	}
	if str(all, "subPath") == "" {
		want["subPath"] = "/sub/"
	}
	if str(all, "subJsonPath") == "" {
		want["subJsonPath"] = "/json/"
	}
	changed := false
	for k, v := range want {
		if fmt.Sprint(all[k]) != fmt.Sprint(v) {
			all[k], changed = v, true
		}
	}
	if changed {
		if err := pp.UpdateSettings(ctx, all); err != nil {
			return err
		}
		if err := panel.RestartPanel(ctx); err != nil {
			r.Logf("warn", "restart panel: %v", err)
		}
		// Give the old process time to exit before the next call.
		_ = sleep(ctx, o.RetryDelay/6)
		if err := o.waitPanel(ctx, r, s, panel); err != nil {
			return err
		}
	}
	scheme, host := "http", firstNonEmpty(s.Hostname, s.PublicIP())
	if ssl {
		scheme = "https"
	}
	base := scheme + "://" + net.JoinHostPort(host, strconv.Itoa(subPort))
	s.PanelSubURL = base + withSlashes(str(all, "subPath")) + s.SubID
	s.PanelSubJSON = base + withSlashes(str(all, "subJsonPath")) + s.SubID
	o.save(r, s, "panel_sub_url", "panel_sub_json")
	r.Info("panel subscription %s (JSON %s)", s.PanelSubURL, s.PanelSubJSON)
	return nil
}

func str(m map[string]any, k string) string {
	if v, ok := m[k].(string); ok {
		return v
	}
	return ""
}

func withSlashes(p string) string {
	return "/" + strings.Trim(p, "/") + "/"
}
