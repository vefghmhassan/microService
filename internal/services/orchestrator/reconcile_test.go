package orchestrator

import (
	"testing"

	"microserver/internal/models"
)

func TestJoinSplitIDs(t *testing.T) {
	got := joinIDs(map[uint]bool{12: true, 3: true, 7: true})
	if got != "3,7,12" {
		t.Fatalf("joinIDs = %q", got)
	}
	ids := splitIDs(" 3,7,,x,12 ")
	if len(ids) != 3 || !ids[3] || !ids[7] || !ids[12] {
		t.Fatalf("splitIDs = %v", ids)
	}
	if len(splitIDs("")) != 0 {
		t.Fatal("empty list must give no ids")
	}
}

func syncOnce(t *testing.T, o *Orchestrator, s *models.Server) {
	t.Helper()
	j, err := o.Engine.Enqueue(KindSync, &s.ID, struct{}{}, "test")
	if err != nil {
		t.Fatal(err)
	}
	if got := waitJob(t, o, j.ID); got.Status != models.JobDone {
		t.Fatalf("sync %s: %s", got.Status, got.Error)
	}
}

// An inbound removed from the template leaves vpn-pannel on the next sync;
// a sync with nothing removed does not touch vpn-pannel.
func TestSyncRemovesDroppedInboundFromVPNPanel(t *testing.T) {
	o, _, _, dest, acc, _ := setup(t)
	var base models.InboundTemplate
	if err := o.DB.Where("upload = ? AND addon_id = 0", true).First(&base).Error; err != nil {
		t.Fatal(err)
	}
	extra := base
	extra.ID, extra.Port, extra.Remark = 0, base.Port+1000, "extra"
	if err := o.DB.Create(&extra).Error; err != nil {
		t.Fatal(err)
	}
	j, _ := o.Engine.Enqueue(KindProvision, nil, ProvisionParams{AccountID: acc.ID}, "test")
	if got := waitJob(t, o, j.ID); got.Status != models.JobDone {
		t.Fatalf("provision %s: %s", got.Status, got.Error)
	}
	var s models.Server
	o.DB.First(&s)
	ids := splitIDs(s.UploadedInbounds)
	if len(ids) < 2 || !ids[extra.ID] {
		t.Fatalf("uploaded set %v should hold the extra inbound %d", ids, extra.ID)
	}

	uploads := len(dest.uploads)
	syncOnce(t, o, &s)
	if len(dest.removed) != 0 || len(dest.uploads) != uploads {
		t.Fatalf("unchanged template touched vpn-pannel: removed=%v uploads=%d->%d", dest.removed, uploads, len(dest.uploads))
	}

	drop := extra.ID
	o.DB.Delete(&models.InboundTemplate{}, drop)
	syncOnce(t, o, &s)

	if len(dest.removed) != 1 {
		t.Fatalf("vpn-pannel deletes = %v", dest.removed)
	}
	if len(dest.uploads) != uploads+1 {
		t.Fatalf("expected one re-upload, uploads %d -> %d", uploads, len(dest.uploads))
	}
	var after models.Server
	o.DB.First(&after, s.ID)
	left := splitIDs(after.UploadedInbounds)
	if left[drop] || len(left) != len(ids)-1 {
		t.Fatalf("uploaded set %v after dropping %d from %v", left, drop, ids)
	}
}
