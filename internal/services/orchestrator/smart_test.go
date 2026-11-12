package orchestrator

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"microserver/internal/models"
	"microserver/internal/services/destination"
)

// smartDest is a vpn-pannel that keeps smart nodes by name.
type smartDest struct {
	*fakeDest
	mu      sync.Mutex
	next    uint
	nodes   map[string]*smartNode
	upserts int
}

type smartNode struct {
	id     uint
	json   string
	ads    bool
	active bool
}

func (d *smartDest) FindByName(_ context.Context, name string) (destination.PanelNode, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if n := d.nodes[name]; n != nil {
		return destination.PanelNode{ID: n.id, Name: name, Active: n.active}, nil
	}
	return destination.PanelNode{}, nil
}

func (d *smartDest) UpsertJSON(_ context.Context, n destination.SmartNode) (uint, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.upserts++
	var m struct {
		Remarks string `json:"remarks"`
	}
	json.Unmarshal([]byte(n.JSON), &m)
	if m.Remarks != n.Name {
		panic("remarks " + m.Remarks + " != name " + n.Name)
	}
	if x := d.nodes[n.Name]; x != nil {
		x.json, x.ads = n.JSON, n.Ads
		return x.id, nil
	}
	d.next++
	d.nodes[n.Name] = &smartNode{id: d.next, json: n.JSON, ads: n.Ads, active: true}
	return d.next, nil
}

func (d *smartDest) byID(id uint) *smartNode {
	for _, n := range d.nodes {
		if n.id == id {
			return n
		}
	}
	return nil
}

func (d *smartDest) SetNodeActive(_ context.Context, id uint, active bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if n := d.byID(id); n != nil {
		n.active = active
	}
	return nil
}

func (d *smartDest) DeleteNode(_ context.Context, id uint) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	for k, n := range d.nodes {
		if n.id == id {
			delete(d.nodes, k)
		}
	}
	return nil
}

func smartSyncOnce(t *testing.T, o *Orchestrator, verify bool) {
	t.Helper()
	j, err := o.Engine.Enqueue(KindSmartSync, nil, SmartSyncParams{Verify: verify}, "test")
	if err != nil {
		t.Fatal(err)
	}
	if got := waitJob(t, o, j.ID); got.Status != models.JobDone {
		t.Fatalf("smart sync %s: %s", got.Status, got.Error)
	}
}

func TestSmartSync(t *testing.T) {
	o, _, _, dest, acc, _ := setup(t)
	sd := &smartDest{fakeDest: dest, nodes: map[string]*smartNode{}}
	o.Dests = func() []NamedDest {
		return []NamedDest{{ID: 1, Name: "test", Ads: []bool{false, true}, Destination: sd}}
	}
	j, _ := o.Engine.Enqueue(KindProvision, nil, ProvisionParams{AccountID: acc.ID}, "test")
	if got := waitJob(t, o, j.ID); got.Status != models.JobDone {
		t.Fatalf("provision %s: %s", got.Status, got.Error)
	}
	var s models.Server
	o.DB.First(&s)
	if s.UploadedInbounds == "" {
		t.Fatal("provision uploaded nothing")
	}
	if err := SeedSmartRules(o.DB); err != nil {
		t.Fatal(err)
	}
	var tg models.SmartRule
	o.DB.Where("key = ?", "telegram").First(&tg)

	per := models.SmartProfile{Name: "per", Enabled: true, Scope: models.SmartScopeServer, RuleIDs: "999," + jsonID(tg.ID)}
	all := models.SmartProfile{Name: "all-in-one", Enabled: true, Scope: models.SmartScopeAll, Family: models.FamilyV4, Country: "DE", NameTemplate: "{name}"}
	v6 := models.SmartProfile{Name: "only-v6", Enabled: true, Scope: models.SmartScopeAll, Family: models.FamilyV6, NameTemplate: "{name}"}
	for _, p := range []*models.SmartProfile{&per, &all, &v6} {
		if err := o.DB.Create(p).Error; err != nil {
			t.Fatal(err)
		}
	}

	smartSyncOnce(t, o, false)
	base := smartBase(&s)
	for _, name := range []string{base + "-smart", base + "-smart-ads", "all-in-one", "all-in-one-ads"} {
		n := sd.nodes[name]
		if n == nil || !n.active {
			t.Fatalf("node %s missing or off; have %v", name, keys(sd.nodes))
		}
	}
	if len(sd.nodes) != 4 {
		t.Fatalf("v6 profile with no v6 server pushed something: %v", keys(sd.nodes))
	}
	if !sd.nodes[base+"-smart-ads"].ads || sd.nodes[base+"-smart"].ads {
		t.Fatal("ads flags are mixed up")
	}
	if !strings.Contains(sd.nodes[base+"-smart"].json, "91.108.4.0/22") || strings.Contains(sd.nodes["all-in-one"].json, "91.108.4.0/22") {
		t.Fatal("rules went to the wrong profile")
	}
	var off models.SmartUpload
	o.DB.Where("profile_id = ?", v6.ID).First(&off)
	if off.Active || off.Error == "" {
		t.Fatalf("v6 row %+v", off)
	}

	// Nothing changed: nothing pushed.
	ups := sd.upserts
	smartSyncOnce(t, o, false)
	if sd.upserts != ups {
		t.Fatalf("unchanged configs pushed again: %d -> %d", ups, sd.upserts)
	}

	// Switched off on the panel (an IP-based bulk action): a verifying sync
	// turns it back on; deleted there: created again.
	sd.nodes["all-in-one"].active = false
	delete(sd.nodes, base+"-smart")
	smartSyncOnce(t, o, true)
	if n := sd.nodes["all-in-one"]; n == nil || !n.active {
		t.Fatal("verify did not switch the node back on")
	}
	if sd.nodes[base+"-smart"] == nil {
		t.Fatal("verify did not recreate the deleted node")
	}

	// Filtered server: its own config goes off, the all-servers one too
	// (it has no server left), and both are kept.
	o.DB.Model(&s).Update("status", models.ServerFiltered)
	smartSyncOnce(t, o, false)
	if sd.nodes[base+"-smart"].active || sd.nodes["all-in-one"].active {
		t.Fatal("configs of a filtered server stayed on")
	}
	o.DB.Model(&s).Update("status", models.ServerActive)
	smartSyncOnce(t, o, false)
	if !sd.nodes[base+"-smart"].active || !sd.nodes["all-in-one-ads"].active {
		t.Fatal("configs did not come back on")
	}

	// Disabled profile: its nodes are deleted.
	o.DB.Model(&per).Update("enabled", false)
	smartSyncOnce(t, o, false)
	if sd.nodes[base+"-smart"] != nil || sd.nodes[base+"-smart-ads"] != nil {
		t.Fatal("disabled profile's nodes left on the panel")
	}
	var n int64
	o.DB.Model(&models.SmartUpload{}).Where("profile_id = ?", per.ID).Count(&n)
	if n != 0 {
		t.Fatalf("%d rows left for the disabled profile", n)
	}

	// Renamed: the old node goes, the new one comes.
	o.DB.Model(&all).Update("name_template", "everything")
	smartSyncOnce(t, o, false)
	if sd.nodes["all-in-one"] != nil || sd.nodes["everything"] == nil || sd.nodes["everything-ads"] == nil {
		t.Fatalf("rename: %v", keys(sd.nodes))
	}
}

func jsonID(id uint) string { b, _ := json.Marshal(id); return string(b) }

func keys(m map[string]*smartNode) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
