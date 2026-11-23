package orchestrator

import (
	"strings"
	"testing"

	"microserver/internal/models"
)

const subSample = `[
  {"remarks":"a","outbounds":[{"tag":"proxy","protocol":"vless","settings":{"address":"1.2.3.4","port":443}}]},
  {"remarks":"b","outbounds":[{"tag":"proxy","protocol":"shadowsocks","settings":{"servers":[{"address":"1.2.3.4","port":8388}]}}]},
  {"remarks":"c","outbounds":[{"tag":"proxy","protocol":"vmess","settings":{"vnext":[{"address":"1.2.3.4","port":9999}]}}]}
]`

func TestSplitPanelJSONCompact(t *testing.T) {
	got, err := splitPanelJSON([]byte(subSample))
	if err != nil || len(got) != 3 {
		t.Fatalf("got %d, %v", len(got), err)
	}
	for _, g := range got {
		if strings.ContainsAny(g, "\n ") {
			t.Fatalf("not compact: %q", g)
		}
	}
	if _, err := splitPanelJSON([]byte(`[]`)); err == nil {
		t.Fatal("empty subscription must be an error")
	}
	if _, err := splitPanelJSON([]byte(`<html>`)); err == nil {
		t.Fatal("non-JSON must be an error")
	}
}

func TestConfigPortAndFilter(t *testing.T) {
	all, _ := splitPanelJSON([]byte(subSample))
	want := []int{443, 8388, 9999}
	for i, c := range all {
		if p := configPort(c); p != want[i] {
			t.Fatalf("config %d port %d, want %d", i, p, want[i])
		}
	}
	snap := &Snapshot{Inbounds: []models.InboundTemplate{
		{Port: 443, Enabled: true, Upload: true},
		{Port: 8388, Enabled: true, Upload: false},
		{Port: 9999, Enabled: false, Upload: true},
	}}
	got := filterPanelJSON(snap, all)
	if len(got) != 1 || configPort(got[0]) != 443 {
		t.Fatalf("filter kept %d configs: %v", len(got), got)
	}
}
