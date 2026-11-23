package geo

import (
	"testing"

	"microserver/internal/services/providers"
)

func nearestID(t *testing.T, srcType, srcRegion, srcCountry, dstType string, opts []providers.Option) string {
	t.Helper()
	src, how := Locate(srcType, srcRegion, "", srcCountry)
	if how == "" {
		t.Fatalf("could not place %s/%s", srcType, srcRegion)
	}
	r := Nearest(src, dstType, opts)
	if len(r) == 0 {
		t.Fatal("no regions ranked")
	}
	return r[0].ID
}

func TestHetznerToVultr(t *testing.T) {
	opts := []providers.Option{
		{ID: "ewr", Name: "New Jersey (ewr)", Country: "US"},
		{ID: "ams", Name: "Amsterdam (ams)", Country: "NL"},
		{ID: "fra", Name: "Frankfurt (fra)", Country: "DE"},
		{ID: "sgp", Name: "Singapore (sgp)", Country: "SG"},
	}
	if got := nearestID(t, "hetzner", "fsn1", "DE", "vultr", opts); got != "fra" {
		t.Fatalf("fsn1 → vultr: got %s, want fra", got)
	}
	if got := nearestID(t, "hetzner", "sin", "SG", "vultr", opts); got != "sgp" {
		t.Fatalf("sin → vultr: got %s, want sgp", got)
	}
}

func TestHetznerToDigitalOcean(t *testing.T) {
	// DigitalOcean reports no country: the slug alone places it.
	opts := []providers.Option{
		{ID: "nyc3", Name: "New York 3 (nyc3)"},
		{ID: "sgp1", Name: "Singapore 1 (sgp1)"},
		{ID: "ams3", Name: "Amsterdam 3 (ams3)"},
		{ID: "fra1", Name: "Frankfurt 1 (fra1)"},
		{ID: "lon1", Name: "London 1 (lon1)"},
	}
	got := nearestID(t, "hetzner", "hel1", "FI", "digitalocean", opts)
	if got != "ams3" && got != "fra1" {
		t.Fatalf("hel1 → digitalocean: got %s, want a northern-EU region", got)
	}
}

func TestOVHAndUnknownRegions(t *testing.T) {
	// OVH region names carry a number; XZone codes are unknown and are placed
	// by city name, then by country.
	if pt, how := Locate("ovh", "GRA11", "", ""); how == "" || pt != gravelines {
		t.Fatalf("GRA11: got %+v by %q", pt, how)
	}
	if pt, how := Locate("ovh", "DE1", "", ""); how != ByRegion || pt != frankfurt {
		t.Fatalf("GRA11: got %+v by %q", pt, how)
	}
	if pt, how := Locate("xzone", "x-81", "Frankfurt Premium", "DE"); how != ByCity || pt != frankfurt {
		t.Fatalf("xzone city: got %+v by %q", pt, how)
	}
	if pt, how := Locate("xzone", "x-99", "Premium 2", "TR"); how != ByCountry || pt.Country != "TR" {
		t.Fatalf("xzone country: got %+v by %q", pt, how)
	}
	if _, how := Locate("xzone", "x-1", "", ""); how != "" {
		t.Fatalf("unplaceable region placed by %q", how)
	}
}

func TestUnplacedRanksLast(t *testing.T) {
	src, _ := Locate("hetzner", "fsn1", "", "DE")
	r := Nearest(src, "xzone", []providers.Option{
		{ID: "a", Name: "mystery"},
		{ID: "b", Name: "Amsterdam", Country: "NL"},
	})
	if r[0].ID != "b" || r[1].Match != "" {
		t.Fatalf("got %+v", r)
	}
}
