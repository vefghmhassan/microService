package orchestrator

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"microserver/internal/models"
	"microserver/internal/services/settings"
	"microserver/internal/services/xui"
)

// fakeMaster is the node side of a master 3x-ui panel.
type fakeMaster struct {
	mu     sync.Mutex
	next   int
	nodes  map[int]xui.Node
	probes int
}

func (m *fakeMaster) ListNodes(context.Context) ([]xui.Node, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []xui.Node
	for _, n := range m.nodes {
		out = append(out, n)
	}
	return out, nil
}
func (m *fakeMaster) AddNode(_ context.Context, n xui.Node) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.next++
	n.ID = m.next
	m.nodes[n.ID] = n
	return n.ID, nil
}
func (m *fakeMaster) UpdateNode(_ context.Context, id int, n xui.Node) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	n.ID = id
	m.nodes[id] = n
	return nil
}
func (m *fakeMaster) DeleteNode(_ context.Context, id int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.nodes, id)
	return nil
}
func (m *fakeMaster) ProbeNode(context.Context, int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.probes++
	return nil
}

// The fake panel can mint API tokens, like *xui.Client.
// It is a v3.7 panel: the node-admin scope does not exist there yet.
func (p *fakePanel) CreateAPIToken(_ context.Context, name, scope string) (string, error) {
	if scope == "node-admin" {
		return "", errors.New("3x-ui POST /panel/api/setting/apiTokens/create: The parameters have been changed. (scope must be 'admin', 'monitor', or 'node-sync'\n)")
	}
	return "tok-" + scope, nil
}

// withMaster gives o a master panel and a location in node mode.
func withMaster(t *testing.T, o *Orchestrator, acc *models.ProviderAccount) (*fakeMaster, *models.Location) {
	t.Helper()
	m := &fakeMaster{nodes: map[int]xui.Node{}}
	o.Master = func(context.Context) (NodeAPI, func(), error) { return m, func() {}, nil }
	master := models.Server{Name: "ms-master", ProviderAccountID: acc.ID, Role: models.RoleMaster,
		Status: models.ServerActive, IPv4: "10.9.9.9", XUIPort: 2053}
	o.DB.Create(&master)
	if err := o.setMaster(&master); err != nil {
		t.Fatal(err)
	}
	loc := models.Location{ProviderAccountID: acc.ID, Region: "fsn1", Plan: "cx22", Country: "DE",
		Enabled: true, NodeMode: true, NamePrefix: "de"}
	o.DB.Create(&loc)
	return m, &loc
}

func TestIPCertScript(t *testing.T) {
	s := IPCertScript("203.0.113.7", "a@b.c", false)
	for _, want := range []string{
		"--issue -d '203.0.113.7' --standalone --server letsencrypt --certificate-profile shortlived --days 6 --httpport 80",
		"--fullchain-file " + IPCertFile, "--key-file " + IPKeyFile, "NOTAFTER=",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("script lacks %q:\n%s", want, s)
		}
	}
	if !strings.Contains(s, "| sh -s email='a@b.c' >") {
		t.Errorf("the contact is not passed to the acme.sh installer:\n%s", s)
	}
	noMail := IPCertScript("203.0.113.7", "", true)
	if !strings.Contains(noMail, "--server letsencrypt_test") {
		t.Error("staging does not use the test CA")
	}
	// A bare "email=" breaks the acme.sh installer.
	if strings.Contains(noMail, "email=") || strings.Contains(noMail, "--register-account") {
		t.Errorf("no contact must mean no email argument:\n%s", noMail)
	}
	got, ok := parseNotAfter("x\nNOTAFTER=Nov 22 10:20:30 2026 GMT\n")
	if !ok || !got.Equal(time.Date(2026, 11, 22, 10, 20, 30, 0, time.UTC)) {
		t.Errorf("parseNotAfter = %v, %v", got, ok)
	}
	if got, ok = parseNotAfter("NOTAFTER=Nov  2 10:20:30 2026 GMT"); !ok || got.Day() != 2 {
		t.Errorf("single-digit day: %v, %v", got, ok)
	}
}

