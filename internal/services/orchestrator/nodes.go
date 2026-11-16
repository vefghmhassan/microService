package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

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
// The "moves to another server by itself" part is a balancer config per
// node location: every working link of the location's healthy nodes behind
// one xray balancer, built like the smart configs and kept current in
// vpn-pannel.

// Job kinds of node mode.
const (
	KindNodeJoin  = "node_join"
	KindNodeLeave = "node_leave"
	KindNodeSync  = "node_sync"
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
	if s.NodeToken == "" {
		tp, ok := o.PanelFor(s, remote).(tokenAPI)
		if !ok {
			return errors.New("this panel client cannot create API tokens")
		}
		tok, err := tp.CreateAPIToken(ctx, "microserver-master", "node-admin")
		if err != nil {
			return fmt.Errorf("node token: %w", err)
		}
		s.NodeToken = secretString(tok)
		o.save(r, s, "node_token")
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
	if s.NodeID != 0 {
		if err := master.UpdateNode(ctx, s.NodeID, n); err != nil {
			return fmt.Errorf("master: update node: %w", err)
		}
	} else {
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

// BalancerName is the vpn-pannel name of a location's balancer config.
func BalancerName(loc *models.Location) string {
	base := firstNonEmpty(loc.NamePrefix, strings.ToLower(loc.Country), "loc"+strconv.FormatUint(uint64(loc.ID), 10))
	return base + "-auto"
}

// nodeBalancer builds the balancer config of one location: the working
// links of its healthy nodes. off says why there is none.
func (o *Orchestrator) nodeBalancer(snap *Snapshot, loc *models.Location) (items []smart.Item, off string) {
	var servers []models.Server
	o.DB.Where("location_id = ? AND role = ? AND status = ?", loc.ID, models.RoleNode, models.ServerActive).Order("id").Find(&servers)
	for i := range servers {
		s := &servers[i]
		if s.PanelRemovedAt != nil || s.UploadedAt == nil || s.UploadedInbounds == "" {
			continue
		}
		if s.LastCheckOK != nil && !*s.LastCheckOK {
			continue
		}
		it, _ := o.smartItems(snap, s, nil)
		items = append(items, it...)
	}
	if len(items) == 0 {
		off = "no healthy node with working configs"
	}
	return items, off
}

// nodeSync is the node_sync job: one balancer config per node location in
// every vpn-pannel, rebuilt from the nodes that work right now.
func (o *Orchestrator) nodeSync(ctx context.Context, r *jobs.Run) error {
	o.nodes.run.Lock()
	defer o.nodes.run.Unlock()

	r.Step("build balancer configs")
	snap, err := LoadSnapshot(o.DB)
	if err != nil {
		return err
	}
	var locs []models.Location
	o.DB.Where("node_mode = ?", true).Order("priority, id").Find(&locs)
	dests := o.Dests()

	type key struct {
		Loc, Dest uint
		Ads       bool
	}
	var rows []models.NodeUpload
	o.DB.Find(&rows)
	have := map[key]*models.NodeUpload{}
	for i := range rows {
		x := &rows[i]
		have[key{x.LocationID, x.DestinationID, x.Ads}] = x
	}
	destByID := map[uint]NamedDest{}
	for _, d := range dests {
		destByID[d.ID] = d
	}

	wanted, changed, failed := 0, 0, 0
	for i := range locs {
		loc := &locs[i]
		items, off := o.nodeBalancer(snap, loc)
		prof := models.SmartProfile{
			Name: BalancerName(loc), Enabled: true, Scope: models.SmartScopeAll, Country: loc.Country,
			Strategy: loc.BalancerStrategy, IranDirect: true,
		}
		smart.Normalize(&prof)
		for _, d := range dests {
			if _, ok := d.Destination.(SmartDestination); !ok {
				continue
			}
			ads := d.Ads
			if len(ads) == 0 {
				ads = []bool{false}
			}
			for _, a := range ads {
				k := key{loc.ID, d.ID, a}
				w := smartWant{
					key:  smartKey{Server: loc.ID, Dest: d.ID, Ads: a},
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
					row = &models.NodeUpload{LocationID: loc.ID, DestinationID: d.ID, Ads: a}
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

	// Locations that left node mode: their balancer configs go.
	for _, row := range have {
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
