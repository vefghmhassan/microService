package orchestrator

import (
	"testing"
	"time"

	"microserver/internal/models"
	"microserver/internal/services/settings"
)

// A ban on one account keeps the address out for every account, and runs out.
func TestIPBanMatchesAddressAcrossAccountsAndExpires(t *testing.T) {
	o, _, _, _, acc, _ := setup(t)
	o.banIP(nil, acc.ID+100, "fsn1", "1.2.3.4", "test")
	if _, ok := o.isBanned("1.2.3.4"); !ok {
		t.Fatal("not banned")
	}
	if _, ok := o.isBanned("1.2.3.5"); ok {
		t.Fatal("unrelated address banned")
	}
	past := time.Now().Add(-time.Minute)
	o.DB.Model(&models.IPAddress{}).Where("address = ?", "1.2.3.4").Update("banned_until", past)
	if _, ok := o.isBanned("1.2.3.4"); ok {
		t.Fatal("expired ban still counts")
	}

	if err := o.Settings.Set(map[string]string{settings.IPBanHours: "0"}); err != nil {
		t.Fatal(err)
	}
	o.banIP(nil, acc.ID, "fsn1", "5.6.7.8", "test")
	if _, ok := o.isBanned("5.6.7.8"); ok {
		t.Fatal("ban with ip.ban_hours=0")
	}
}

// A provision that is handed a banned IP rotates away without checking it.
func TestProvisionRotatesAwayFromBannedIP(t *testing.T) {
	o, _, _, _, acc, _ := setup(t)
	o.banIP(nil, acc.ID+1, "fsn1", "10.0.0.1", "test")

	j, err := o.Engine.Enqueue(KindProvision, nil, ProvisionParams{AccountID: acc.ID}, "test")
	if err != nil {
		t.Fatal(err)
	}
	if got := waitJob(t, o, j.ID); got.Status != models.JobDone {
		t.Fatalf("provision: %s", got.Error)
	}
	var s models.Server
	o.DB.Where("status <> ?", models.ServerDeleted).First(&s)
	if s.IPv4 == "10.0.0.1" || s.IPv4 == "" {
		t.Fatalf("server kept banned IP %q", s.IPv4)
	}
	var checks int64
	o.DB.Model(&models.CheckRun{}).Where("host = ?", "10.0.0.1").Count(&checks)
	if checks != 0 {
		t.Fatalf("banned IP was checked %d times", checks)
	}
}

// A manual rotation bans the address left behind; a ban hides kept IPs from reuse.
func TestRotationBansOldIPAndReuseSkipsBanned(t *testing.T) {
	o, _, _, _, acc, _ := setup(t)
	j, _ := o.Engine.Enqueue(KindProvision, nil, ProvisionParams{AccountID: acc.ID}, "test")
	if got := waitJob(t, o, j.ID); got.Status != models.JobDone {
		t.Fatalf("provision: %s", got.Error)
	}
	var s models.Server
	o.DB.First(&s)
	old := s.IPv4
	id := s.ID
	j, _ = o.Engine.Enqueue(KindRotate, &id, RotateParams{Reason: "manual"}, "test")
	if got := waitJob(t, o, j.ID); got.Status != models.JobDone {
		t.Fatalf("rotate: %s", got.Error)
	}
	if _, ok := o.isBanned(old); !ok {
		t.Fatalf("old IP %s not banned after rotation", old)
	}

	o.DB.Create(&models.IPAddress{ProviderAccountID: acc.ID, Address: "9.9.9.9", Region: "r1", Status: models.IPKept, Reusable: true})
	if ip := o.pickReusable(acc.ID, "r1"); ip == nil {
		t.Fatal("kept IP not offered")
	}
	o.banIP(nil, acc.ID, "r1", "9.9.9.9", "test")
	if ip := o.pickReusable(acc.ID, "r1"); ip != nil {
		t.Fatal("banned kept IP offered for reuse")
	}
}
