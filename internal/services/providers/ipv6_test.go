package providers

import (
	"encoding/json"
	"testing"
)

func TestHostIPv6(t *testing.T) {
	for in, want := range map[string]string{
		"2a01:4f8:c17:3f5::/64":              "2a01:4f8:c17:3f5::1",
		"2600:3c01::f03c:91ff:fe24:3a2f/128": "2600:3c01::f03c:91ff:fe24:3a2f",
		"2604:a880:400:d0::1b2:6001":         "2604:a880:400:d0::1b2:6001",
		"2a01:4f8:c17:3f5::5/64":             "2a01:4f8:c17:3f5::5",
		"203.0.113.7":                        "",
		"":                                   "",
		"garbage":                            "",
	} {
		if got := hostIPv6(in); got != want {
			t.Errorf("hostIPv6(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHetznerInstanceIPv6(t *testing.T) {
	var s hzServer
	raw := `{"id":42,"name":"srv","status":"running","public_net":{
		"ipv4":{"id":9,"ip":"203.0.113.7"},
		"ipv6":{"id":10,"ip":"2a01:4f8:c17:3f5::/64"}}}`
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		t.Fatal(err)
	}
	inst := s.instance()
	if inst.IPv4 != "203.0.113.7" || inst.IPv6 != "2a01:4f8:c17:3f5::1" {
		t.Fatalf("got v4 %q v6 %q", inst.IPv4, inst.IPv6)
	}
}

func TestLinodeInstanceIPv6(t *testing.T) {
	var s linodeInstance
	raw := `{"id":7,"label":"srv","status":"running","region":"eu-central",
		"ipv4":["192.168.130.4","172.105.1.2"],"ipv6":"2600:3c01::f03c:91ff:fe24:3a2f/128"}`
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		t.Fatal(err)
	}
	inst := s.instance()
	if inst.IPv4 != "172.105.1.2" || inst.IPv6 != "2600:3c01::f03c:91ff:fe24:3a2f" {
		t.Fatalf("got v4 %q v6 %q", inst.IPv4, inst.IPv6)
	}
}
