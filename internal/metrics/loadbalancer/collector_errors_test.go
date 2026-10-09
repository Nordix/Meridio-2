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
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

// fakeCollectorErrors is an errorRecorder test double that records Inc calls keyed by
// "collector/reason".
type fakeCollectorErrors struct {
	counts map[string]int
}

func newFakeCollectorErrors() *fakeCollectorErrors {
	return &fakeCollectorErrors{counts: make(map[string]int)}
}

func (f *fakeCollectorErrors) Inc(collector, reason string) {
	f.counts[collector+"/"+reason]++
}

func (f *fakeCollectorErrors) count(collector, reason string) int {
	return f.counts[collector+"/"+reason]
}

func TestCollectorErrors_PreInitializedToZero(t *testing.T) {
	c := NewCollectorErrors(testGateway, testPrefix)

	expected := `
# HELP meridio_2_lb_collector_errors_total Number of scrape-time read failures in the LB metrics collectors, by collector and reason. A non-zero rate means a collector could not read its source for some scrapes (the corresponding data series is absent for those scrapes).
# TYPE meridio_2_lb_collector_errors_total counter
meridio_2_lb_collector_errors_total{collector="nftables",gateway="gw-a",reason="vip_set_size"} 0
meridio_2_lb_collector_errors_total{collector="nftables",gateway="gw-a",reason="drops"} 0
meridio_2_lb_collector_errors_total{collector="route",gateway="gw-a",reason="policy_routes"} 0
meridio_2_lb_collector_errors_total{collector="nfqlb",gateway="gw-a",reason="flow_matches"} 0
meridio_2_lb_collector_errors_total{collector="nfqlb",gateway="gw-a",reason="active_targets"} 0
`
	require.NoError(t, testutil.CollectAndCompare(c.Collector(), strings.NewReader(expected),
		"meridio_2_lb_collector_errors_total"))
}

func TestCollectorErrors_Inc(t *testing.T) {
	c := NewCollectorErrors(testGateway, testPrefix)
	c.Inc(CollectorNftables, CollectorReasonDrops)
	c.Inc(CollectorNftables, CollectorReasonDrops)
	c.Inc(CollectorRoute, CollectorReasonPolicyRoutes)

	expected := `
# HELP meridio_2_lb_collector_errors_total Number of scrape-time read failures in the LB metrics collectors, by collector and reason. A non-zero rate means a collector could not read its source for some scrapes (the corresponding data series is absent for those scrapes).
# TYPE meridio_2_lb_collector_errors_total counter
meridio_2_lb_collector_errors_total{collector="nftables",gateway="gw-a",reason="vip_set_size"} 0
meridio_2_lb_collector_errors_total{collector="nftables",gateway="gw-a",reason="drops"} 2
meridio_2_lb_collector_errors_total{collector="route",gateway="gw-a",reason="policy_routes"} 1
meridio_2_lb_collector_errors_total{collector="nfqlb",gateway="gw-a",reason="flow_matches"} 0
meridio_2_lb_collector_errors_total{collector="nfqlb",gateway="gw-a",reason="active_targets"} 0
`
	require.NoError(t, testutil.CollectAndCompare(c.Collector(), strings.NewReader(expected),
		"meridio_2_lb_collector_errors_total"))
}

func TestCollectorErrors_NilReceiverIsNoOp(t *testing.T) {
	var c *CollectorErrors
	require.NotPanics(t, func() { c.Inc(CollectorNfqlb, CollectorReasonFlowMatches) })
}
