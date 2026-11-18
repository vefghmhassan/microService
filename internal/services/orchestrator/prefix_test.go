package orchestrator

import (
	"net"
	"strings"
	"testing"
)

func TestRandomInPrefixStaysInTheSlash64(t *testing.T) {
	cur := "2a01:4ff:2f0:e27::1"
	_, subnet, _ := net.ParseCIDR("2a01:4ff:2f0:e27::/64")
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		got, err := randomInPrefix(cur)
		if err != nil {
			t.Fatal(err)
		}
		ip := net.ParseIP(got)
		if !subnet.Contains(ip) || got == cur || isLowSuffix(ip) {
			t.Fatalf("bad address %s", got)
		}
		seen[got] = true
	}
	if len(seen) < 45 {
		t.Errorf("addresses repeat: %d distinct of 50", len(seen))
	}
	if _, err := randomInPrefix("1.2.3.4"); err == nil {
		t.Error("an IPv4 address must be refused")
	}
}

func TestAddExtraV6ScriptQuotesTheAddress(t *testing.T) {
	s := strings.TrimSpace(strings.Replace(addExtraV6Script, "%q", `"2a01::5"`, 1))
	if !strings.Contains(s, `NEW="2a01::5"`) || strings.Contains(s, "%!") {
		t.Errorf("script not formed: %s", s[:80])
	}
}
