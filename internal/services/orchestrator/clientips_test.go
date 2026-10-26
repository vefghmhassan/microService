package orchestrator

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"microserver/internal/database"
	"microserver/internal/models"
)

const sampleAccessLog = `2026/10/26 21:00:00.100000 from 5.112.3.4:51234 accepted tcp:www.google.com:443 [inbound-2007 >> direct] email: alice
2026/10/26 21:03:00.200000 from tcp:5.112.3.4:51240 accepted tcp:www.google.com:443 [inbound-2007 >> direct] email: alice
2026/10/26 21:04:00.000000 from [2a01:5ec0::1]:4000 accepted udp:1.1.1.1:53 [inbound-80 -> direct] email: bob
2026/10/26 21:05:00.000000 from 1.2.3.4:1 rejected  proxy/vless/encoding: invalid request user id
2026/10/26 21:06:00.000000 from 37.98.1.1:9 accepted tcp:x.com:443 [inbound-80 >> direct]
`

// TestAccessLogScript runs the real remote script against a sample log with
// the local awk, then parses its output.
func TestAccessLogScript(t *testing.T) {
	if _, err := exec.LookPath("awk"); err != nil {
		t.Skip("no awk")
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "access.log")
	if err := os.WriteFile(log, []byte(sampleAccessLog), 0o644); err != nil {
		t.Fatal(err)
	}
	script := strings.Replace(accessLogScript, "F="+AccessLogPath, "F="+log, 1)
	out, err := exec.Command("sh", "-c", script).Output()
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(log); len(b) != 0 {
		t.Fatal("log must be truncated")
	}
	out = append([]byte("TZ +0330\n"), out[strings.Index(string(out), "\n")+1:]...)
	sum, err := parseAccessSummary(string(out))
	if err != nil {
		t.Fatal(err)
	}
	if sum.Size != fmt.Sprint(len(sampleAccessLog)) {
		t.Fatalf("size: %q", sum.Size)
	}
	rows := sum.Rows
	got := map[string]seenIP{}
	for _, r := range rows {
		got[r.IP+"|"+r.Email] = r
	}
	if len(got) != 3 {
		t.Fatalf("rows: %+v", rows)
	}
	a := got["5.112.3.4|alice"]
	if a.Conns != 2 || a.Tag != "inbound-2007" || a.Last.Format(time.RFC3339) != "2026-10-26T17:33:00Z" {
		t.Fatalf("alice: %+v", a)
	}
	if b := got["2a01:5ec0::1|bob"]; b.Conns != 1 || b.Tag != "inbound-80" {
		t.Fatalf("bob: %+v", b)
	}
	if _, ok := got["37.98.1.1|"]; !ok {
		t.Fatal("a line without email must still count")
	}
}

type fakeGeo map[string]models.IPGeo

func (f fakeGeo) Lookup(ip string) (models.IPGeo, bool) {
	g, ok := f[ip]
	g.IP = ip
	return g, ok
}

func TestStoreClientIPsUpsertsAndResolves(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(db); err != nil {
		t.Fatal(err)
	}
	geo := fakeGeo{"5.112.3.4": {Country: "IR", Region: "Tehran", City: "Tehran", ASN: 197207, ISP: "MCI"}}
	at := time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC)
	row := seenIP{IP: "5.112.3.4", Email: "alice", Tag: "inbound-2007", Conns: 3, First: at, Last: at}
	tags := map[string]string{"inbound-2007": "in-2007"}
	for i := 0; i < 2; i++ {
		row.Last = at.Add(time.Duration(i) * time.Minute)
		if err := StoreClientIPs(db, geo, 1, []seenIP{row}, tags); err != nil {
			t.Fatal(err)
		}
	}
	var recs []models.ClientIP
	db.Find(&recs)
	if len(recs) != 1 || recs[0].Conns != 6 || recs[0].Inbound != "in-2007" || !recs[0].LastSeen.Equal(at.Add(time.Minute)) {
		t.Fatalf("records: %+v", recs)
	}
	var g models.IPGeo
	if err := db.First(&g, "ip = ?", "5.112.3.4").Error; err != nil || g.City != "Tehran" || g.ISP != "MCI" {
		t.Fatalf("geo: %+v %v", g, err)
	}
}

func TestWithAccessLog(t *testing.T) {
	s := map[string]any{"log": map[string]any{"access": "none", "loglevel": "warning", "maskAddress": "quarter"}}
	if !withAccessLog(s) {
		t.Fatal("expected a change")
	}
	lg := s["log"].(map[string]any)
	if lg["access"] != AccessLogPath || lg["maskAddress"] != "" || lg["loglevel"] != "warning" {
		t.Fatalf("log: %v", lg)
	}
	if withAccessLog(s) {
		t.Fatal("second call must be a no-op")
	}
}