func TestIPCertServerKeepsPort80Free(t *testing.T) {
	o, _, _, _, _, _ := setup(t)
	o.DB.Create(&models.InboundTemplate{Remark: "plain", Protocol: "vless", Port: 80, Enabled: true,
		Settings: `{"clients":[{"id":"11111111-2222-4333-8444-555555555556","email":"u2"}],"decryption":"none"}`})
	snap, err := LoadSnapshot(o.DB)
	if err != nil {
		t.Fatal(err)
	}
	ports := func(s *models.Server) map[int]bool {
		out := map[int]bool{}
		for _, in := range snap.For(s).Inbounds {
			out[in.Port] = true
		}
		return out
	}
	if p := ports(&models.Server{}); !p[80] || !p[443] {
		t.Fatalf("an ordinary server gets both inbounds, got %v", p)
	}
	if p := ports(&models.Server{Role: models.RoleNode}); p[80] || !p[443] {
		t.Fatalf("a node must leave port 80 free, got %v", p)
	}
	// A node with its zone certificate needs no IP certificate.
	withZone := &models.Server{Role: models.RoleNode, DNS: models.DNS{SSL: true, CFZoneName: "example.com"}}
	if p := ports(withZone); !p[80] {
		t.Fatalf("a node with a zone certificate keeps port 80, got %v", p)
	}
}

