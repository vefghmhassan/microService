package orchestrator

import (
	"time"

	"gorm.io/gorm"

	"microserver/internal/models"
)

// History (jobs with their logs, checks with their probes) is only read for
// the recent past, but nothing removed it: the tables grew until a backup
// with history, and the database copy before every panel update, took
// minutes. PruneHistory keeps the last keepDays days.

// Work of one PruneHistory call is bounded, so it never holds the database
// for long; a large backlog goes over the next calls.
const (
	pruneBatch   = 500 // jobs or checks per statement
	pruneBatches = 40  // statements of each kind per call
)

// PruneHistory deletes finished jobs and checks older than keepDays days,
// with their logs and probes, and reports how many of each went. keepDays
// of 0 or less keeps everything. Queued and running jobs are never touched.
func PruneHistory(db *gorm.DB, keepDays int) (jobs, checks int64) {
	if keepDays <= 0 {
		return 0, 0
	}
	cut := time.Now().AddDate(0, 0, -keepDays)
	finished := []string{models.JobDone, models.JobFailed, models.JobCanceled}
	for i := 0; i < pruneBatches; i++ {
		var ids []uint
		db.Model(&models.Job{}).Where("created_at < ? AND status IN ?", cut, finished).
			Order("id").Limit(pruneBatch).Pluck("id", &ids)
		if len(ids) == 0 {
			break
		}
		db.Where("job_id IN ?", ids).Delete(&models.JobLog{})
		jobs += db.Where("id IN ?", ids).Delete(&models.Job{}).RowsAffected
	}
	for i := 0; i < pruneBatches; i++ {
		var ids []uint
		db.Model(&models.CheckRun{}).Where("created_at < ?", cut).Order("id").Limit(pruneBatch).Pluck("id", &ids)
		if len(ids) == 0 {
			break
		}
		db.Where("check_run_id IN ?", ids).Delete(&models.CheckProbe{})
		checks += db.Where("id IN ?", ids).Delete(&models.CheckRun{}).RowsAffected
	}
	return jobs, checks
}
