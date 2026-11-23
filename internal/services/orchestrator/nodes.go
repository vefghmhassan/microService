package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"microserver/internal/jobs"
	"microserver/internal/models"
	"microserver/internal/services/settings"
	"microserver/internal/services/smart"
	"microserver/internal/services/xui"
)

// Node mode. One server runs the master 3x-ui panel; every server built in
// a location with NodeMode on is registered there as a node (3x-ui's
// multi-node feature), over https with a certificate the master verifies.
//
// The master only watches the nodes (heartbeat, versions, load, node.up /
// node.down): they are registered with inbound sync "selected" and no tags,
// so the master never takes over their inbounds. The shared template is
// still applied to each panel directly, because every server carries its own
// client IDs under the same emails, which one master client table cannot
// hold.
//
// The "moves to another server by itself" part is the balancer configs:
// every working link of the healthy nodes of a balancer mode (some
// locations, or all of them) behind one xray balancer, built like the smart
// configs and kept current in vpn-pannel.

// With the node.only setting on, those (and the smart configs) are all that
// vpn-pannel holds: the servers' own configs are not uploaded.

// Job kinds of node mode.
const (
	KindNodeJoin  = "node_join"
	KindNodeLeave = "node_leave"
	KindNodeSync  = "node_sync"
	// KindNodeOnly applies a change of the node.only setting.
	KindNodeOnly = "node_only"
)

// NodeAPI is the node side of a master 3x-ui panel; *xui.Client has it.
type NodeAPI interface {
	ListNodes(ctx context.Context) ([]xui.Node, error)
	AddNode(ctx context.Context, n xui.Node) (int, error)
	UpdateNode(ctx context.Context, id int, n xui.Node) error
	DeleteNode(ctx context.Context, id int) error
	ProbeNode(ctx context.Context, id int) error
}

// tokenAPI mints scoped API tokens on a panel; *xui.Client has it.
type tokenAPI interface {
	CreateAPIToken(ctx context.Context, name, scope string) (string, error)
}

// ErrNoMaster means no master panel is set up yet.
var ErrNoMaster = errors.New("پنل مادر تعریف نشده است")

// MasterServer is the server that runs the master panel.
func (o *Orchestrator) MasterServer() (*models.Server, error) {
	id := o.Settings.Int(settings.NodeMasterID)
	if id <= 0 {
		return nil, ErrNoMaster
	}
	var s models.Server
	if err := o.DB.Preload("ProviderAccount").First(&s, id).Error; err != nil {
		return nil, ErrNoMaster
	}
	if s.Role != models.RoleMaster || s.Status == models.ServerDeleted || s.Status == models.ServerDeleting {
		return nil, ErrNoMaster
	}
	if s.XUIPort == 0 {
		return nil, errors.New("پنل مادر هنوز نصب نشده است")
	}
	return &s, nil
}

// defaultMaster reaches the master panel through SSH, like every panel.
func (o *Orchestrator) defaultMaster(ctx context.Context) (NodeAPI, func(), error) {
	m, err := o.MasterServer()
	if err != nil {
		return nil, nil, err
	}
	remote, _, err := o.Connect(ctx, m)
	if err != nil {
		return nil, nil, fmt.Errorf("master %s: ssh: %w", m.Name, err)
	}
	api, ok := o.PanelFor(m, remote).(NodeAPI)
	if !ok {
		remote.Close()
		return nil, nil, errors.New("the master panel client has no node API")
	}
	return api, func() { remote.Close() }, nil
}

// setMaster records s as the master panel's server.
func (o *Orchestrator) setMaster(s *models.Server) error {
	return o.Settings.Set(map[string]string{settings.NodeMasterID: strconv.FormatUint(uint64(s.ID), 10)})
}

// nodeAddress is where the master reaches the panel of s: the name its
// certificate is for.
func nodeAddress(s *models.Server) string {
	if s.SSL && s.Hostname != "" && !s.IPCert {
		return s.Hostname
	}
	return s.IPv4
}

