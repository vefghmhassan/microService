package orchestrator

import (
	"context"
	"errors"
	"sync"
	"testing"

	"microserver/internal/models"
	"microserver/internal/services/destination"
)

// flakyDest fails every deletion while down is set.
type flakyDest struct {
	mu      sync.Mutex
	down    bool
	deleted []string
}

func (d *flakyDest) Upload(context.Context, destination.UploadRequest) error { return nil }
func (d *flakyDest) ReplaceAddress(context.Context, string, string, string) (*destination.ReplaceResult, error) {
	return &destination.ReplaceResult{Updated: 1}, nil
}
func (d *flakyDest) SetActive(context.Context, string, bool) error { return nil }
func (d *flakyDest) DeleteAddress(_ context.Context, addr string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.down {
		return errors.New("connection refused")
	}
	d.deleted = append(d.deleted, addr)
	return nil
}

func deleteServer(t *testing.T, o *Orchestrator, id uint) {
	t.Helper()
	j, err := o.DeleteServer(id, "test")
	if err != nil {
		t.Fatal(err)
	}
	if got := waitJob(t, o, j.ID); got.Status != models.JobDone {
		t.Fatalf("delete: %s %s", got.Status, got.Error)
	}
}

// Deleting a server deletes its configs from the destination panel; they
// used to be only deactivated and stayed there.
func TestDeleteRemovesConfigs(t *testing.T) {
	o, _, _, dest, acc, _ := setup(t)
	s := provisionOne(t, o, acc)
	deleteServer(t, o, s.ID)
	dest.mu.Lock()
	removed := append([]string(nil), dest.removed...)
	dest.mu.Unlock()
	if !contains(removed, s.UploadedIP) {
		t.Fatalf("removed %v, want %s", removed, s.UploadedIP)
	}
}

// A deletion the panel did not take is stored and retried until it goes
// through.
func TestFailedPanelDeleteIsRetried(t *testing.T) {
	o, _, _, _, acc, _ := setup(t)
	s := provisionOne(t, o, acc)
	fd := &flakyDest{down: true}
	o.Dests = func() []NamedDest { return []NamedDest{{ID: 7, Name: "flaky", Destination: fd}} }
	deleteServer(t, o, s.ID)

	var rows []models.PanelCleanup
	o.DB.Find(&rows)
	if len(rows) == 0 || rows[0].DestID != 7 {
		t.Fatalf("pending cleanups %+v", rows)
	}

	old := panelCleanupEvery
	panelCleanupEvery = 0
	t.Cleanup(func() { panelCleanupEvery = old })

	o.RunPanelCleanups(context.Background())
	o.DB.Find(&rows)
	if len(rows) == 0 || rows[0].Attempts != 1 {
		t.Fatalf("still down: %+v", rows)
	}

	fd.mu.Lock()
	fd.down = false
	fd.mu.Unlock()
	o.RunPanelCleanups(context.Background())
	var n int64
	o.DB.Model(&models.PanelCleanup{}).Count(&n)
	if n != 0 || !contains(fd.deleted, s.UploadedIP) {
		t.Fatalf("left %d rows, deleted %v", n, fd.deleted)
	}
}

// An address another live server still uses keeps its configs.
func TestDeleteKeepsAddressInUse(t *testing.T) {
	o, _, _, dest, acc, _ := setup(t)
	s := provisionOne(t, o, acc)
	other := models.Server{Name: "other", Status: models.ServerActive, Hostname: "x1.example.com", ProviderAccountID: acc.ID}
	o.DB.Create(&other)
	o.DB.Model(&s).Update("hostname", "x1.example.com")
	deleteServer(t, o, s.ID)
	dest.mu.Lock()
	defer dest.mu.Unlock()
	if contains(dest.removed, "x1.example.com") {
		t.Fatalf("shared name was cleared: %v", dest.removed)
	}
	if !contains(dest.removed, s.UploadedIP) {
		t.Fatalf("removed %v, want %s", dest.removed, s.UploadedIP)
	}
}
