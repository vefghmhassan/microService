package orchestrator

import (
	"testing"

	"microserver/internal/models"
	"microserver/internal/services/providers"
)

// A provider whose token died is still removed with everything built on it.
func TestPurgeProviderWithDeadToken(t *testing.T) {
	o, cf, _, dest, acc, _ := setupDNS(t)
	runAuto(t, o, AutoParams{Count: 2})
	var live []models.Server
	o.DB.Where("status = ?", models.ServerActive).Find(&live)
	if len(live) != 2 {
		t.Fatalf("%d active servers", len(live))
	}
	prov, _ := o.Provider(acc)
	prov.(*fakeProvider).deleteErr = &providers.APIError{Status: 401, Body: `{"errors":[{"reason":"Invalid Token"}]}`}

	j, err := o.Engine.Enqueue(KindPurgeProvider, nil, PurgeParams{AccountID: acc.ID}, "test")
	if err != nil {
		t.Fatal(err)
	}
	if got := waitJob(t, o, j.ID); got.Status != models.JobDone {
		t.Fatalf("purge: %s", got.Error)
	}

	for _, m := range []any{&models.ProviderAccount{}, &models.Server{}, &models.Location{}} {
		var n int64
		o.DB.Model(m).Count(&n)
		if n != 0 {
			t.Fatalf("%T: %d rows left", m, n)
		}
	}
	for _, s := range live {
		if _, ok := cf.get(s.Hostname); ok {
			t.Fatalf("DNS record %s kept", s.Hostname)
		}
	}
	if len(dest.removed) < 2 {
		t.Fatalf("vpn-pannel deletes %v", dest.removed)
	}
}
