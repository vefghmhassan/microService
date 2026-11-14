package orchestrator

import (
	"testing"
	"time"

	"microserver/internal/models"
)

func provisionOne(t *testing.T, o *Orchestrator, acc *models.ProviderAccount) models.Server {
	t.Helper()
	j, _ := o.Engine.Enqueue(KindProvision, nil, ProvisionParams{AccountID: acc.ID}, "test")
	if got := waitJob(t, o, j.ID); got.Status != models.JobDone {
		t.Fatalf("provision: %s", got.Error)
	}
	var s models.Server
	o.DB.First(&s)
	if s.UploadedIP == "" {
		t.Fatal("server was not uploaded")
	}
	return s
}

func reload(o *Orchestrator, id uint) models.Server {
	var s models.Server
	o.DB.First(&s, id)
	return s
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func TestUnpublishRemovesConfigsKeepsServer(t *testing.T) {
	o, _, _, dest, acc, _ := setup(t)
	s := provisionOne(t, o, acc)
	addr := s.UploadedIP

	j, err := o.Engine.Enqueue(KindUnpublish, &s.ID, struct{}{}, "test")
	if err != nil {
		t.Fatal(err)
	}
	if got := waitJob(t, o, j.ID); got.Status != models.JobDone {
		t.Fatalf("unpublish: %s", got.Error)
	}
	dest.mu.Lock()
	removed := append([]string(nil), dest.removed...)
	dest.mu.Unlock()
	if !contains(removed, addr) {
		t.Fatalf("removed %v, want %s", removed, addr)
	}
	s = reload(o, s.ID)
	if s.UploadedIP != "" || s.UploadedAt != nil || s.UploadedInbounds != "" || s.PanelRemovedAt == nil {
		t.Fatalf("upload state not cleared: %+v", s)
	}
	if s.Status != models.ServerActive {
		t.Fatalf("status %s, want active", s.Status)
	}

	snap, err := LoadSnapshot(o.DB)
	if err != nil {
		t.Fatal(err)
	}
	ts, err := o.SmartTargets(snap, &models.SmartProfile{Scope: models.SmartScopeServer})
	if err != nil {
		t.Fatal(err)
	}
	for _, tg := range ts {
		if tg.ServerID == s.ID {
			t.Fatal("unpublished server still has a smart target")
		}
	}

	// An upload brings it back.
	j, _ = o.Engine.Enqueue(KindUpload, &s.ID, struct{}{}, "test")
	if got := waitJob(t, o, j.ID); got.Status != models.JobDone {
		t.Fatalf("upload: %s", got.Error)
	}
	s = reload(o, s.ID)
	if s.PanelRemovedAt != nil || s.UploadedIP == "" {
		t.Fatalf("upload did not restore state: %+v", s)
	}
}

func TestScheduledDeleteRunsAndCancels(t *testing.T) {
	o, _, _, _, acc, _ := setup(t)
	s := provisionOne(t, o, acc)

	j, err := o.ScheduleDelete(s.ID, time.Hour, "test")
	if err != nil {
		t.Fatal(err)
	}
	if got := waitJob(t, o, j.ID); got.Status != models.JobDone {
		t.Fatalf("unpublish: %s", got.Error)
	}
	s = reload(o, s.ID)
	if s.DeleteAt == nil || s.PanelRemovedAt == nil {
		t.Fatalf("not scheduled: %+v", s)
	}

	// Not due yet: nothing happens.
	o.RunDueDeletes()
	var n int64
	o.DB.Model(&models.Job{}).Where("kind = ?", KindDelete).Count(&n)
	if n != 0 {
		t.Fatal("delete queued before its time")
	}

	// Cancel, then make it due: still nothing.
	if err := o.CancelScheduledDelete(s.ID); err != nil {
		t.Fatal(err)
	}
	o.RunDueDeletes()
	o.DB.Model(&models.Job{}).Where("kind = ?", KindDelete).Count(&n)
	if n != 0 {
		t.Fatal("canceled deletion still ran")
	}

	// Schedule again and let it come due.
	if _, err := o.ScheduleDelete(s.ID, -time.Second, "test"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		o.RunDueDeletes()
		s = reload(o, s.ID)
		if s.Status == models.ServerDeleted {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if s.Status != models.ServerDeleted || s.DeleteAt != nil {
		t.Fatalf("server not deleted: status %s delete_at %v", s.Status, s.DeleteAt)
	}
}
