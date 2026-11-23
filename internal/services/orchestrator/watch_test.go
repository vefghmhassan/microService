package orchestrator

import (
	"context"
	"sync"
	"testing"
	"time"

	"microserver/internal/models"
	"microserver/internal/services/settings"
)

func TestTrafficLimitPrecedence(t *testing.T) {
	o, _, _, _, acc, _ := setup(t)
	o.Settings.Set(map[string]string{settings.TrafficLimitGB: "100"})
	loc := models.Location{ProviderAccountID: acc.ID, Region: "fsn1", Plan: "cx22", TrafficLimitGB: 9 * 1024, Enabled: true}
	o.DB.Create(&loc)

	s := models.Server{LocationID: loc.ID}
	if got := o.TrafficLimit(&s); got != 9*1024*gb {
		t.Fatalf("location cap: %d", got)
	}
	s.TrafficLimitGB = 200
	if got := o.TrafficLimit(&s); got != 200*gb {
		t.Fatalf("server cap: %d", got)
	}
	if got := o.TrafficLimit(&models.Server{}); got != 100*gb {
		t.Fatalf("global cap: %d", got)
	}
	o.DB.Model(&loc).Update("traffic_limit_gb", 0)
	if got := o.TrafficLimit(&models.Server{LocationID: loc.ID}); got != 100*gb {
		t.Fatalf("location without cap: %d", got)
	}
}

func TestLowUsageAlertsOnce(t *testing.T) {
	o, _, panel, _, acc, _ := setup(t)
	o.Settings.Set(map[string]string{settings.WatchLowUsage: "true", settings.WatchMinutes: "30", settings.WatchMinMB: "1024"})
	var mu sync.Mutex
	var alerts []uint
	o.Alert = func(s *models.Server, text string) {
		mu.Lock()
		alerts = append(alerts, s.ID)
		mu.Unlock()
	}
	j, _ := o.Engine.Enqueue(KindProvision, nil, ProvisionParams{AccountID: acc.ID}, "test")
	if got := waitJob(t, o, j.ID); got.Status != models.JobDone {
		t.Fatalf("provision: %s", got.Error)
	}
	var s models.Server
	o.DB.First(&s)
	if s.WatchFrom == nil {
		t.Fatal("watch not started when the server went live")
	}

	panel.mu.Lock()
	panel.inbounds[0].Up, panel.inbounds[0].Down = 100<<20, 200<<20
	panel.mu.Unlock()

	// Window still open: nothing yet.
	o.CollectAll(context.Background())
	if len(alerts) != 0 {
		t.Fatalf("alerted before the window passed: %v", alerts)
	}

	o.DB.Model(&s).Update("watch_from", time.Now().Add(-31*time.Minute))
	o.CollectAll(context.Background())
	o.CollectAll(context.Background()) // the window is closed: no second alert
	if len(alerts) != 1 || alerts[0] != s.ID {
		t.Fatalf("alerts %v", alerts)
	}
	var after models.Server
	o.DB.First(&after, s.ID)
	if after.WatchFrom != nil {
		t.Fatal("window not closed")
	}
}

func TestLowUsageQuietWhenUsed(t *testing.T) {
	o, _, panel, _, acc, _ := setup(t)
	o.Settings.Set(map[string]string{settings.WatchLowUsage: "true", settings.WatchMinMB: "1024"})
	n := 0
	o.Alert = func(*models.Server, string) { n++ }
	j, _ := o.Engine.Enqueue(KindProvision, nil, ProvisionParams{AccountID: acc.ID}, "test")
	if got := waitJob(t, o, j.ID); got.Status != models.JobDone {
		t.Fatalf("provision: %s", got.Error)
	}
	var s models.Server
	o.DB.First(&s)

	// 3 GB used in the window.
	panel.mu.Lock()
	panel.inbounds[0].Up, panel.inbounds[0].Down = gb, 2*gb
	panel.mu.Unlock()
	o.DB.Model(&s).Update("watch_from", time.Now().Add(-31*time.Minute))
	o.CollectAll(context.Background())
	if n != 0 {
		t.Fatal("alerted although 3 GB were used")
	}

	// Counters reset below the base, but 3 GB since: still quiet.
	o.DB.Model(&s).Updates(map[string]any{"watch_from": time.Now().Add(-31 * time.Minute), "watch_base": 50 * gb})
	o.CollectAll(context.Background())
	if n != 0 {
		t.Fatal("alerted after a counter reset")
	}
}