// nodeJoin registers s on the master panel (or updates its registration):
// a certificate the master can verify, the panel open over https, a token
// of its own for the master, then the node itself.
func (o *Orchestrator) nodeJoin(ctx context.Context, r *jobs.Run, s *models.Server, remote Remote) (err error) {
	defer func() {
		note := ""
		if err != nil {
			note = truncate(err.Error(), 250)
		}
		if s.NodeError != note {
			s.NodeError = note
			o.save(r, s, "node_error")
		}
	}()
	if s.XUIPort == 0 {
		return errors.New("3x-ui is not installed on this server")
	}
	if _, err := o.MasterServer(); err != nil {
		return err
	}
	r.Step("join the master panel")
	if s.Role != models.RoleNode {
		s.Role = models.RoleNode
		o.save(r, s, "role")
	}
	if !(s.SSL && s.CFZoneName != "") {
		// A certificate for another address (after an IP change) or one
		// about to run out is issued again.
		fresh := s.IPCert && s.CertNotAfter != nil && time.Until(*s.CertNotAfter) > IPCertRenew &&
			s.XUIScheme == "https" && s.CertIP == s.IPv4
		if !fresh {
			if err := o.ensureIPCert(ctx, r, s, remote); err != nil {
				return err
			}
		}
	}
	if s.XUIScheme != "https" {
		return errors.New("the panel is not served over https; the master would not trust it")
	}
	if !s.XUIPublic {
		if err := o.setPanelListen(ctx, r, s, remote, true); err != nil {
			return err
		}
		if err := o.waitPanel(ctx, r, s, o.PanelFor(s, remote)); err != nil {
			return err
		}
	}
	mintToken := func() error {
		tp, ok := o.PanelFor(s, remote).(tokenAPI)
		if !ok {
			return errors.New("this panel client cannot create API tokens")
		}
		// "node-admin" (node-sync plus panel updates from the master) only
		// exists in newer 3x-ui; v3.7 knows admin, monitor and node-sync.
		tok, err := tp.CreateAPIToken(ctx, "microserver-master", "node-admin")
		if err != nil && strings.Contains(strings.ToLower(err.Error()), "scope") {
			tok, err = tp.CreateAPIToken(ctx, "microserver-master", "node-sync")
		}
		if err != nil {
			return fmt.Errorf("node token: %w", err)
		}
		s.NodeToken = secretString(tok)
		o.save(r, s, "node_token")
		return nil
	}
	if s.NodeToken == "" {
		if err := mintToken(); err != nil {
			return err
		}
	}

	master, done, err := o.Master(ctx)
	if err != nil {
		return err
	}
	defer done()
	verify := "verify"
	if o.Settings.Bool(settings.AcmeStaging) {
		// The staging CA is not trusted by anyone.
		verify = "skip"
	}
	tok := string(s.NodeToken)
	n := xui.Node{
		Name: s.Name, Remark: "microServer #" + strconv.FormatUint(uint64(s.ID), 10),
		Scheme: "https", Address: nodeAddress(s), Port: s.XUIPort, BasePath: "/" + strings.Trim(s.XUIBasePath, "/") + "/",
		APIToken: &tok, Enable: true, TLSVerifyMode: verify,
		// The master watches the node; its inbounds stay this panel's.
		InboundSyncMode: "selected", InboundTags: []string{},
	}
	if n.BasePath == "//" {
		n.BasePath = "/"
	}
	if s.NodeID == 0 {
		// A registration left over under this name is taken over.
		if nodes, lerr := master.ListNodes(ctx); lerr == nil {
			for _, x := range nodes {
				if x.Name == s.Name {
					s.NodeID = x.ID
				}
			}
		}
	}
	// register sends n to the master, which probes the node with the token
	// before it saves anything.
	register := func() error {
		if s.NodeID != 0 {
			if err := master.UpdateNode(ctx, s.NodeID, n); err != nil {
				return fmt.Errorf("master: update node: %w", err)
			}
			return nil
		}
		id, err := master.AddNode(ctx, n)
		if err != nil {
			return fmt.Errorf("master: add node: %w", err)
		}
		if id == 0 {
			nodes, err := master.ListNodes(ctx)
			if err != nil {
				return fmt.Errorf("master: list nodes: %w", err)
			}
			for _, x := range nodes {
				if x.Name == s.Name {
					id = x.ID
				}
			}
		}
		s.NodeID = id
		return nil
	}
	if err := register(); err != nil {
		if !nodeRejectedToken(err) {
			return err
		}
		// The node does not know the stored token (a reinstalled or rebuilt
		// panel answers an unknown token with 404): mint a new one, once.
		r.Logf("warn", "%v; minting a new node token", err)
		if err := mintToken(); err != nil {
			return err
		}
		tok = string(s.NodeToken)
		if err := register(); err != nil {
			return err
		}
	}
	o.save(r, s, "node_id")
	if s.NodeID != 0 {
		if err := master.ProbeNode(ctx, s.NodeID); err != nil {
			r.Logf("warn", "master: probe node: %v", err)
		}
	}
	r.Info("node %s is registered on the master panel at https://%s:%d", s.Name, n.Address, n.Port)
	return nil
}

