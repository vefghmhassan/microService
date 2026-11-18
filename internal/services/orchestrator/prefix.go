package orchestrator

import (
	"context"
	"crypto/rand"
	"fmt"
	"net"
	"strconv"
	"strings"

	"microserver/internal/jobs"
	"microserver/internal/models"
	"microserver/internal/services/providers"
	"microserver/internal/services/settings"
)

// maxPrefixRotations bounds in-/64 rotations per job. If several addresses of
// one /64 are filtered, the whole prefix probably is, and only a new server
// gets another.
const maxPrefixRotations = 2

// prefixRotation tells whether the server can change its IPv6 by taking
// another address of its own /64 instead of being rebuilt. Hetzner routes the
// whole /64 to the server, so any address in it works once it is on the NIC.
func (o *Orchestrator) prefixRotation(r *jobs.Run, prov providers.Provider, s *models.Server) bool {
	if s.Family() != models.FamilyV6 || prov.Type() != "hetzner" || s.IPv4 == "" || s.IPv6 == "" {
		return false
	}
	if !o.Settings.Bool(settings.RotateV6InPrefix) {
		return false
	}
	return prefixRotations(r) < maxPrefixRotations
}

func prefixRotations(r *jobs.Run) int {
	n, _ := strconv.Atoi(r.GetString("prefix_rotations"))
	return n
}

// randomInPrefix returns a random address in cur's /64, other than cur.
func randomInPrefix(cur string) (string, error) {
	ip := net.ParseIP(cur)
	if ip == nil || ip.To4() != nil {
		return "", fmt.Errorf("%q is not an IPv6 address", cur)
	}
	out := make(net.IP, 16)
	copy(out, ip.To16())
	for {
		if _, err := rand.Read(out[8:]); err != nil {
			return "", err
		}
		// Skip the subnet-router anycast, the usual ::1 and the current one.
		if out.Equal(ip) || isLowSuffix(out) {
			continue
		}
		return out.String(), nil
	}
}

func isLowSuffix(ip net.IP) bool {
	for _, b := range ip[8:15] {
		if b != 0 {
			return false
		}
	}
	return true
}

// addExtraV6Script puts the address on the default NIC now and, where netplan
// is used, keeps it across reboots. The previous extra address is removed.
const addExtraV6Script = `set -e
NEW=%q
IF=$(ip -6 route show default | awk '{for(i=1;i<=NF;i++) if($i=="dev"){print $(i+1); exit}}')
[ -n "$IF" ] || IF=$(ip -o route get 1.1.1.1 | awk '{for(i=1;i<=NF;i++) if($i=="dev"){print $(i+1); exit}}')
[ -n "$IF" ] || { echo "no network interface found"; exit 1; }
OLD=$(cat /etc/ms-extra-v6 2>/dev/null || true)
if [ -n "$OLD" ]; then ip -6 addr del "$OLD/64" dev "$IF" 2>/dev/null || true; fi
ip -6 addr add "$NEW/64" dev "$IF" nodad
echo "$NEW" > /etc/ms-extra-v6
if [ -d /etc/netplan ]; then
  F=/etc/netplan/60-ms-extra-v6.yaml
  printf 'network:\n  version: 2\n  ethernets:\n    %%s:\n      addresses: ["%%s/64"]\n' "$IF" "$NEW" > "$F"
  chmod 600 "$F"
  netplan generate >/dev/null 2>&1 || rm -f "$F"
fi
echo "added $NEW on $IF"
`

// rotateInPrefix moves the server to a new address of its own /64. SSH keeps
// using the IPv4, so the connection survives the change.
func (o *Orchestrator) rotateInPrefix(ctx context.Context, r *jobs.Run, s *models.Server) (*providers.RotateResult, error) {
	next, err := randomInPrefix(s.IPv6)
	if err != nil {
		return nil, err
	}
	remote, _, err := o.Connect(ctx, s)
	if err != nil {
		return nil, fmt.Errorf("ssh: %w", err)
	}
	defer remote.Close()
	out, err := remote.Run(ctx, fmt.Sprintf(addExtraV6Script, next), nil)
	if err != nil {
		return nil, fmt.Errorf("add %s: %w (%s)", next, err, strings.TrimSpace(out))
	}
	old := s.IPv6
	s.IPv6 = next
	s.RotateCount++
	o.save(r, s)
	r.Set("prefix_rotations", strconv.Itoa(prefixRotations(r)+1))
	r.Info("new IPv6 %s in the same /64 (old %s)", next, old)
	return &providers.RotateResult{OldIP: old}, nil
}
