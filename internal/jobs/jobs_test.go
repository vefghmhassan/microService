package jobs

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"microserver/internal/database"
	"microserver/internal/models"
)

func waitStatus(t *testing.T, e *Engine, id uint, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var j models.Job
	for time.Now().Before(deadline) {
		e.db.First(&j, id)
		if j.Status == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("job %d is %s, want %s", id, j.Status, want)
}

// A job that never ends on one server must not take the workers away from
// everything else: the jobs queued behind it wait without holding a worker.
func TestStuckServerJobDoesNotStarveTheQueue(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(db); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			sqlDB.Close()
		}
	})
	e := NewEngine(db, 2)
	e.Register("hang", func(ctx context.Context, _ *Run) error {
		<-ctx.Done()
		return ctx.Err()
	})
	e.Register("quick", func(context.Context, *Run) error { return nil })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := e.Start(ctx); err != nil {
		t.Fatal(err)
	}

	sid := uint(7)
	stuck, _ := e.Enqueue("hang", &sid, struct{}{}, "test")
	waitStatus(t, e, stuck.ID, models.JobRunning)
	behind1, _ := e.Enqueue("quick", &sid, struct{}{}, "test")
	behind2, _ := e.Enqueue("quick", &sid, struct{}{}, "test")

	other := uint(8)
	global, _ := e.Enqueue("quick", nil, struct{}{}, "test")
	elsewhere, _ := e.Enqueue("quick", &other, struct{}{}, "test")
	waitStatus(t, e, global.ID, models.JobDone)
	waitStatus(t, e, elsewhere.ID, models.JobDone)

	// Same-server jobs still wait their turn, and run once it is free.
	var j models.Job
	e.db.First(&j, behind1.ID)
	if j.Status != models.JobQueued {
		t.Fatalf("job behind the stuck one is %s", j.Status)
	}
	if err := e.Cancel(stuck.ID); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, e, stuck.ID, models.JobCanceled)
	waitStatus(t, e, behind1.ID, models.JobDone)
	waitStatus(t, e, behind2.ID, models.JobDone)
}
