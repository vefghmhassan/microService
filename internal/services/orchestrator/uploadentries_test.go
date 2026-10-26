package orchestrator

import (
	"strings"
	"testing"

	"microserver/internal/models"
)

func TestUploadEntriesJSONPerInbound(t *testing.T) {
	settings := `{"clients":[{"id":"11111111-1111-1111-1111-111111111111","email":"a"}],"decryption":"none"}`
	stream := `{"network":"tcp","security":"none"}`
	snap := &Snapshot{Inbounds: []models.InboundTemplate{
		{Remark: "plain", Protocol: "vless", Port: 2001, Enabled: true, Upload: true, Settings: settings, StreamSettings: stream},
		{Remark: "json", Protocol: "vless", Port: 2002, Enabled: true, Upload: true, UploadJSON: true, Settings: settings, StreamSettings: stream},
		{Remark: "off", Protocol: "vless", Port: 2003, Enabled: true, Upload: false, UploadJSON: true, Settings: settings, StreamSettings: stream},
	}}
	got, err := UploadEntries(snap, "9.9.9.9")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !strings.HasPrefix(got[0], "vless://") || !strings.HasPrefix(got[1], "{") || strings.Contains(got[1], "\n") {
		t.Fatalf("entries: %q", got)
	}
}
