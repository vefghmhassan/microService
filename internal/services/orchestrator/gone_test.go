package orchestrator

import (
	"context"
	"errors"
	"strings"
	"testing"

	"microserver/internal/models"
)

// A server deleted in the provider's console must stop looking active, and
// the GeoIP report must say why no addresses were collected.
func TestCollectStatsMarksServerDeletedOutsidePanel(t *testing.T) {
	o, prov, _, _, acc, _ := setup(t)
	j, _ := o.Engine.Enqueue(KindProvision, nil, ProvisionParams{AccountID: acc.ID}, "test")
	if got := waitJob(t, o, j.ID); got.Status != models.JobDone {
		t.Fatalf("provision: %s", got.Error)
	}
	var s models.Server
	o.DB.First(&s)

	prov.mu.Lock()
	delete(prov.servers, s.ProviderRef)
	prov.mu.Unlock()
	o.Connect = func(context.Context, *models.Server) (Remote, string, error) {
		return nil, "", errors.New("dial tcp: i/o timeout")
	}

	o.CollectAll(context.Background())
	o.DB.First(&s, s.ID)
	if s.Status != models.ServerError || !strings.Contains(s.StatsError, "no longer exists") {
		t.Fatalf("status=%s stats_error=%q", s.Status, s.StatsError)
	}
	if s.IPCollectAt == nil || !strings.Contains(s.IPCollectNote, "ssh") {
		t.Fatalf("ip collect note %q", s.IPCollectNote)
	}
}

// A server that still exists but does not answer keeps its status.
func TestCollectStatsKeepsUnreachableServer(t *testing.T) {
	o, _, _, _, acc, _ := setup(t)
	j, _ := o.Engine.Enqueue(KindProvision, nil, ProvisionParams{AccountID: acc.ID}, "test")
	if got := waitJob(t, o, j.ID); got.Status != models.JobDone {
		t.Fatalf("provision: %s", got.Error)
	}
	o.Connect = func(context.Context, *models.Server) (Remote, string, error) {
		return nil, "", errors.New("dial tcp: i/o timeout")
	}
	o.CollectAll(context.Background())
	var s models.Server
	o.DB.First(&s)
	if s.Status != models.ServerActive || !strings.Contains(s.StatsError, "i/o timeout") {
		t.Fatalf("status=%s stats_error=%q", s.Status, s.StatsError)
	}
}
