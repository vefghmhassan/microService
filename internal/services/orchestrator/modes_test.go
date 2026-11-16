package orchestrator

import (
	"strings"
	"testing"
	"time"

	"microserver/internal/models"
	"microserver/internal/services/settings"
)

// modeNodes provisions n servers in a node location and joins them.
func modeNodes(t *testing.T, o *Orchestrator, acc *models.ProviderAccount, loc *models.Location, m *fakeMaster, n int) []uint {
	t.Helper()
	var ids []uint
	for i := 0; i < n; i++ {
		j, _ := o.Engine.Enqueue(KindProvision, nil, ProvisionParams{AccountID: acc.ID}, "test")
		got := waitJob(t, o, j.ID)
		if got.Status != models.JobDone {
			t.Fatalf("provision %s: %s", got.Status, got.Error)
		}
		ids = append(ids, *got.ServerID)
	}
	o.DB.Model(&models.Server{}).Where("id IN ?", ids).Update("location_id", loc.ID)
	before := len(m.nodes)
	if _, err := o.JoinLocation(loc.ID, "test"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && len(m.nodes) < before+n {
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	return ids
}

func runJob(t *testing.T, o *Orchestrator, kind string) {
	t.Helper()
	j, _ := o.Engine.Enqueue(kind, nil, struct{}{}, "test")
	if got := waitJob(t, o, j.ID); got.Status != models.JobDone {
		t.Fatalf("%s %s: %s", kind, got.Status, got.Error)
	}
}

func TestSeedBalancerModesKeepsExistingUploads(t *testing.T) {
	o, _, _, _, acc, _ := setup(t)
	_, loc := withMaster(t, o, acc)
	// Rows of an older version: keyed by location, 0 for all nodes.
	o.DB.Create(&models.NodeUpload{LocationID: loc.ID, DestinationID: 1, Name: "de-auto", PanelNodeID: 7})
	o.DB.Create(&models.NodeUpload{LocationID: 0, DestinationID: 1, Name: AllNodesName, PanelNodeID: 8})

	if err := SeedBalancerModes(o.DB, o.Settings); err != nil {
		t.Fatal(err)
	}
	var modes []models.BalancerMode
	o.DB.Order("priority").Find(&modes)
	if len(modes) != 2 || modes[0].Name != "de-auto" || modes[1].Name != AllNodesName || modes[1].LocationIDs != "" {
		t.Fatalf("seeded modes: %+v", modes)
	}
	for _, m := range modes {
		var row models.NodeUpload
		if o.DB.Where("mode_id = ?", m.ID).First(&row).Error != nil || row.Name != m.Name {
			t.Fatalf("%s: its config in vpn-pannel was not attached: %+v", m.Name, row)
		}
	}
	// Once only: a list the admin emptied stays empty.
	o.DB.Where("1 = 1").Delete(&models.BalancerMode{})
	if err := SeedBalancerModes(o.DB, o.Settings); err != nil {
		t.Fatal(err)
	}
	var n int64
	o.DB.Model(&models.BalancerMode{}).Count(&n)
	if n != 0 {
		t.Fatalf("seeded again: %d modes", n)
	}
}

func TestBalancerModesDecideTheConfigs(t *testing.T) {
	o, _, _, dest, acc, _ := setup(t)
	sd := &smartDest{fakeDest: dest, nodes: map[string]*smartNode{}}
	o.Dests = func() []NamedDest { return []NamedDest{{ID: 1, Name: "test", Destination: sd}} }
	m, loc := withMaster(t, o, acc)
	modeNodes(t, o, acc, loc, m, 2)

	// No mode: nothing is made by itself any more.
	runJob(t, o, KindNodeSync)
	if len(sd.nodes) != 0 {
		t.Fatalf("configs without a mode: %+v", sd.nodes)
	}

	// A second location with one node; a mode of both, one of the first.
	loc2 := models.Location{ProviderAccountID: acc.ID, Region: "hel1", Plan: "cx22", Country: "FI", Enabled: true, NodeMode: true}
	o.DB.Create(&loc2)
	modeNodes(t, o, acc, &loc2, m, 1)
	ids := func(l ...*models.Location) string {
		var p []string
		for _, x := range l {
			p = append(p, jsonID(x.ID))
		}
		return strings.Join(p, ",")
	}
	both := models.BalancerMode{Name: "eu", Enabled: true, Priority: 2, LocationIDs: ids(loc, &loc2),
		Strategy: "random", ProbeInterval: "1m", AdsMode: models.AdsBoth}
	one := models.BalancerMode{Name: "only-de", Enabled: true, Priority: 1, LocationIDs: ids(loc)}
	o.DB.Create(&both)
	o.DB.Create(&one)
	runJob(t, o, KindNodeSync)

	out := func(name string) int {
		var row models.NodeUpload
		o.DB.Where("name = ?", name).First(&row)
		return row.Outbounds
	}
	if out("only-de") != 2 || out("eu") != 3 || out("eu-ads") != 3 {
		t.Fatalf("proxies: only-de %d (want 2), eu %d and eu-ads %d (want 3)", out("only-de"), out("eu"), out("eu-ads"))
	}
	eu := sd.nodes["eu"]
	if eu == nil || !strings.Contains(eu.json, "random") || !strings.Contains(eu.json, `"probeInterval":"1m"`) {
		t.Fatalf("eu does not carry its strategy and probe interval: %+v", eu)
	}
	if ads := sd.nodes["eu-ads"]; ads == nil || !ads.ads || sd.nodes["eu"].ads {
		t.Fatalf("the mode's own ads setting was not used: %+v", sd.nodes)
	}
	if sd.nodes["only-de-ads"] != nil {
		t.Fatal("a mode without an ads setting got an ads copy")
	}
	// The subscription follows the same list, in its order.
	nc, err := o.NodeConfigs()
	if err != nil || len(nc) != 2 || nc[0].Name != "only-de" || nc[1].Name != "eu" {
		t.Fatalf("NodeConfigs: %+v, %v", nc, err)
	}

	// Switched off or deleted: the configs leave vpn-pannel.
	o.DB.Model(&both).Update("enabled", false)
	o.DB.Delete(&one)
	runJob(t, o, KindNodeSync)
	if len(sd.nodes) != 0 {
		t.Fatalf("left in vpn-pannel: %+v", sd.nodes)
	}
	if nc, _ := o.NodeConfigs(); len(nc) != 0 {
		t.Fatalf("left in the subscription: %+v", nc)
	}
}

func TestNodeOnlyKeepsServerConfigsOut(t *testing.T) {
	o, _, _, dest, acc, _ := setup(t)
	sd := &smartDest{fakeDest: dest, nodes: map[string]*smartNode{}}
	o.Dests = func() []NamedDest { return []NamedDest{{ID: 1, Name: "test", Destination: sd}} }
	m, loc := withMaster(t, o, acc)
	ids := modeNodes(t, o, acc, loc, m, 2)
	if err := SeedBalancerModes(o.DB, o.Settings); err != nil {
		t.Fatal(err)
	}
	var first models.Server
	o.DB.First(&first, ids[0])
	if first.UploadedIP == "" {
		t.Fatal("the server was not uploaded before the switch")
	}

	// On: every server's own configs go, the balancer keeps its proxies.
	o.Settings.Set(map[string]string{settings.NodeOnly: "true"})
	runJob(t, o, KindNodeOnly)
	removed := map[string]bool{}
	dest.mu.Lock()
	for _, a := range dest.removed {
		removed[a] = true
	}
	uploads := len(dest.uploads)
	dest.mu.Unlock()
	if !removed[first.UploadedIP] {
		t.Fatalf("configs on %s were not deleted: %v", first.UploadedIP, removed)
	}
	runJob(t, o, KindNodeSync)
	var row models.NodeUpload
	o.DB.Where("name = ?", AllNodesName).First(&row)
	if row.Outbounds != 2 || sd.nodes[AllNodesName] == nil || !sd.nodes[AllNodesName].active {
		t.Fatalf("all-auto holds %d proxies after the switch, want 2", row.Outbounds)
	}

	// A new server is not uploaded, yet joins the balancer.
	j, _ := o.Engine.Enqueue(KindProvision, nil, ProvisionParams{AccountID: acc.ID}, "test")
	got := waitJob(t, o, j.ID)
	if got.Status != models.JobDone {
		t.Fatalf("provision %s: %s", got.Status, got.Error)
	}
	jj, _ := o.Engine.Enqueue(KindNodeJoin, got.ServerID, struct{}{}, "test")
	if got := waitJob(t, o, jj.ID); got.Status != models.JobDone {
		t.Fatalf("node join %s: %s", got.Status, got.Error)
	}
	var fresh models.Server
	o.DB.First(&fresh, *got.ServerID)
	dest.mu.Lock()
	after := len(dest.uploads)
	dest.mu.Unlock()
	if after != uploads {
		t.Fatalf("%d upload(s) to vpn-pannel in node-only mode", after-uploads)
	}
	if fresh.UploadedAt == nil {
		t.Fatal("the new server is not counted as published, so no balancer would hold it")
	}
	runJob(t, o, KindNodeSync)
	o.DB.Where("name = ?", AllNodesName).First(&row)
	if row.Outbounds != 3 {
		t.Fatalf("all-auto holds %d proxies, want 3 with the new server", row.Outbounds)
	}

	// Off: the servers' configs are uploaded again.
	o.Settings.Set(map[string]string{settings.NodeOnly: "false"})
	runJob(t, o, KindNodeOnly)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		dest.mu.Lock()
		n := len(dest.uploads)
		dest.mu.Unlock()
		if n >= uploads+3 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the servers' configs did not go up again after the switch was turned off")
}