// nodeRejectedToken reports whether the master could not get into the node
// with the token it was given: 3x-ui answers an unknown token with 404 (or
// 401/403), which the master passes on as "HTTP n from remote panel".
func nodeRejectedToken(err error) bool {
	msg := err.Error()
	if !strings.Contains(msg, "from remote panel") {
		return false
	}
	for _, code := range []string{"HTTP 401", "HTTP 403", "HTTP 404"} {
		if strings.Contains(msg, code) {
			return true
		}
	}
	return false
}

// nodeJoinJob is the node_join job of one server.
func (o *Orchestrator) nodeJoinJob(ctx context.Context, r *jobs.Run) error {
	s, err := o.loadServer(r)
	if err != nil {
		return err
	}
	if s.Role == models.RoleMaster {
		return errors.New("the master panel's server cannot be a node")
	}
	remote, _, err := o.Connect(ctx, s)
	if err != nil {
		return fmt.Errorf("ssh: %w", err)
	}
	defer remote.Close()
	// An inbound on port 80 would block the certificate: take it off first.
	if s.Role != models.RoleNode && !(s.SSL && s.CFZoneName != "") && o.holdsAcmePort(s) {
		s.Role = models.RoleNode
		o.save(r, s, "role")
		r.Step("free port 80")
		if err := o.applyWith(ctx, r, s, remote); err != nil {
			return err
		}
	}
	return o.nodeJoin(ctx, r, s, remote)
}

// nodeLeave takes s off the master panel. It only warns: the server may be
// gone already, and so may the master.
func (o *Orchestrator) nodeLeave(ctx context.Context, r *jobs.Run, s *models.Server) {
	if s.Role != models.RoleNode {
		return
	}
	if s.NodeID != 0 {
		master, done, err := o.Master(ctx)
		if err != nil {
			r.Logf("warn", "master: %v", err)
		} else {
			if err := master.DeleteNode(ctx, s.NodeID); err != nil {
				r.Logf("warn", "master: delete node %s: %v", s.Name, err)
			} else {
				r.Info("node %s removed from the master panel", s.Name)
			}
			done()
		}
	}
	s.Role, s.NodeID, s.NodeError = "", 0, ""
	o.save(r, s, "role", "node_id", "node_error")
}

// nodeLeaveJob is the node_leave job: the server stays, as an ordinary one.
func (o *Orchestrator) nodeLeaveJob(ctx context.Context, r *jobs.Run) error {
	s, err := o.loadServer(r)
	if err != nil {
		return err
	}
	r.Step("leave the master panel")
	o.nodeLeave(ctx, r, s)
	return nil
}

