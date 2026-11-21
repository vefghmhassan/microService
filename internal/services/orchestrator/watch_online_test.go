package orchestrator

import (
	"strings"
	"testing"
	"time"

	"microserver/internal/models"
	"microserver/internal/services/settings"
)

// lowOnlineCase runs one closed watch window with plenty of traffic and
// returns the alert text ("" = no alert).
func lowOnlineCase(t *testing.T, conf map[string]string, base, online int) string {
	t.Helper()
	o, _, _, _, acc, _ := setup(t)
	vals := map[string]string{settings.WatchLowUsage: "true", settings.WatchMinutes: "30", settings.WatchMinMB: "1"}
	for k, v := range conf {
		vals[k] = v
	}
	if err := o.Settings.Set(vals); err != nil {
		t.Fatal(err)
	}
	var got string
	o.Alert = func(_ *models.Server, text string) { got = text }
	from := time.Now().Add(-31 * time.Minute)
	s := models.Server{Name: "w1", ProviderAccountID: acc.ID, Status: models.ServerActive, IPv4: "10.9.9.9",
		TrafficUp: 50 << 20, WatchFrom: &from, WatchOnlineBase: base, OnlineCount: online}
	o.DB.Create(&s)
	o.checkLowUsage(&s)
	return got
}

func TestLowOnlineAgainstBefore(t *testing.T) {
	if txt := lowOnlineCase(t, nil, 8, 2); !strings.Contains(txt, "آنلاین: 2") || !strings.Contains(txt, "8") {
		t.Fatalf("8 → 2 online should alert, got %q", txt)
	}
	if txt := lowOnlineCase(t, nil, 8, 6); txt != "" {
		t.Fatalf("8 → 6 online should be quiet, got %q", txt)
	}
	if txt := lowOnlineCase(t, map[string]string{settings.WatchOnlinePct: "0"}, 8, 1); txt != "" {
		t.Fatalf("online_pct=0 should be quiet, got %q", txt)
	}
}

func TestLowOnlineNewServerMinimum(t *testing.T) {
	if txt := lowOnlineCase(t, map[string]string{settings.WatchMinOnline: "3"}, 0, 1); !strings.Contains(txt, "حداقل 3") {
		t.Fatalf("min_online=3 with 1 online should alert, got %q", txt)
	}
	if txt := lowOnlineCase(t, nil, 0, 0); txt != "" {
		t.Fatalf("min_online=0 should be quiet, got %q", txt)
	}
}

// A rotation remembers how many were online before it.
func TestRotateSavesOnlineBefore(t *testing.T) {
	o, _, _, _, acc, _ := setup(t)
	j, _ := o.Engine.Enqueue(KindProvision, nil, ProvisionParams{AccountID: acc.ID}, "test")
	if got := waitJob(t, o, j.ID); got.Status != models.JobDone {
		t.Fatalf("provision: %s", got.Error)
	}
	var s models.Server
	o.DB.First(&s)
	o.DB.Model(&s).Update("online_count", 8)
	id := s.ID
	j, _ = o.Engine.Enqueue(KindRotate, &id, RotateParams{Reason: "manual"}, "test")
	if got := waitJob(t, o, j.ID); got.Status != models.JobDone {
		t.Fatalf("rotate: %s", got.Error)
	}
	o.DB.First(&s, id)
	if s.WatchFrom == nil || s.WatchOnlineBase != 8 {
		t.Fatalf("watch from=%v online base=%d", s.WatchFrom, s.WatchOnlineBase)
	}
}
