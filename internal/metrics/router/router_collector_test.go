/*
Copyright (c) 2026 OpenInfra Foundation Europe. All rights reserved.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package router

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/nordix/meridio-2/internal/bird"
)

const (
	testGateway = "gw-a"
	testPrefix  = "meridio_2"

	// emptyBird is a successful birdc response from an instance with nothing configured.
	emptyBird = "BIRD 3.2.3 ready.\n"
)

// fakeRunner stands in for the `birdc` subprocess, returning canned output per command.
//
// Mocking at this boundary rather than at the parsed-struct level means the real parsers in
// internal/bird run, so these tests cover command construction, parsing and metric emission as one
// path. There are no hand-written []ProtocolStatus fixtures that could drift from what the parser
// actually produces. What it cannot cover is drift between these fixtures and what real BIRD emits;
// only an e2e run against a live BIRD closes that.
type fakeRunner struct {
	protocolsOut string
	protocolsErr error
	bfdOut       string
	bfdErr       error
}

func (f fakeRunner) Run(_ context.Context, args ...string) ([]byte, error) {
	switch {
	case slices.Contains(args, "protocols"):
		return []byte(f.protocolsOut), f.protocolsErr
	case slices.Contains(args, "bfd"):
		return []byte(f.bfdOut), f.bfdErr
	default:
		return nil, errors.New("unexpected birdc command: " + strings.Join(args, " "))
	}
}

func newCollector(r fakeRunner) *RouterCollector {
	return NewRouterCollector(
		bird.NewClient("/test/bird.ctl", bird.WithCommandRunner(r)),
		5*time.Second, testGateway, testPrefix,
	)
}

// protocolsOutput is synthetic `birdc show protocols all "NBR-*"` output. Its structure (column
// order, indentation, blank-line separated blocks, the dual-channel shape with a REJECTed channel)
// follows observed BIRD 3.2.3 output, but the names, counts and timestamps are made up. The Since
// values are epochs, as `timeformat protocol "%s"` in the generated bird.conf produces — that
// directive is new and this representation has not yet been confirmed against a live BIRD.
//
// establishedFor sets how long ago the BGP session came up, since the collector derives its uptime
// from Since. Covers a BGP router with an active ipv4 channel plus a disabled ipv6 channel, and a
// Static router (which must produce no bgp_* series).
func protocolsOutput(establishedFor time.Duration) string {
	since := time.Now().Add(-establishedFor).Unix()
	return fmt.Sprintf(`BIRD 3.2.3 ready.
Name       Proto      Table      State  Since         Info
NBR-gw-bgp-v4 BGP        ---        up     %d  Established
  BGP state:          Established
  Channel ipv4
    Input filter:   gateway_routes
    Output filter:  announced_routes
    Routes:         3 imported, 2 exported, 3 preferred
  Channel ipv6
    Input filter:   REJECT
    Output filter:  REJECT
    Routes:         0 imported, 0 exported, 0 preferred

NBR-gw-static-v4 Static     master4    up     %d
  Channel ipv4
    Routes:         1 imported, 0 exported, 1 preferred`, since, since)
}

// bfdOutput is synthetic `birdc show bfd sessions` output, same caveats as protocolsOutput. Its
// Since values are fixed, as nothing derives a duration from them.
const bfdOutput = `BIRD 3.2.3 ready.
IP address                Interface  State      Since         Interval  Timeout
169.254.100.150           vlan-100   Up         1759518307      0.300    1.500
fd00::150                 vlan-100   Down       1759518301      0.300    1.500`

func TestRouterCollector_SessionMetrics(t *testing.T) {
	collector := newCollector(fakeRunner{protocolsOut: protocolsOutput(time.Hour), bfdOut: bfdOutput})

	// The BGP router reports established with its channel route counts summed (the disabled ipv6
	// channel contributes 0). The Static router yields no bgp_* series at all — its connectivity is
	// observed via BFD. bfd_session_up is keyed by neighbor+interface and reports both sessions,
	// including the one that is down. Uptime is asserted separately, being time-dependent.
	expected := `
# HELP meridio_2_bfd_session_up Whether the BFD session to the given neighbor on the given interface is up (1) or not (0), as reported by BIRD.
# TYPE meridio_2_bfd_session_up gauge
meridio_2_bfd_session_up{gateway="gw-a",interface="vlan-100",neighbor="169.254.100.150"} 1
meridio_2_bfd_session_up{gateway="gw-a",interface="vlan-100",neighbor="fd00::150"} 0
# HELP meridio_2_bgp_prefixes_announced Number of prefixes announced to the given GatewayRouter, as reported by BIRD.
# TYPE meridio_2_bgp_prefixes_announced gauge
meridio_2_bgp_prefixes_announced{gateway="gw-a",gateway_router="gw-bgp-v4"} 2
# HELP meridio_2_bgp_routes_imported Number of routes imported from the given GatewayRouter, as reported by BIRD.
# TYPE meridio_2_bgp_routes_imported gauge
meridio_2_bgp_routes_imported{gateway="gw-a",gateway_router="gw-bgp-v4"} 3
# HELP meridio_2_bgp_session_up Whether the BGP session to the given GatewayRouter is established (1) or not (0), as reported by BIRD.
# TYPE meridio_2_bgp_session_up gauge
meridio_2_bgp_session_up{gateway="gw-a",gateway_router="gw-bgp-v4"} 1
`
	require.NoError(t, testutil.CollectAndCompare(collector, strings.NewReader(expected),
		testPrefix+"_bgp_session_up", testPrefix+"_bgp_routes_imported",
		testPrefix+"_bgp_prefixes_announced", testPrefix+"_bfd_session_up"))
}

// TestRouterCollector_SessionUptime covers the uptime gauge. The collector computes it as
// now - Since rather than reporting Since directly, so there are three cases:
//
//   - established: the value is the time the session has been up;
//   - not established: 0, since Since no longer marks a session start;
//   - established but Since unparseable: no series at all, as 0 would look like a fresh reset.
func TestRouterCollector_SessionUptime(t *testing.T) {
	t.Run("established reports seconds since the session came up", func(t *testing.T) {
		collector := newCollector(fakeRunner{
			protocolsOut: protocolsOutput(90 * time.Minute),
			bfdOut:       bfdOutput,
		})

		got, ok := uptimeGauge(t, collector)
		require.True(t, ok, "expected an uptime series")
		// Allow a small window for the time between fixture construction and collection.
		require.InDelta(t, (90 * time.Minute).Seconds(), got, 5)
	})

	t.Run("not established reports zero", func(t *testing.T) {
		// A session in Connect still carries a Since — the moment it entered that state — so this
		// is exactly the case where treating Since as a session start would be wrong.
		connecting := `BIRD 3.2.3 ready.
Name       Proto      Table      State  Since         Info
NBR-gw-bgp-v4 BGP        ---        start  ` + strconv.FormatInt(time.Now().Add(-time.Hour).Unix(), 10) + `  Connect`

		collector := newCollector(fakeRunner{protocolsOut: connecting, bfdOut: emptyBird})

		got, ok := uptimeGauge(t, collector)
		require.True(t, ok, "expected an uptime series reporting zero, not an absent series")
		require.Equal(t, float64(0), got)
	})

	t.Run("established but unparseable Since omits the series", func(t *testing.T) {
		// BIRD's default timeformat, i.e. bird.conf is missing `timeformat protocol "%s"`. Emitting
		// 0 here would be indistinguishable from a session that just reset, so nothing is emitted.
		humanReadable := `BIRD 3.2.3 ready.
Name       Proto      Table      State  Since         Info
NBR-gw-bgp-v4 BGP        ---        up     08:25:11.859  Established`

		collector := newCollector(fakeRunner{protocolsOut: humanReadable, bfdOut: emptyBird})

		require.Equal(t, 1, testutil.CollectAndCount(collector, testPrefix+"_bgp_session_up"),
			"the session itself must still be reported")
		_, ok := uptimeGauge(t, collector)
		require.False(t, ok, "expected no uptime series when Since is unparseable")
	})
}

// uptimeGauge collects and returns the value of the single uptime series, plus whether it was
// emitted at all. Needed because CollectAndCompare requires an exact expected value, which a
// clock-derived gauge cannot provide.
func uptimeGauge(t *testing.T, collector *RouterCollector) (float64, bool) {
	t.Helper()
	reg := prometheus.NewPedanticRegistry()
	require.NoError(t, reg.Register(collector))

	families, err := reg.Gather()
	require.NoError(t, err)
	for _, mf := range families {
		if mf.GetName() != testPrefix+"_bgp_session_uptime_seconds" {
			continue
		}
		require.Len(t, mf.GetMetric(), 1, "expected exactly one uptime series")
		return mf.GetMetric()[0].GetGauge().GetValue(), true
	}
	return 0, false
}

func TestRouterCollector_CollectSuccess(t *testing.T) {
	queryFailed := errors.New("exit status 1")

	tests := []struct {
		name         string
		runner       fakeRunner
		wantBGPOK    string
		wantBFDOK    string
		wantNoSeries bool
	}{
		{
			name:      "both sources readable",
			runner:    fakeRunner{protocolsOut: protocolsOutput(time.Hour), bfdOut: bfdOutput},
			wantBGPOK: "1",
			wantBFDOK: "1",
		},
		{
			// Empty output is a successful read of a BIRD with nothing configured: success stays 1
			// while no session series are emitted. This is the distinction the gauge exists for.
			name:         "both readable but empty",
			runner:       fakeRunner{protocolsOut: emptyBird, bfdOut: emptyBird},
			wantBGPOK:    "1",
			wantBFDOK:    "1",
			wantNoSeries: true,
		},
		{
			// The two queries are independent: a failing protocols query must not suppress BFD.
			name:      "protocols query fails, bfd succeeds",
			runner:    fakeRunner{protocolsErr: queryFailed, bfdOut: bfdOutput},
			wantBGPOK: "0",
			wantBFDOK: "1",
		},
		{
			name:         "both queries fail",
			runner:       fakeRunner{protocolsErr: queryFailed, bfdErr: queryFailed},
			wantBGPOK:    "0",
			wantBFDOK:    "0",
			wantNoSeries: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			collector := newCollector(tt.runner)

			expected := `
# HELP meridio_2_router_metrics_collect_success Whether this scrape successfully gathered the given source from BIRD (1) or not (0). Reports only the health of serving these metrics: it is independent of BGP/BFD session state and of the router's own BIRD monitoring, so 0 means the data could not be read, not that connectivity is down.
# TYPE meridio_2_router_metrics_collect_success gauge
meridio_2_router_metrics_collect_success{gateway="gw-a",source="bgp_sessions"} ` + tt.wantBGPOK + `
meridio_2_router_metrics_collect_success{gateway="gw-a",source="bfd_sessions"} ` + tt.wantBFDOK + `
`
			require.NoError(t, testutil.CollectAndCompare(collector, strings.NewReader(expected),
				testPrefix+"_router_metrics_collect_success"))

			if tt.wantNoSeries {
				// A failed fetch must emit nothing rather than forging zeros, which would read as
				// "every session down".
				require.Equal(t, 0, testutil.CollectAndCount(collector,
					testPrefix+"_bgp_session_up", testPrefix+"_bfd_session_up"))
			}
		})
	}
}

// TestRouterCollector_OmitsUnreportedRouteCounts verifies that route counts are omitted, not
// zero-filled, when BIRD does not report them: a BGP protocol that is not up has no detail block,
// so the parsed counts are nil rather than 0. Emitting 0 would be indistinguishable from an
// established session with a genuinely empty routing table.
func TestRouterCollector_OmitsUnreportedRouteCounts(t *testing.T) {
	const connecting = `BIRD 3.2.3 ready.
Name       Proto      Table      State  Since         Info
NBR-gw-bgp-v4 BGP        ---        start  1759518307  Connect`

	collector := newCollector(fakeRunner{protocolsOut: connecting, bfdOut: emptyBird})

	expected := `
# HELP meridio_2_bgp_session_up Whether the BGP session to the given GatewayRouter is established (1) or not (0), as reported by BIRD.
# TYPE meridio_2_bgp_session_up gauge
meridio_2_bgp_session_up{gateway="gw-a",gateway_router="gw-bgp-v4"} 0
`
	require.NoError(t, testutil.CollectAndCompare(collector, strings.NewReader(expected),
		testPrefix+"_bgp_session_up"))

	require.Equal(t, 0, testutil.CollectAndCount(collector,
		testPrefix+"_bgp_routes_imported", testPrefix+"_bgp_prefixes_announced"))
}
