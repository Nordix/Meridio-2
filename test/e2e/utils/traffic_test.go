//go:build e2e
// +build e2e

package utils

import (
	"strings"
	"testing"
)

// Real `birdc show route for <prefix> all` output captured from the VPN gateway
// (BIRD 3.1.2) with the separate-appnetwork suite deployed. Note BIRD returns the
// longest-matching route, so an absent prefix yields only the 0.0.0.0/0 default.

const showRoutePresentVIP = `BIRD 3.1.2 ready.
Table master4:
0.0.0.0/0            blackhole [DEFAULT4 07:31:28.285] * (110)
	preference: 110
	source: static
	Internal route handling values: 0L 4G 0S id 1
10.0.0.1/32          unicast [GW4_A1_1 07:31:58.426] * (100) [AS64512i]
	via 169.254.10.1 on vlan1
	preference: 100
	igp_metric: 0
	from: 169.254.10.1
	source: BGP
	bgp_origin: IGP
	bgp_path: 64512
	bgp_next_hop: 169.254.10.1
	bgp_local_pref: 100
	Internal route handling values: 0L 8G 0S id 2
                     unicast [GW4_A1_2 07:31:58.427] (100) [AS64512i]
	via 169.254.10.2 on vlan1
	preference: 100
	igp_metric: 0
	from: 169.254.10.2
	source: BGP
	bgp_origin: IGP
	bgp_path: 64512
`

// Output for an absent prefix: BIRD falls back to the longest match (default).
const showRouteAbsentVIP = `BIRD 3.1.2 ready.
Table master4:
0.0.0.0/0            blackhole [DEFAULT4 07:31:28.285] * (110)
	preference: 110
	source: static
	Internal route handling values: 0L 4G 0S id 1
`

func TestExactPrefixBlock_Present(t *testing.T) {
	block, found := exactPrefixBlock(showRoutePresentVIP, "10.0.0.1/32")
	if !found {
		t.Fatalf("expected to find block for 10.0.0.1/32")
	}
	if strings.Contains(block, "0.0.0.0/0") {
		t.Errorf("block should not include the default route entry:\n%s", block)
	}
	if !strings.Contains(block, "source: BGP") {
		t.Errorf("block should include BGP source:\n%s", block)
	}
	// Both ECMP paths must be captured.
	if got := strings.Count(block, "via "); got != 2 {
		t.Errorf("expected 2 next-hop lines, got %d:\n%s", got, block)
	}
}

func TestExactPrefixBlock_Absent(t *testing.T) {
	// The exact VIP is absent; only the default fallback is present.
	_, found := exactPrefixBlock(showRouteAbsentVIP, "99.99.99.99/32")
	if found {
		t.Errorf("expected no block for absent prefix 99.99.99.99/32")
	}
	// Sanity: the default route itself is findable by its own prefix.
	if _, ok := exactPrefixBlock(showRouteAbsentVIP, "0.0.0.0/0"); !ok {
		t.Errorf("expected to find the default route block")
	}
}

func TestParseVPNGatewayRouteNextHops(t *testing.T) {
	block, _ := exactPrefixBlock(showRoutePresentVIP, "10.0.0.1/32")
	var nextHops []string
	for _, line := range strings.Split(block, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "via ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			nextHops = append(nextHops, fields[1])
		}
	}
	want := []string{"169.254.10.1", "169.254.10.2"}
	if len(nextHops) != len(want) {
		t.Fatalf("expected %v, got %v", want, nextHops)
	}
	for i := range want {
		if nextHops[i] != want[i] {
			t.Errorf("next-hop[%d]: expected %s, got %s", i, want[i], nextHops[i])
		}
	}
}