// JoinLocation queues node_join for every live server of a location that
// is not a node yet; it returns how many were queued.
func (o *Orchestrator) JoinLocation(locationID uint, source string) (int, error) {
	if _, err := o.MasterServer(); err != nil {
		return 0, err
	}
	var servers []models.Server
	o.DB.Where("location_id = ? AND status IN ? AND xui_port > 0 AND role = ?", locationID,
		[]string{models.ServerActive, models.ServerFiltered}, "").Find(&servers)
	n := 0
	for i := range servers {
		id := servers[i].ID
		if _, err := o.Engine.Enqueue(KindNodeJoin, &id, struct{}{}, source); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// LeaveLocation queues node_leave for every node of a location.
func (o *Orchestrator) LeaveLocation(locationID uint, source string) (int, error) {
	var servers []models.Server
	o.DB.Where("location_id = ? AND role = ? AND status <> ?", locationID, models.RoleNode, models.ServerDeleted).Find(&servers)
	n := 0
	for i := range servers {
		id := servers[i].ID
		if _, err := o.Engine.Enqueue(KindNodeLeave, &id, struct{}{}, source); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// NodeStatus is one node as the master panel sees it, with our server.
type NodeStatus struct {
	xui.Node
	ServerID uint `json:"server_id"`
}

// Nodes lists the master panel's nodes.
func (o *Orchestrator) Nodes(ctx context.Context) ([]NodeStatus, error) {
	master, done, err := o.Master(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	nodes, err := master.ListNodes(ctx)
	if err != nil {
		return nil, err
	}
	var servers []models.Server
	o.DB.Select("id", "node_id").Where("role = ? AND node_id > 0 AND status <> ?", models.RoleNode, models.ServerDeleted).Find(&servers)
	byNode := map[int]uint{}
	for _, s := range servers {
		byNode[s.NodeID] = s.ID
	}
	out := make([]NodeStatus, 0, len(nodes))
	for _, n := range nodes {
		n.APIToken = nil
		out = append(out, NodeStatus{Node: n, ServerID: byNode[n.ID]})
	}
	return out, nil
}

// ---- balancer configs ----

// nodeState debounces the triggers of node_sync into one job.
type nodeState struct {
	mu    sync.Mutex
	run   sync.Mutex
	timer *time.Timer
}

// QueueNodeSync schedules a node sync after SmartDelay when a location
// uses node mode or a balancer config is still in vpn-pannel.
func (o *Orchestrator) QueueNodeSync() {
	if o == nil || o.Engine == nil {
		return
	}
	var n int64
	o.DB.Model(&models.Location{}).Where("node_mode = ?", true).Count(&n)
	if n == 0 {
		o.DB.Model(&models.Server{}).Where("role = ? AND status <> ?", models.RoleNode, models.ServerDeleted).Count(&n)
	}
	if n == 0 {
		o.DB.Model(&models.NodeUpload{}).Count(&n)
		if n == 0 {
			return
		}
	}
	st := &o.nodes
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.timer != nil {
		return
	}
	st.timer = time.AfterFunc(SmartDelay, func() {
		st.mu.Lock()
		st.timer = nil
		st.mu.Unlock()
		var queued int64
		o.DB.Model(&models.Job{}).Where("kind = ? AND status = ?", KindNodeSync, models.JobQueued).Count(&queued)
		if queued > 0 {
			return
		}
		if _, err := o.Engine.Enqueue(KindNodeSync, nil, struct{}{}, "auto"); err != nil {
			o.notify("node sync: %v", err)
		}
	})
}

// BalancerName is the name the balancer mode seeded for a location gets.
func BalancerName(loc *models.Location) string {
	base := firstNonEmpty(loc.NamePrefix, strings.ToLower(loc.Country), "loc"+strconv.FormatUint(uint64(loc.ID), 10))
	return base + "-auto"
}

// AllNodesName is the name of the seeded balancer mode that holds every
// healthy node, whatever its location.
const AllNodesName = "all-auto"

// NodeProbeInterval is how often a balancer config tests its servers when
// its mode does not say: a client leaves a dead server within about this
// long.
const NodeProbeInterval = "30s"

// SeedBalancerModes makes, once, the balancer modes older versions had
// built in: one per node location and one of all nodes. Balancer configs
// already in vpn-pannel are attached to their mode, so nothing is pushed
// again. After that the list is the admin's.
func SeedBalancerModes(db *gorm.DB, st *settings.Store) error {
	if st.Bool(settings.NodeModesSeeded) {
		return nil
	}
	var n int64
	db.Model(&models.BalancerMode{}).Count(&n)
	if n == 0 {
		var locs []models.Location
		db.Where("node_mode = ?", true).Order("priority, id").Find(&locs)
		used := map[string]bool{AllNodesName: true}
		prio := 0
		add := func(m models.BalancerMode, locID uint) error {
			prio++
			m.Enabled, m.Priority = true, prio
			if err := db.Create(&m).Error; err != nil {
				return err
			}
			return db.Model(&models.NodeUpload{}).Where("mode_id = 0 AND location_id = ?", locID).Update("mode_id", m.ID).Error
		}
		for i := range locs {
			name := BalancerName(&locs[i])
			if used[name] {
				name = "loc" + strconv.FormatUint(uint64(locs[i].ID), 10) + "-auto"
			}
			used[name] = true
			m := models.BalancerMode{Name: name, LocationIDs: strconv.FormatUint(uint64(locs[i].ID), 10),
				Strategy: locs[i].BalancerStrategy, Country: strings.ToUpper(locs[i].Country)}
			if err := add(m, locs[i].ID); err != nil {
				return err
			}
		}
		if err := add(models.BalancerMode{Name: AllNodesName}, 0); err != nil {
			return err
		}
	}
	return st.Set(map[string]string{settings.NodeModesSeeded: "true"})
}

// ModeLocationIDs parses a mode's location list.
func ModeLocationIDs(v string) []uint {
	var out []uint
	seen := map[uint]bool{}
	for _, p := range strings.Split(v, ",") {
		n, err := strconv.ParseUint(strings.TrimSpace(p), 10, 64)
		if err != nil || n == 0 || seen[uint(n)] {
			continue
		}
		seen[uint(n)] = true
		out = append(out, uint(n))
	}
	return out
}

// nodeGroup is one balancer config: an enabled balancer mode.
type nodeGroup struct {
	ModeID   uint
	Name     string
	Country  string
	Strategy string
	Probe    string
	// Locations are the locations whose nodes it holds; none = every node.
	Locations []uint
	// Ads are the ads flags the mode sets itself; nil follows the vpn-pannel.
	Ads []bool
	// DropIdle and IdleMinutes are the mode's: leave idle inbounds out.
	DropIdle    bool
	IdleMinutes int
}

// nodeGroups lists the balancer configs wanted now: the enabled balancer
// modes, in their order. A mode of all nodes waits for the first node; it is
// what keeps a client connected when its server's whole location is gone,
// and the only one servers without a location (joined by hand) are in.
// A mode left out here has its config taken out of vpn-pannel.
func (o *Orchestrator) nodeGroups() []nodeGroup {
	var modes []models.BalancerMode
	o.DB.Where("enabled = ?", true).Order("priority, id").Find(&modes)
	if len(modes) == 0 {
		return nil
	}
	var first models.Server
	anyNode := o.DB.Where("role = ? AND status NOT IN ?", models.RoleNode, []string{models.ServerDeleted, models.ServerDeleting}).
		Order("id").First(&first).Error == nil
	var out []nodeGroup
	for _, m := range modes {
		g := nodeGroup{ModeID: m.ID, Name: m.Name, Country: m.Country, Strategy: m.Strategy, Probe: m.ProbeInterval,
			Locations: ModeLocationIDs(m.LocationIDs), Ads: m.AdsValues(), DropIdle: m.DropIdle, IdleMinutes: m.IdleMinutes}
		if len(g.Locations) == 0 {
			if !anyNode {
				continue
			}
			if g.Country == "" {
				g.Country = first.Country
			}
		} else {
			// Like the all-nodes mode, it waits until one of its locations
			// uses node mode or still has a node.
			var n int64
			o.DB.Model(&models.Location{}).Where("id IN ? AND node_mode = ?", g.Locations, true).Count(&n)
			if n == 0 {
				o.DB.Model(&models.Server{}).Where("location_id IN ? AND role = ? AND status NOT IN ?", g.Locations,
					models.RoleNode, []string{models.ServerDeleted, models.ServerDeleting}).Count(&n)
			}
			if n == 0 {
				continue
			}
			if g.Country == "" {
				var loc models.Location
				if o.DB.Select("country").Where("id IN ?", g.Locations).Order("priority, id").First(&loc).Error == nil {
					g.Country = loc.Country
				}
			}
		}
		out = append(out, g)
	}
	return out
}

// nodeProfile is how a group's balancer config is built.
func nodeProfile(g nodeGroup) models.SmartProfile {
	prof := models.SmartProfile{
		Name: g.Name, Enabled: true, Scope: models.SmartScopeAll, Country: g.Country,
		Strategy: g.Strategy, IranDirect: true, ProbeInterval: firstNonEmpty(g.Probe, NodeProbeInterval),
	}
	smart.Normalize(&prof)
	return prof
}

// NodeConfig is one balancer config as a client runs it.
type NodeConfig struct {
	Name      string
	JSON      string
	Outbounds int
}

// NodeConfigs builds the balancer configs wanted now (the ones node_sync
// puts in vpn-pannel), for the app's own subscription. A group without a
// healthy node is left out.
func (o *Orchestrator) NodeConfigs() ([]NodeConfig, error) {
	snap, err := LoadSnapshot(o.DB)
	if err != nil {
		return nil, err
	}
	var out []NodeConfig
	for _, g := range o.nodeGroups() {
		items, off := o.nodeBalancer(snap, g)
		if off != "" {
			continue
		}
		res, err := smart.Build(nodeProfile(g), nil, items, g.Name)
		if err != nil {
			continue
		}
		out = append(out, NodeConfig{Name: g.Name, JSON: res.JSON, Outbounds: res.Outbounds})
	}
	return out, nil
}

// nodeBalancer collects a group's proxies: the working links of its
// healthy nodes. off says why there are none.
func (o *Orchestrator) nodeBalancer(snap *Snapshot, g nodeGroup) (items []smart.Item, off string) {
	q := o.DB.Where("role = ? AND status = ?", models.RoleNode, models.ServerActive)
	if len(g.Locations) > 0 {
		q = q.Where("location_id IN ?", g.Locations)
	}
	var servers []models.Server
	q.Order("id").Find(&servers)
	idle := o.idleRuleFor(g.DropIdle, g.IdleMinutes)
	for i := range servers {
		s := &servers[i]
		if s.PanelRemovedAt != nil || s.UploadedAt == nil {
			continue
		}
		if s.LastCheckOK != nil && !*s.LastCheckOK {
			continue
		}
		it, _ := o.smartItems(snap, s, nil, idle)
		items = append(items, it...)
	}
	if len(items) == 0 {
		off = "no healthy node with working configs"
	}
	return items, off
}

// nodeSync is the node_sync job: the balancer configs of nodeGroups in
// every vpn-pannel, rebuilt from the nodes that work right now.
func (o *Orchestrator) nodeSync(ctx context.Context, r *jobs.Run) error {
	o.nodes.run.Lock()
	defer o.nodes.run.Unlock()

	r.Step("build balancer configs")
	snap, err := LoadSnapshot(o.DB)
	if err != nil {
		return err
	}
	groups := o.nodeGroups()
	dests := o.Dests()

	type key struct {
		Mode, Dest uint
		Ads        bool
	}
	var rows []models.NodeUpload
	o.DB.Find(&rows)
	have := map[key]*models.NodeUpload{}
	var gone []*models.NodeUpload
	for i := range rows {
		x := &rows[i]
		if x.ModeID == 0 {
			// A row of an older version that no mode took over.
			gone = append(gone, x)
			continue
		}
		have[key{x.ModeID, x.DestinationID, x.Ads}] = x
	}
	destByID := map[uint]NamedDest{}
	for _, d := range dests {
		destByID[d.ID] = d
	}

	wanted, changed, failed := 0, 0, 0
	for _, g := range groups {
		loc := g
		items, off := o.nodeBalancer(snap, g)
		prof := nodeProfile(g)
		for _, d := range dests {
			if _, ok := d.Destination.(SmartDestination); !ok {
				continue
			}
			ads := d.Ads
			if len(loc.Ads) > 0 {
				ads = loc.Ads
			}
			if len(ads) == 0 {
				ads = []bool{false}
			}
			for _, a := range ads {
				k := key{loc.ModeID, d.ID, a}
				w := smartWant{
					key:  smartKey{Server: loc.ModeID, Dest: d.ID, Ads: a},
					name: AdsBaseName(prof.Name, a, len(ads) > 1), country: strings.ToUpper(loc.Country), off: off, dest: d,
				}
				if w.off == "" {
					res, err := smart.Build(prof, nil, items, w.name)
					if err != nil {
						w.off = err.Error()
					} else {
						w.json, w.outbounds = res.JSON, res.Outbounds
					}
				}
				row := have[k]
				delete(have, k)
				if row == nil {
					row = &models.NodeUpload{ModeID: loc.ModeID, DestinationID: d.ID, Ads: a}
				}
				wanted++
				// pushSmart works on a smart upload row; a balancer config
				// is the same kind of node in vpn-pannel.
				tmp := models.SmartUpload{Name: row.Name, PanelNodeID: row.PanelNodeID, Hash: row.Hash,
					Outbounds: row.Outbounds, Active: row.Active, PushedAt: row.PushedAt}
				did, err := o.pushSmart(ctx, r, w, &tmp, false)
				if did {
					changed++
				}
				row.Name, row.PanelNodeID, row.Hash = tmp.Name, tmp.PanelNodeID, tmp.Hash
				row.Outbounds, row.Active, row.PushedAt = tmp.Outbounds, tmp.Active, tmp.PushedAt
				row.Error = w.off
				if err != nil {
					failed++
					row.Error = truncate(err.Error(), 500)
					r.Logf("warn", "%s (%s): %v", w.name, d.Name, err)
				}
				o.DB.Save(row)
			}
		}
	}

	// Groups that are gone (a mode was removed or switched off, the last
	// node was deleted): their balancer configs go.
	for _, row := range have {
		gone = append(gone, row)
	}
	for _, row := range gone {
		d, ok := destByID[row.DestinationID]
		if sd, isSmart := d.Destination.(SmartDestination); ok && isSmart && row.Name != "" {
			n, err := sd.FindByName(ctx, row.Name)
			if err == nil && n.ID != 0 {
				err = sd.DeleteNode(ctx, n.ID)
			}
			if err != nil {
				failed++
				row.Error = truncate(err.Error(), 500)
				o.DB.Save(row)
				r.Logf("warn", "%s (%s): delete: %v", row.Name, d.Name, err)
				continue
			}
			r.Info("%s: removed %s", d.Name, row.Name)
		}
		o.DB.Delete(row)
	}
	r.Info("balancer configs: %d wanted, %d pushed or changed, %d failed", wanted, changed, failed)
	if failed > 0 && changed == 0 && failed == wanted {
		return errors.New("every balancer config push failed")
	}
	return nil
}

// ---- node only ----

// NodeOnly reports whether vpn-pannel holds only balancer and smart
// configs. A server's own configs are then not uploaded; what the upload
// would have recorded (the address, the working inbounds) still is, because
// the balancer and smart configs are built from it.
func (o *Orchestrator) NodeOnly() bool {
	return o.Settings != nil && o.Settings.Bool(settings.NodeOnly)
}

// nodeOnly is the node_only job, run when the setting changes. Switched on,
// every server's own configs leave vpn-pannel; switched off, they go up
// again.
func (o *Orchestrator) nodeOnly(ctx context.Context, r *jobs.Run) error {
	var servers []models.Server
	o.DB.Where("uploaded_at IS NOT NULL AND panel_removed_at IS NULL AND status NOT IN ?",
		[]string{models.ServerDeleted, models.ServerDeleting}).Order("id").Find(&servers)
	if !o.NodeOnly() {
		r.Step("upload the servers' configs again")
		n := 0
		for i := range servers {
			s := &servers[i]
			if s.Status != models.ServerActive && s.Status != models.ServerFiltered {
				continue
			}
			id := s.ID
			if _, err := o.Engine.Enqueue(KindUpload, &id, struct{}{}, r.Job.Source); err != nil {
				r.Logf("warn", "%s: %v", s.Name, err)
				continue
			}
			n++
		}
		r.Info("queued upload for %d servers", n)
		return nil
	}
	r.Step("remove the servers' configs from vpn-pannel")
	for i := range servers {
		s := &servers[i]
		// Not deleteOnPanels: that keeps an address a live server uses,
		// and here the live server is the one being cleared.
		for _, addr := range panelAddrs(s) {
			for _, d := range o.Dests() {
				if err := d.DeleteAddress(ctx, addr); err != nil {
					r.Logf("warn", "%s: could not delete configs on %s: %v", d.Name, addr, err)
					continue
				}
				r.Info("%s: configs on %s deleted", d.Name, addr)
			}
		}
		if s.CDNHostname != "" {
			o.retireCleanConfigs(ctx, r, s.CDNHostname, true)
		}
	}
	r.Info("cleared %d servers; balancer and smart configs stay", len(servers))
	return nil
}
