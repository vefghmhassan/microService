package orchestrator

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"microserver/internal/models"
	"microserver/internal/services/providers"
)

// hungRemote is a server that vanished mid-command: Run never returns on its
// own.
type hungRemote struct {
	fakeRemote
	started chan struct{}
}

func (r *hungRemote) Run(ctx context.Context, _ string, _ func(string)) (string, error) {
	select {
	case r.started <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return "", ctx.Err()
}

// Deleting a server that is already gone at the provider must work even while
// another job hangs on it, and asking twice must not queue it twice.
func TestDeleteServerGoneAtProviderWithHungJob(t *testing.T) {
	o, prov, _, _, acc, _ := setup(t)
	j, _ := o.Engine.Enqueue(KindProvision, nil, ProvisionParams{AccountID: acc.ID}, "test")
	if got := waitJob(t, o, j.ID); got.Status != models.JobDone {
		t.Fatalf("provision: %s", got.Error)
	}
	var s models.Server
	o.DB.First(&s)

	prov.mu.Lock()
	delete(prov.servers, s.ProviderRef)
	prov.deleteErr = fmt.Errorf("%w: 404", providers.ErrNotFound)
	prov.mu.Unlock()
	hung := &hungRemote{started: make(chan struct{}, 1)}
	o.Connect = func(context.Context, *models.Server) (Remote, string, error) { return hung, "", nil }

	stuck, _ := o.Engine.Enqueue(KindInstall, &s.ID, InstallParams{}, "test")
	select {
	case <-hung.started:
	case <-time.After(5 * time.Second):
		t.Fatal("install never reached the server")
	}

	del, err := o.DeleteServer(s.ID, "test")
	if err != nil {
		t.Fatal(err)
	}
	again, err := o.DeleteServer(s.ID, "test")
	if err != nil || again.ID != del.ID {
		t.Fatalf("second delete queued job %d, want %d (%v)", again.ID, del.ID, err)
	}
	if got := waitJob(t, o, del.ID); got.Status != models.JobDone {
		t.Fatalf("delete: %s %s", got.Status, got.Error)
	}
	if got := waitJob(t, o, stuck.ID); got.Status != models.JobCanceled {
		t.Fatalf("stuck install ended %s", got.Status)
	}
	o.DB.First(&s, s.ID)
	if s.Status != models.ServerDeleted {
		t.Fatalf("server status %s", s.Status)
	}
}

// The config test at the end of an install gives up on a server that stopped
// answering instead of holding the job open.
func TestAutoTestConfigsIsBounded(t *testing.T) {
	old := autoTestTimeout
	autoTestTimeout = 50 * time.Millisecond
	t.Cleanup(func() { autoTestTimeout = old })

	o, _, _, _, acc, _ := setup(t)
	j, _ := o.Engine.Enqueue(KindProvision, nil, ProvisionParams{AccountID: acc.ID}, "test")
	if got := waitJob(t, o, j.ID); got.Status != models.JobDone {
		t.Fatalf("provision: %s", got.Error)
	}
	var s models.Server
	o.DB.First(&s)
	hung := &testHangRemote{}
	o.Connect = func(context.Context, *models.Server) (Remote, string, error) { return hung, "", nil }

	inst, _ := o.Engine.Enqueue(KindInstall, &s.ID, InstallParams{}, "test")
	if got := waitJob(t, o, inst.ID); got.Status != models.JobDone {
		t.Fatalf("install: %s %s", got.Status, got.Error)
	}
	if !hung.hit.Load() {
		t.Fatal("the config test never ran")
	}
	o.DB.First(&s, s.ID)
	if s.Status != models.ServerActive {
		t.Fatalf("server status %s", s.Status)
	}
}

// testHangRemote answers everything except the config test, which never
// returns on its own.
type testHangRemote struct {
	fakeRemote
	hit atomic.Bool
}

func (r *testHangRemote) Run(ctx context.Context, cmd string, logLine func(string)) (string, error) {
	if b64, ok := strings.CutPrefix(cmd, "echo "); ok && strings.HasSuffix(cmd, "| base64 -d | bash") {
		if dec, err := base64.StdEncoding.DecodeString(strings.Fields(b64)[0]); err == nil && strings.Contains(string(dec), "socks5h://") {
			r.hit.Store(true)
			<-ctx.Done()
			return "", ctx.Err()
		}
	}
	return r.fakeRemote.Run(ctx, cmd, logLine)
}

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
