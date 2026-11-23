package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"microserver/internal/jobs"
	"microserver/internal/models"
	"microserver/internal/services/providers"
)

// PurgeParams names the provider account a purge removes.
type PurgeParams struct {
	AccountID uint `json:"account_id"`
}

// CancelServerJobs cancels the queued and running jobs of the given servers,
// except those of kind keep.
func (o *Orchestrator) CancelServerJobs(serverIDs []uint, keep string) {
	if len(serverIDs) == 0 {
		return
	}
	var active []models.Job
	o.DB.Where("server_id IN ? AND status IN ? AND kind <> ?", serverIDs, []string{models.JobQueued, models.JobRunning}, keep).Find(&active)
	for _, j := range active {
		_ = o.Engine.Cancel(j.ID)
	}
}

// DeleteServer queues the deletion of a server. Whatever else is queued or
// running on it is canceled first, so a job stuck on a server that is already
// gone (deleted in the provider's console) cannot hold the deletion back, and
// asking twice does not queue it twice.
func (o *Orchestrator) DeleteServer(serverID uint, source string) (*models.Job, error) {
	o.CancelServerJobs([]uint{serverID}, KindDelete)
	var pending []models.Job
	o.DB.Where("server_id = ? AND kind = ? AND status IN ?", serverID, KindDelete, []string{models.JobQueued, models.JobRunning}).
		Order("id").Limit(1).Find(&pending)
	if len(pending) > 0 {
		return &pending[0], nil
	}
	return o.Engine.Enqueue(KindDelete, &serverID, struct{}{}, source)
}

// ScheduleDelete takes the server's configs out of vpn-pannel now and marks
// it for full deletion after the given delay (see RunDueDeletes). The time is
// stored on the row, so a restart does not lose it.
func (o *Orchestrator) ScheduleDelete(serverID uint, after time.Duration, source string) (*models.Job, error) {
	at := time.Now().Add(after)
	if err := o.DB.Model(&models.Server{}).Where("id = ?", serverID).Update("delete_at", at).Error; err != nil {
		return nil, err
	}
	o.publishServer(serverID)
	j, err := o.Engine.Enqueue(KindUnpublish, &serverID, struct{}{}, source)
	if err != nil {
		o.DB.Model(&models.Server{}).Where("id = ?", serverID).Update("delete_at", nil)
		o.publishServer(serverID)
		return nil, err
	}
	return j, nil
}

// CancelScheduledDelete drops a scheduled deletion. The configs already
// removed from vpn-pannel stay removed.
func (o *Orchestrator) CancelScheduledDelete(serverID uint) error {
	if err := o.DB.Model(&models.Server{}).Where("id = ?", serverID).Update("delete_at", nil).Error; err != nil {
		return err
	}
	o.publishServer(serverID)
	return nil
}

// RunDueDeletes queues the deletion of every server whose scheduled time
// has come.
func (o *Orchestrator) RunDueDeletes() {
	var due []models.Server
	o.DB.Where("delete_at IS NOT NULL AND delete_at <= ? AND status NOT IN ?", time.Now(),
		[]string{models.ServerDeleting, models.ServerDeleted}).Find(&due)
	for _, s := range due {
		o.DB.Model(&models.Server{}).Where("id = ?", s.ID).Update("delete_at", nil)
		if _, err := o.DeleteServer(s.ID, "schedule"); err != nil {
			continue
		}
	}
}

func (o *Orchestrator) publishServer(id uint) {
	var s models.Server
	if o.DB.First(&s, id).Error == nil && o.Engine != nil && o.Engine.Broker != nil {
		o.Engine.Broker.Publish(serverEvent(&s))
	}
}

// purgeProvider removes a provider account and everything built with it:
// each server is deleted at the provider when its API still answers, its DNS
// record and vpn-pannel configs are removed, and then the rows go. The API is
// usually why the account is being deleted (a dead token), so provider errors
// are only logged.
func (o *Orchestrator) purgeProvider(ctx context.Context, r *jobs.Run) error {
	var p PurgeParams
	if err := r.Params(&p); err != nil || p.AccountID == 0 {
		return errors.New("no provider account given")
	}
	var acc models.ProviderAccount
	if err := o.DB.First(&acc, p.AccountID).Error; err != nil {
		return fmt.Errorf("provider account %d: %w", p.AccountID, err)
	}
	r.Step("purge " + acc.Name)

	var servers []models.Server
	o.DB.Where("provider_account_id = ?", acc.ID).Order("id").Find(&servers)
	ids := make([]uint, 0, len(servers))
	for i := range servers {
		ids = append(ids, servers[i].ID)
	}
	// Nothing else may keep working on these servers.
	o.CancelServerJobs(ids, "")

	prov, perr := o.Provider(&acc)
	if perr != nil {
		r.Logf("warn", "provider client: %v (servers are only removed here)", perr)
	}
	live := 0
	for i := range servers {
		s := &servers[i]
		if s.Status == models.ServerDeleted {
			continue
		}
		live++
		s.ProviderAccount = &acc
		if prov != nil && s.ProviderRef != "" {
			dctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			err := prov.Delete(dctx, s.ProviderRef)
			cancel()
			if err != nil && !errors.Is(err, providers.ErrNotFound) {
				r.Logf("warn", "%s: provider delete failed: %v", s.Name, err)
			} else {
				r.Info("%s: deleted at the provider", s.Name)
			}
		}
		o.retire(ctx, r, s, true)
	}

	err := o.DB.Transaction(func(tx *gorm.DB) error {
		if len(ids) > 0 {
			for _, m := range []any{&models.InboundStat{}, &models.ClientIP{}} {
				if err := tx.Where("server_id IN ?", ids).Delete(m).Error; err != nil {
					return err
				}
			}
			for _, m := range []any{&models.Job{}, &models.CheckRun{}} {
				if err := tx.Model(m).Where("server_id IN ?", ids).Update("server_id", nil).Error; err != nil {
					return err
				}
			}
		}
		if err := tx.Where("provider_account_id = ?", acc.ID).Delete(&models.IPAddress{}).Error; err != nil {
			return err
		}
		if err := tx.Where("provider_account_id = ?", acc.ID).Delete(&models.Location{}).Error; err != nil {
			return err
		}
		if err := tx.Where("provider_account_id = ?", acc.ID).Delete(&models.Server{}).Error; err != nil {
			return err
		}
		return tx.Delete(&acc).Error
	})
	if err != nil {
		return fmt.Errorf("remove rows: %w", err)
	}
	r.Info("account %s and %d servers removed", acc.Name, live)
	o.notify("🗑 حساب %s و %d سرور آن حذف شد", acc.Name, live)
	return nil
}
