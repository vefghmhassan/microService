package orchestrator

import (
	"context"
	"errors"
	"testing"

	"microserver/internal/models"
	"microserver/internal/services/destination"
)

// downDest is a vpn-pannel that is unreachable for uploads and has no configs.
type downDest struct{ uploads, replaces int }

func (d *downDest) Upload(context.Context, destination.UploadRequest) error {
	d.uploads++
	return errors.New("connection refused")
}
func (d *downDest) ReplaceAddress(context.Context, string, string, string) (*destination.ReplaceResult, error) {
	d.replaces++
	return nil, destination.ErrNoNodes
}
func (d *downDest) SetActive(context.Context, string, bool) error { return nil }

// One unreachable panel must not fail provisioning or rotation while
// another one takes the configs.
func TestSeveralDestinations(t *testing.T) {
	o, _, _, dest, acc, _ := setup(t)
	down := &downDest{}
	o.Dests = func() []NamedDest {
		return []NamedDest{{Name: "main", Destination: dest}, {Name: "down", Ads: []bool{true}, Destination: down}}
	}
	j, _ := o.Engine.Enqueue(KindProvision, nil, ProvisionParams{AccountID: acc.ID}, "test")
	if got := waitJob(t, o, j.ID); got.Status != models.JobDone {
		t.Fatalf("provision %s: %s", got.Status, got.Error)
	}
	if len(dest.uploads) != 1 || down.uploads != 1 {
		t.Fatalf("uploads main=%d down=%d", len(dest.uploads), down.uploads)
	}
	var s models.Server
	o.DB.First(&s)
	j2, _ := o.Engine.Enqueue(KindRotate, &s.ID, RotateParams{Reason: "test"}, "test")
	if got := waitJob(t, o, j2.ID); got.Status != models.JobDone {
		t.Fatalf("rotate %s: %s", got.Status, got.Error)
	}
	if len(dest.replaced) != 1 || down.replaces != 1 || down.uploads != 2 {
		t.Fatalf("main replaced=%d, down replaces=%d uploads=%d", len(dest.replaced), down.replaces, down.uploads)
	}
}

// recDest records every upload request.
type recDest struct {
	reqs     []destination.UploadRequest
	replaces int
}

func (d *recDest) Upload(_ context.Context, in destination.UploadRequest) error {
	d.reqs = append(d.reqs, in)
	return nil
}
func (d *recDest) ReplaceAddress(context.Context, string, string, string) (*destination.ReplaceResult, error) {
	d.replaces++
	return &destination.ReplaceResult{Updated: 2}, nil
}
func (d *recDest) SetActive(context.Context, string, bool) error { return nil }

// A "both" panel gets a normal and an ads set, but is moved only once.
func TestBothAdsModes(t *testing.T) {
	o, _, _, _, acc, _ := setup(t)
	both := &recDest{}
	o.Dests = func() []NamedDest { return []NamedDest{{Name: "both", Ads: []bool{false, true}, Destination: both}} }
	j, _ := o.Engine.Enqueue(KindProvision, nil, ProvisionParams{AccountID: acc.ID}, "test")
	if got := waitJob(t, o, j.ID); got.Status != models.JobDone {
		t.Fatalf("provision %s: %s", got.Status, got.Error)
	}
	if len(both.reqs) != 2 || both.reqs[0].Ads || !both.reqs[1].Ads || both.reqs[1].BaseName != both.reqs[0].BaseName+"-ads" {
		t.Fatalf("uploads: %+v", both.reqs)
	}
	var s models.Server
	o.DB.First(&s)
	j2, _ := o.Engine.Enqueue(KindRotate, &s.ID, RotateParams{Reason: "test"}, "test")
	if got := waitJob(t, o, j2.ID); got.Status != models.JobDone {
		t.Fatalf("rotate %s: %s", got.Status, got.Error)
	}
	if both.replaces != 1 || len(both.reqs) != 2 {
		t.Fatalf("replaces=%d uploads=%d", both.replaces, len(both.reqs))
	}
}
