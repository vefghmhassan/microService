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
	if len(ids) > 0 {
		// Nothing else may keep working on these servers.
		var active []models.Job
		o.DB.Where("server_id IN ? AND status IN ?", ids, []string{models.JobQueued, models.JobRunning}).Find(&active)
		for _, j := range active {
			_ = o.Engine.Cancel(j.ID)
		}
	}

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
