package orchestrator

import (
	"testing"
	"time"

	"microserver/internal/models"
)

func TestNextIdleSince(t *testing.T) {
	now := time.Now()
	old := now.Add(-time.Hour)
	if got := nextIdleSince(nil, true, now); got == nil || !got.Equal(now) {
		t.Fatalf("idle start: %v", got)
	}
	if got := nextIdleSince(&old, true, now); got == nil || !got.Equal(old) {
		t.Fatalf("idle clock moved: %v", got)
	}
	if got := nextIdleSince(&old, false, now); got != nil {
		t.Fatalf("users came back but still idle: %v", got)
	}
}

func TestIdleRuleCycle(t *testing.T) {
	now := time.Now()
	at := func(d time.Duration) *time.Time { x := now.Add(-d); return &x }
	r := idleRule{After: 30 * time.Minute, Retry: 6 * time.Hour}
	cases := []struct {
		idle time.Duration
		out  bool
	}{
		{10 * time.Minute, false},             // not long enough
		{31 * time.Minute, true},              // out
		{6*time.Hour + 20*time.Minute, true},  // still out
		{6*time.Hour + 40*time.Minute, false}, // back for a try
		{7*time.Hour + 10*time.Minute, true},  // try failed: out again
	}
	for _, c := range cases {
		if got := r.out(at(c.idle), now); got != c.out {
			t.Fatalf("idle %v: out=%v, want %v", c.idle, got, c.out)
		}
	}
	if r.out(nil, now) || (idleRule{}).out(at(time.Hour), now) {
		t.Fatal("no idle clock or rule off must keep it in")
	}
	if !(idleRule{After: time.Minute}).out(at(48*time.Hour), now) {
		t.Fatal("no retry: stays out")
	}
}

func TestIdleOut(t *testing.T) {
	o, _, _, _, acc, _ := setup(t)
	s := models.Server{Name: "s1", Status: models.ServerActive, ProviderAccountID: acc.ID}
	o.DB.Create(&s)
	now := time.Now()
	long := now.Add(-2 * time.Hour)
	put := func(port, online int, idle *time.Time) {
		o.DB.Where("server_id = ? AND port = ?", s.ID, port).Delete(&models.InboundStat{})
		o.DB.Create(&models.InboundStat{ServerID: s.ID, Port: port, Enabled: true, Online: online, IdleSince: idle})
	}
	r := idleRule{After: 30 * time.Minute}
	ports := []int{443, 8080, 8388}

	// 8080 idle for long while 443 has users: out.
	put(443, 20, nil)
	put(8080, 0, &long)
	put(8388, 3, nil)
	if out := o.idleOut(&s, r, ports, now); len(out) != 1 || !out[8080] {
		t.Fatalf("out %v", out)
	}
	// Narrowed to idle ports only: never all of them.
	if out := o.idleOut(&s, r, []int{8080}, now); len(out) != 0 {
		t.Fatalf("dropped every inbound: %v", out)
	}
	// Nobody on the server at all: a quiet server, not a broken inbound.
	put(443, 0, &long)
	put(8388, 0, &long)
	if out := o.idleOut(&s, r, ports, now); len(out) != 0 {
		t.Fatalf("quiet server emptied: %v", out)
	}
	// Rule off.
	if out := o.idleOut(&s, idleRule{}, ports, now); len(out) != 0 {
		t.Fatalf("rule off dropped: %v", out)
	}
}

func TestSmartItemsDropIdleAndScore(t *testing.T) {
	o, _, _, _, acc, _ := setup(t)
	j, _ := o.Engine.Enqueue(KindProvision, nil, ProvisionParams{AccountID: acc.ID}, "test")
	if got := waitJob(t, o, j.ID); got.Status != models.JobDone {
		t.Fatalf("provision %s: %s", got.Status, got.Error)
	}
	var s models.Server
	o.DB.First(&s)
	snap, err := LoadSnapshot(o.DB)
	if err != nil {
		t.Fatal(err)
	}
	// A second inbound like the first, on another port.
	var first *models.InboundTemplate
	for i := range snap.Inbounds {
		if in := &snap.Inbounds[i]; in.Enabled && in.Upload && splitIDs(s.UploadedInbounds)[in.ID] {
			first = in
			break
		}
	}
	if first == nil {
		t.Fatal("no uploaded inbound")
	}
	second := *first
	second.ID, second.Port = first.ID+1000, first.Port+1
	snap.Inbounds = append(snap.Inbounds, second)
	s.UploadedInbounds += "," + jsonID(second.ID)
	ports := []int{first.Port, second.Port}

	all, _ := o.smartItems(snap, &s, nil, idleRule{})
	if len(all) < 2 {
		t.Fatalf("got %d links for two inbounds", len(all))
	}
	long := time.Now().Add(-2 * time.Hour)
	o.DB.Where("server_id = ?", s.ID).Delete(&models.InboundStat{})
	o.DB.Create(&models.InboundStat{ServerID: s.ID, Port: ports[0], Enabled: true, Online: 0, IdleSince: &long})
	o.DB.Create(&models.InboundStat{ServerID: s.ID, Port: ports[1], Enabled: true, Online: 9, QKbps: 4000})

	kept, _ := o.smartItems(snap, &s, nil, idleRule{After: 30 * time.Minute})
	if len(kept) >= len(all) {
		t.Fatalf("idle inbound kept: %d of %d", len(kept), len(all))
	}
	scored := false
	for _, it := range kept {
		scored = scored || it.Score == 4000
	}
	if !scored {
		t.Fatalf("no score carried: %+v", kept)
	}
	// Without DropIdle everything stays.
	if again, _ := o.smartItems(snap, &s, nil, idleRule{}); len(again) != len(all) {
		t.Fatalf("rule off dropped: %d of %d", len(again), len(all))
	}
}
