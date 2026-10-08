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

package loadbalancer

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/nordix/meridio-2/internal/nfqlb"
)

const (
	testGateway = "gw-a"
	testPrefix  = "meridio_2"
)

// fakeStats is a StatsReader test double.
type fakeStats struct {
	flows      []nfqlb.FlowMatch
	flowsErr   error
	targets    map[string]int
	targetsErr error
}

func (f *fakeStats) FlowMatches(_ context.Context) ([]nfqlb.FlowMatch, error) {
	return f.flows, f.flowsErr
}

func (f *fakeStats) ActiveTargets(_ context.Context) (map[string]int, error) {
	return f.targets, f.targetsErr
}

func newTestCollector(stats StatsReader) *Collector {
	return NewCollector(stats, testGateway, testPrefix, time.Second, newFakeCollectorErrors())
}

func newTestCollectorWithErrs(stats StatsReader, errs errorRecorder) *Collector {
	return NewCollector(stats, testGateway, testPrefix, time.Second, errs)
}

func TestCollector_FlowMatchesAndActiveTargets(t *testing.T) {
	stats := &fakeStats{
		flows: []nfqlb.FlowMatch{
			{Name: "route-a", MatchesCount: 42},
			{Name: "route-b", MatchesCount: 0},
		},
		targets: map[string]int{"dg-a": 3},
	}
	c := newTestCollector(stats)

	expected := `
# HELP meridio_2_lb_flow_matches_total Number of packets matched per L34Route flow by nfqlb (from flow-list matches_count). Resets if the flow is re-created; consume via rate()/increase().
# TYPE meridio_2_lb_flow_matches_total counter
meridio_2_lb_flow_matches_total{gateway="gw-a",l34route="route-a"} 42
meridio_2_lb_flow_matches_total{gateway="gw-a",l34route="route-b"} 0
# HELP meridio_2_lb_active_targets Number of backends currently receiving traffic for the DistributionGroup (active Maglev targets, from nfqlb show).
# TYPE meridio_2_lb_active_targets gauge
meridio_2_lb_active_targets{dg="dg-a",gateway="gw-a"} 3
`
	require.NoError(t, testutil.CollectAndCompare(c, strings.NewReader(expected),
		"meridio_2_lb_flow_matches_total", "meridio_2_lb_active_targets"))
}

func TestCollector_NoFlowsNoTargets_NoSeries(t *testing.T) {
	c := newTestCollector(&fakeStats{flows: nil, targets: map[string]int{}})
	require.NoError(t, testutil.CollectAndCompare(c, strings.NewReader(""),
		"meridio_2_lb_flow_matches_total", "meridio_2_lb_active_targets"))
}

// A failure reading one source must not suppress the other: active_targets is still produced
// even when flow-list errors. Under the skip-and-count strategy the errored source emits NO
// series (so CollectAndCompare over both names sees only active_targets) and the collector-errors
// counter records the failure.
func TestCollector_FlowMatchesError_ActiveTargetsStillCollected(t *testing.T) {
	stats := &fakeStats{
		flowsErr: errors.New("nfqlb flow-list failed"),
		targets:  map[string]int{"dg-a": 2},
	}
	errs := newFakeCollectorErrors()
	c := newTestCollectorWithErrs(stats, errs)

	expected := `
# HELP meridio_2_lb_active_targets Number of backends currently receiving traffic for the DistributionGroup (active Maglev targets, from nfqlb show).
# TYPE meridio_2_lb_active_targets gauge
meridio_2_lb_active_targets{dg="dg-a",gateway="gw-a"} 2
`
	require.NoError(t, testutil.CollectAndCompare(c, strings.NewReader(expected),
		"meridio_2_lb_flow_matches_total", "meridio_2_lb_active_targets"))
	require.Equal(t, 1, errs.count(CollectorNfqlb, CollectorReasonFlowMatches))
	require.Equal(t, 0, errs.count(CollectorNfqlb, CollectorReasonActiveTargets))
}

// Symmetric to the above: when active-targets (nfqlb show) errors, flow_matches is still
// produced, the active_targets series is absent, and the collector-errors counter records the
// active_targets failure.
func TestCollector_ActiveTargetsError_FlowMatchesStillCollected(t *testing.T) {
	stats := &fakeStats{
		flows:      []nfqlb.FlowMatch{{Name: "route-a", MatchesCount: 9}},
		targetsErr: errors.New("nfqlb show failed"),
	}
	errs := newFakeCollectorErrors()
	c := newTestCollectorWithErrs(stats, errs)

	expected := `
# HELP meridio_2_lb_flow_matches_total Number of packets matched per L34Route flow by nfqlb (from flow-list matches_count). Resets if the flow is re-created; consume via rate()/increase().
# TYPE meridio_2_lb_flow_matches_total counter
meridio_2_lb_flow_matches_total{gateway="gw-a",l34route="route-a"} 9
`
	require.NoError(t, testutil.CollectAndCompare(c, strings.NewReader(expected),
		"meridio_2_lb_flow_matches_total", "meridio_2_lb_active_targets"))
	require.Equal(t, 1, errs.count(CollectorNfqlb, CollectorReasonActiveTargets))
	require.Equal(t, 0, errs.count(CollectorNfqlb, CollectorReasonFlowMatches))
}

func TestCollector_MultipleDGsAndRoutes(t *testing.T) {
	stats := &fakeStats{
		flows: []nfqlb.FlowMatch{
			{Name: "route-a", MatchesCount: 10},
			{Name: "route-b", MatchesCount: 20},
		},
		targets: map[string]int{"dg-a": 1, "dg-b": 5},
	}
	c := newTestCollector(stats)

	expected := `
# HELP meridio_2_lb_flow_matches_total Number of packets matched per L34Route flow by nfqlb (from flow-list matches_count). Resets if the flow is re-created; consume via rate()/increase().
# TYPE meridio_2_lb_flow_matches_total counter
meridio_2_lb_flow_matches_total{gateway="gw-a",l34route="route-a"} 10
meridio_2_lb_flow_matches_total{gateway="gw-a",l34route="route-b"} 20
# HELP meridio_2_lb_active_targets Number of backends currently receiving traffic for the DistributionGroup (active Maglev targets, from nfqlb show).
# TYPE meridio_2_lb_active_targets gauge
meridio_2_lb_active_targets{dg="dg-a",gateway="gw-a"} 1
meridio_2_lb_active_targets{dg="dg-b",gateway="gw-a"} 5
`
	require.NoError(t, testutil.CollectAndCompare(c, strings.NewReader(expected),
		"meridio_2_lb_flow_matches_total", "meridio_2_lb_active_targets"))
}