func TestProvisionInNodeLocationJoinsMaster(t *testing.T) {
	o, _, _, _, acc, _ := setup(t)
	m, loc := withMaster(t, o, acc)
	j, _ := o.Engine.Enqueue(KindProvisionAuto, nil, AutoParams{Count: 1}, "test")
	if got := waitJob(t, o, j.ID); got.Status != models.JobDone {
		t.Fatalf("auto %s: %s", got.Status, got.Error)
	}
	var s models.Server
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if o.DB.Where("location_id = ?", loc.ID).First(&s).Error == nil && s.Status == models.ServerActive {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if s.Status != models.ServerActive {
		t.Fatalf("server not active: %+v", s.Status)
	}
	if s.Role != models.RoleNode || s.NodeID == 0 || s.NodeError != "" {
		t.Fatalf("role %q node %d error %q", s.Role, s.NodeID, s.NodeError)
	}
	if !s.IPCert || s.CertNotAfter == nil || s.XUIScheme != "https" || !s.XUIPublic || s.CertIP != s.IPv4 {
		t.Fatalf("ip cert %v until %v scheme %s public %v", s.IPCert, s.CertNotAfter, s.XUIScheme, s.XUIPublic)
	}
	n := m.nodes[s.NodeID]
	if n.Scheme != "https" || n.Address != s.IPv4 || n.Port != s.XUIPort || n.TLSVerifyMode != "verify" ||
		n.InboundSyncMode != "selected" || len(n.InboundTags) != 0 || n.APIToken == nil || *n.APIToken != "tok-node-sync" {
		t.Fatalf("node on master: %+v", n)
	}

	// Deleting the server takes it off the master.
	d, _ := o.DeleteServer(s.ID, "test")
	if got := waitJob(t, o, d.ID); got.Status != models.JobDone {
		t.Fatalf("delete %s: %s", got.Status, got.Error)
	}
	if len(m.nodes) != 0 {
		t.Fatalf("node still on master: %+v", m.nodes)
	}
}

func TestJoinLocationConvertsExistingServersAndBalancerFollows(t *testing.T) {
	o, _, _, dest, acc, _ := setup(t)
	sd := &smartDest{fakeDest: dest, nodes: map[string]*smartNode{}}
	o.Dests = func() []NamedDest { return []NamedDest{{ID: 1, Name: "test", Destination: sd}} }
	// Two ordinary servers first.
	var ids []uint
	for i := 0; i < 2; i++ {
		j, _ := o.Engine.Enqueue(KindProvision, nil, ProvisionParams{AccountID: acc.ID}, "test")
		got := waitJob(t, o, j.ID)
		if got.Status != models.JobDone {
			t.Fatalf("provision %s: %s", got.Status, got.Error)
		}
		ids = append(ids, *got.ServerID)
	}
	m, loc := withMaster(t, o, acc)
	o.DB.Model(&models.Server{}).Where("id IN ?", ids).Update("location_id", loc.ID)

	n, err := o.JoinLocation(loc.ID, "test")
	if err != nil || n != 2 {
		t.Fatalf("JoinLocation = %d, %v", n, err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && len(m.nodes) < 2 {
		time.Sleep(20 * time.Millisecond)
	}
	if len(m.nodes) != 2 {
		t.Fatalf("nodes on master: %d", len(m.nodes))
	}
	time.Sleep(50 * time.Millisecond)

	// A server that got its configs by a move from a replaced one (so it
	// has no recorded set of uploaded inbounds) is still in the balancer.
	o.DB.Model(&models.Server{}).Where("id = ?", ids[1]).Update("uploaded_inbounds", "")

	sync := func() {
		t.Helper()
		j, _ := o.Engine.Enqueue(KindNodeSync, nil, struct{}{}, "test")
		if got := waitJob(t, o, j.ID); got.Status != models.JobDone {
			t.Fatalf("node sync %s: %s", got.Status, got.Error)
		}
	}
	sync()
	node := sd.nodes["de-auto"]
	if node == nil || !node.active {
		t.Fatalf("no balancer config in vpn-pannel: %+v", sd.nodes)
	}
	outbounds := func() int {
		var row models.NodeUpload
		o.DB.Where("location_id = ?", loc.ID).First(&row)
		return row.Outbounds
	}
	if got := outbounds(); got != 2 {
		t.Fatalf("balancer holds %d proxies, want 2", got)
	}
	if !strings.Contains(node.json, "leastPing") {
		t.Fatal("balancer strategy is not leastPing")
	}

	// Every node is also in the all-nodes config, with or without a location.
	allOut := func() int {
		var row models.NodeUpload
		o.DB.Where("location_id = 0").First(&row)
		return row.Outbounds
	}
	if all := sd.nodes[AllNodesName]; all == nil || !all.active || allOut() != 2 {
		t.Fatalf("all-nodes config: %+v with %d proxies, want 2", all, allOut())
	}
	if !strings.Contains(sd.nodes[AllNodesName].json, `"probeInterval":"`+NodeProbeInterval+`"`) {
		t.Fatal("the all-nodes config does not probe its servers every " + NodeProbeInterval)
	}
	// A node that was joined by hand has no location: only all-auto has it.
	j3, _ := o.Engine.Enqueue(KindProvision, nil, ProvisionParams{AccountID: acc.ID}, "test")
	got3 := waitJob(t, o, j3.ID)
	if got3.Status != models.JobDone {
		t.Fatalf("provision %s: %s", got3.Status, got3.Error)
	}
	jj, _ := o.Engine.Enqueue(KindNodeJoin, got3.ServerID, struct{}{}, "test")
	if got := waitJob(t, o, jj.ID); got.Status != models.JobDone {
		t.Fatalf("node join %s: %s", got.Status, got.Error)
	}
	sync()
	if outbounds() != 2 || allOut() != 3 {
		t.Fatalf("location config %d proxies (want 2), all-nodes %d (want 3)", outbounds(), allOut())
	}

	// A filtered node drops out of the balancer.
	no := false
	o.DB.Model(&models.Server{}).Where("id = ?", ids[0]).Updates(map[string]any{"status": models.ServerFiltered, "last_check_ok": &no})
	sync()
	if got := outbounds(); got != 1 {
		t.Fatalf("balancer holds %d proxies after one node was filtered, want 1", got)
	}
	if got := allOut(); got != 2 {
		t.Fatalf("all-nodes config holds %d proxies after one node was filtered, want 2", got)
	}
	// The hand-joined node is deleted: the clients go on with the rest.
	d3, _ := o.DeleteServer(*got3.ServerID, "test")
	if got := waitJob(t, o, d3.ID); got.Status != models.JobDone {
		t.Fatalf("delete %s: %s", got.Status, got.Error)
	}
	sync()
	if got := allOut(); got != 1 || !sd.nodes[AllNodesName].active {
		t.Fatalf("all-nodes config holds %d proxies after a node was deleted, want 1", got)
	}

	// Node mode off: the servers leave the master and the config goes.
	o.DB.Model(loc).Update("node_mode", false)
	if n, _ := o.LeaveLocation(loc.ID, "test"); n != 2 {
		t.Fatalf("LeaveLocation = %d", n)
	}
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && len(m.nodes) > 0 {
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	sync()
	if len(m.nodes) != 0 || len(sd.nodes) != 0 {
		t.Fatalf("left behind: master %d, vpn-pannel %d", len(m.nodes), len(sd.nodes))
	}
	if o.Settings.Int(settings.NodeMasterID) == 0 {
		t.Fatal("master setting lost")
	}
}

func TestProvisionMaster(t *testing.T) {
	o, _, panel, dest, acc, _ := setup(t)
	j, _ := o.Engine.Enqueue(KindProvision, nil, ProvisionParams{AccountID: acc.ID, Role: models.RoleMaster, Name: "ms-master"}, "test")
	got := waitJob(t, o, j.ID)
	if got.Status != models.JobDone {
		t.Fatalf("provision %s: %s", got.Status, got.Error)
	}
	m, err := o.MasterServer()
	if err != nil || m.ID != *got.ServerID || m.Status != models.ServerActive {
		t.Fatalf("master: %+v, %v", m, err)
	}
	if len(panel.inbounds) != 0 || len(dest.uploads) != 0 || m.XUIPublic {
		t.Fatalf("master got inbounds %d, uploads %d, public %v", len(panel.inbounds), len(dest.uploads), m.XUIPublic)
	}
}
