package geoip

import "testing"

func TestClean(t *testing.T) {
	for in, want := range map[string]string{
		"Tehrān":              "Tehran",
		"Tehran (District 6)": "Tehran",
		"Bushehr (Maḩalleh-ye Sangī-ye Gharbī)": "Bushehr",
		"Sistan and Baluchestan":                "Sistan and Baluchestan",
	} {
		if got := clean(in); got != want {
			t.Errorf("clean(%q) = %q, want %q", in, got, want)
		}
	}
}
