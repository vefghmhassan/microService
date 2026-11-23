package orchestrator

import (
	"testing"

	"microserver/internal/models"
)

func TestPanelBaseNameNumbersPerLocationPrefix(t *testing.T) {
	o, _, _, _, acc, _ := setup(t)
	loc := &models.Location{Priority: 1, ProviderAccountID: acc.ID, Region: "fsn1", Plan: "p", Enabled: true, NamePrefix: "sama"}
	o.DB.Create(loc)
	plain := &models.Location{Priority: 2, ProviderAccountID: acc.ID, Region: "nbg1", Plan: "p", Enabled: true}
	o.DB.Create(plain)
	mk := func(locID uint) *models.Server {
		s := &models.Server{Name: "ms", ProviderAccountID: acc.ID, LocationID: locID, Status: models.ServerActive}
		o.DB.Create(s)
		return s
	}

	a, b, c := mk(loc.ID), mk(loc.ID), mk(loc.ID)
	if got := o.panelBaseName(a); got != "sama-1" {
		t.Errorf("first: %q", got)
	}
	if got := o.panelBaseName(b); got != "sama-2" {
		t.Errorf("second: %q", got)
	}
	if got := o.panelBaseName(a); got != "sama-1" {
		t.Errorf("a server keeps its name: %q", got)
	}
	// A deleted server frees its number.
	o.DB.Model(a).Update("status", models.ServerDeleted)
	if got := o.panelBaseName(c); got != "sama-1" {
		t.Errorf("freed number: %q", got)
	}
	if got := o.panelBaseName(mk(plain.ID)); got != "" {
		t.Errorf("no prefix must fall back to the default, got %q", got)
	}
	if got := o.panelBaseName(mk(0)); got != "" {
		t.Errorf("manual server: %q", got)
	}
}

func TestValidNamePrefix(t *testing.T) {
	for _, ok := range []string{"", "sama", "sama-de", "a_b1"} {
		if err := ValidNamePrefix(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"sama-", "-x", "a b", "سما", "a.b"} {
		if ValidNamePrefix(bad) == nil {
			t.Errorf("%q should be refused", bad)
		}
	}
}
