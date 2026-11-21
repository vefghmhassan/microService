package orchestrator

import (
	"errors"
	"time"

	"gorm.io/gorm"

	"microserver/internal/jobs"
	"microserver/internal/models"
	"microserver/internal/services/settings"
)

// banIP keeps addr from being bought or reused for ip.ban_hours. The ban is
// stored on the pool row of (addr, account), but isBanned matches the address
// alone: the IP is what Iran blocks, whichever account hands it out next.
// A ban is only ever extended, never shortened.
func (o *Orchestrator) banIP(r *jobs.Run, accountID uint, region, addr, reason string) {
	hours := o.Settings.Int(settings.IPBanHours)
	if addr == "" || hours <= 0 {
		return
	}
	until := time.Now().Add(time.Duration(hours) * time.Hour)
	var ip models.IPAddress
	err := o.DB.Where("address = ? AND provider_account_id = ?", addr, accountID).First(&ip).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		ip = models.IPAddress{Address: addr, ProviderAccountID: accountID, Region: region, Status: models.IPReleased}
	} else if err != nil {
		if r != nil {
			r.Logf("warn", "ban %s: %v", addr, err)
		}
		return
	}
	if ip.BannedUntil != nil && ip.BannedUntil.After(until) {
		return
	}
	ip.BannedUntil = &until
	if reason != "" {
		ip.Note = truncate("banned: "+reason, 255)
	}
	if err := o.DB.Save(&ip).Error; err != nil {
		if r != nil {
			r.Logf("warn", "ban %s: %v", addr, err)
		}
		return
	}
	if r != nil {
		r.Info("IP %s banned until %s (%s)", addr, until.Format("2006-01-02 15:04"), reason)
	}
}

// banServerIPs bans every public address s holds right now.
func (o *Orchestrator) banServerIPs(r *jobs.Run, s *models.Server, reason string) {
	o.banIP(r, s.ProviderAccountID, s.Region, s.IPv4, reason)
	if s.IPv6 != "" && s.Family() == models.FamilyV6 {
		o.banIP(r, s.ProviderAccountID, s.Region, s.IPv6, reason)
	}
}

// isBanned reports whether addr is under a ban on any account, and until when.
func (o *Orchestrator) isBanned(addr string) (time.Time, bool) {
	if addr == "" {
		return time.Time{}, false
	}
	var ip models.IPAddress
	err := o.DB.Where("address = ? AND banned_until > ?", addr, time.Now()).
		Order("banned_until DESC").First(&ip).Error
	if err != nil || ip.BannedUntil == nil {
		return time.Time{}, false
	}
	return *ip.BannedUntil, true
}
