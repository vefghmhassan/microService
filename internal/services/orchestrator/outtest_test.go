package orchestrator

import (
	"context"
	"encoding/base64"
	"os/exec"
	"strings"
	"testing"

	"microserver/internal/models"
)

func TestApplyFullTemplateReplacesSetting(t *testing.T) {
	p := &fakePanel{}
	snap := &Snapshot{Xray: models.XrayTemplate{
		Outbounds: `[{"tag":"ignored"}]`,
		Full:      `{"api":{"tag":"api"},"stats":{},"policy":{},"outbounds":[{"tag":"direct","protocol":"freedom"}],"custom":1}`,
	}}
	if err := Apply(context.Background(), p, snap, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	if p.setting["custom"] != float64(1) {
		t.Fatalf("full template not applied: %v", p.setting)
	}
	outs := p.setting["outbounds"].([]any)
	if len(outs) != 1 || outs[0].(map[string]any)["tag"] != "direct" {
		t.Fatalf("outbounds: %v", outs)
	}
	if lg := p.setting["log"].(map[string]any); lg["access"] != AccessLogPath {
		t.Fatalf("access log must stay on: %v", lg)
	}
}

func TestTestConfigRoutesEachTarget(t *testing.T) {
	all := []map[string]any{
		{"tag": "a", "protocol": "vless"},
		{"tag": "b", "protocol": "vmess", "proxySettings": map[string]any{"tag": "a"}},
	}
	cfg := testConfig(all, all[1:], 30000)
	ins := cfg["inbounds"].([]any)
	rules := cfg["routing"].(map[string]any)["rules"].([]any)
	if len(ins) != 1 || ins[0].(map[string]any)["port"] != 30000 || rules[0].(map[string]any)["outboundTag"] != "b" {
		t.Fatalf("cfg: %v", cfg)
	}
	if len(cfg["outbounds"].([]any)) != 2 {
		t.Fatal("chained outbounds must all be present")
	}
}

func TestParseTestOutput(t *testing.T) {
	targets := []map[string]any{{"tag": "a"}, {"tag": "b"}, {"tag": "c"}}
	body := base64.StdEncoding.EncodeToString([]byte(`{"status":"success","query":"1.2.3.4","country":"Germany","countryCode":"DE","city":"Falkenstein","isp":"Hetzner"}`))
	res, err := parseTestOutput(TestHTTP, "R 0 200 0.412 "+body+"\nR 1 000 10.0 \n", targets)
	if err != nil {
		t.Fatal(err)
	}
	if !res[0].OK || res[0].MS != 412 || res[0].CountryCode != "DE" || res[0].IP != "1.2.3.4" {
		t.Fatalf("a: %+v", res[0])
	}
	if res[1].OK || res[1].Error == "" || res[2].OK {
		t.Fatalf("b/c: %+v %+v", res[1], res[2])
	}
	tcp, _ := parseTestOutput(TestTCP, "R 0 ok 37\nR 1 fail timeout\n", targets[:2])
	if !tcp[0].OK || tcp[0].MS != 37 || tcp[1].OK {
		t.Fatalf("tcp: %+v", tcp)
	}
	if _, err := parseTestOutput(TestReal, "ERR failed to start\n", targets); err == nil || !strings.Contains(err.Error(), "failed to start") {
		t.Fatalf("expected xray error, got %v", err)
	}
}

func TestTCPScriptSkipsUnsafeHosts(t *testing.T) {
	s, _ := tcpScript([]map[string]any{
		{"tag": "x", "settings": map[string]any{"servers": []any{map[string]any{"address": "a.com;rm -rf /", "port": float64(1)}}}},
		{"tag": "y", "settings": map[string]any{"vnext": []any{map[string]any{"address": "1.2.3.4", "port": float64(443)}}}},
	})
	if strings.Contains(s, "rm -rf") || !strings.Contains(s, "/dev/tcp/1.2.3.4/443") {
		t.Fatalf("script:\n%s", s)
	}
}

func TestScriptsParseInBash(t *testing.T) {
	all := []map[string]any{{"tag": "a", "protocol": "vless", "settings": map[string]any{"vnext": []any{map[string]any{"address": "1.2.3.4", "port": float64(443)}}}}}
	p, _ := proxyScript(TestHTTP, all, all, 30000)
	tc, _ := tcpScript(all)
	for _, s := range []string{p, tc} {
		cmd := exec.Command("bash", "-n")
		cmd.Stdin = strings.NewReader(s)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %s\n%s", err, out, s)
		}
	}
}
