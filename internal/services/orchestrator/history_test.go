package orchestrator

import (
	"testing"
	"time"

	"microserver/internal/models"
)

func TestPruneHistoryKeepsRecentAndActive(t *testing.T) {
	o, _, _, _, _, _ := setup(t)
	old, recent := time.Now().AddDate(0, 0, -30), time.Now().AddDate(0, 0, -2)
	job := func(status string, at time.Time) uint {
		j := models.Job{Kind: "check", Status: status, CreatedAt: at, Logs: []models.JobLog{{Message: "a", CreatedAt: at}, {Message: "b", CreatedAt: at}}}
		if err := o.DB.Create(&j).Error; err != nil {
			t.Fatal(err)
		}
		return j.ID
	}
	check := func(at time.Time) uint {
		c := models.CheckRun{Host: "h", Port: 443, CreatedAt: at, Probes: []models.CheckProbe{{Source: "t"}, {Source: "t"}}}
		if err := o.DB.Create(&c).Error; err != nil {
			t.Fatal(err)
		}
		return c.ID
	}
	oldDone, oldFailed := job(models.JobDone, old), job(models.JobFailed, old)
	oldRunning, newDone := job(models.JobRunning, old), job(models.JobDone, recent)
	oldCheck, newCheck := check(old), check(recent)

	if j, c := PruneHistory(o.DB, 0); j != 0 || c != 0 {
		t.Fatalf("0 days keeps everything, removed %d jobs %d checks", j, c)
	}
	j, c := PruneHistory(o.DB, 14)
	if j != 2 || c != 1 {
		t.Fatalf("removed %d jobs and %d checks, want 2 and 1", j, c)
	}
	count := func(m any, q string, id uint) (n int64) {
		o.DB.Model(m).Where(q, id).Count(&n)
		return
	}
	for _, id := range []uint{oldDone, oldFailed} {
		if count(&models.Job{}, "id = ?", id) != 0 || count(&models.JobLog{}, "job_id = ?", id) != 0 {
			t.Fatalf("old finished job %d or its logs survived", id)
		}
	}
	for _, id := range []uint{oldRunning, newDone} {
		if count(&models.Job{}, "id = ?", id) != 1 || count(&models.JobLog{}, "job_id = ?", id) != 2 {
			t.Fatalf("job %d (running or recent) must stay with its logs", id)
		}
	}
	if count(&models.CheckRun{}, "id = ?", oldCheck) != 0 || count(&models.CheckProbe{}, "check_run_id = ?", oldCheck) != 0 {
		t.Fatal("old check or its probes survived")
	}
	if count(&models.CheckRun{}, "id = ?", newCheck) != 1 || count(&models.CheckProbe{}, "check_run_id = ?", newCheck) != 2 {
		t.Fatal("recent check must stay with its probes")
	}
}
