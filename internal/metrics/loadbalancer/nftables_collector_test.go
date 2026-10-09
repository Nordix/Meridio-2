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
	"errors"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

// fakeNftables is an NftablesReader test double.
type fakeNftables struct {
	ipv4, ipv6 int
	err        error
	drops      map[string]uint64
	dropsErr   error
}

func (f *fakeNftables) VIPSetSize() (int, int, error) { return f.ipv4, f.ipv6, f.err }

func (f *fakeNftables) DropCounts() (map[string]uint64, error) { return f.drops, f.dropsErr }

func newTestNftablesCollector(nft NftablesReader, errs errorRecorder) *NftablesCollector {
	return NewNftablesCollector(nft, testGateway, testPrefix, errs)
}

func TestNftablesCollector_VIPSetSize_SumsFamilies(t *testing.T) {
	c := newTestNftablesCollector(&fakeNftables{ipv4: 3, ipv6: 2}, newFakeCollectorErrors())

	expected := `
# HELP meridio_2_lb_nftables_vip_set_size Number of VIP entries currently programmed in the LB's nftables VIP sets (IPv4 and IPv6 combined), from the meridio-lb table.
# TYPE meridio_2_lb_nftables_vip_set_size gauge
meridio_2_lb_nftables_vip_set_size{gateway="gw-a"} 5
`
	require.NoError(t, testutil.CollectAndCompare(c, strings.NewReader(expected),
		"meridio_2_lb_nftables_vip_set_size"))
}

func TestNftablesCollector_VIPSetSize_Zero(t *testing.T) {
	c := newTestNftablesCollector(&fakeNftables{ipv4: 0, ipv6: 0}, newFakeCollectorErrors())

	expected := `
# HELP meridio_2_lb_nftables_vip_set_size Number of VIP entries currently programmed in the LB's nftables VIP sets (IPv4 and IPv6 combined), from the meridio-lb table.
# TYPE meridio_2_lb_nftables_vip_set_size gauge
meridio_2_lb_nftables_vip_set_size{gateway="gw-a"} 0
`
	require.NoError(t, testutil.CollectAndCompare(c, strings.NewReader(expected),
		"meridio_2_lb_nftables_vip_set_size"))
}

// On a read error the collector SKIPS the series and counts the failure (no invalid metric).
func TestNftablesCollector_VIPSetSize_Error(t *testing.T) {
	errs := newFakeCollectorErrors()
	c := newTestNftablesCollector(&fakeNftables{err: errors.New("netlink read failed")}, errs)

	// No vip_set_size series emitted.
	require.NoError(t, testutil.CollectAndCompare(c, strings.NewReader(""),
		"meridio_2_lb_nftables_vip_set_size"))
	require.Equal(t, 1, errs.count(CollectorNftables, CollectorReasonVIPSetSize))
}

func TestNftablesCollector_Drops_PerReason(t *testing.T) {
	c := newTestNftablesCollector(&fakeNftables{
		drops: map[string]uint64{"no_flow": 7, "no_targets": 3},
	}, newFakeCollectorErrors())

	expected := `
# HELP meridio_2_lb_drops_total Number of packets dropped by the LB's drop-accounting chain, by reason (no_flow: matched a VIP but no flow selector matched; no_targets: a flow matched but no active targets). Absent when drop accounting is disabled.
# TYPE meridio_2_lb_drops_total counter
meridio_2_lb_drops_total{gateway="gw-a",reason="no_flow"} 7
meridio_2_lb_drops_total{gateway="gw-a",reason="no_targets"} 3
`
	require.NoError(t, testutil.CollectAndCompare(c, strings.NewReader(expected),
		"meridio_2_lb_drops_total"))
}

// A disabled drop chain yields an empty map, so no drop series are emitted.
func TestNftablesCollector_Drops_DisabledEmitsNoSeries(t *testing.T) {
	c := newTestNftablesCollector(&fakeNftables{drops: map[string]uint64{}}, newFakeCollectorErrors())
	require.NoError(t, testutil.CollectAndCompare(c, strings.NewReader(""),
		"meridio_2_lb_drops_total"))
}

// Only one reason present (e.g. only nolbFwmark configured) emits only that series.
func TestNftablesCollector_Drops_SingleReason(t *testing.T) {
	c := newTestNftablesCollector(&fakeNftables{drops: map[string]uint64{"no_flow": 5}}, newFakeCollectorErrors())

	expected := `
# HELP meridio_2_lb_drops_total Number of packets dropped by the LB's drop-accounting chain, by reason (no_flow: matched a VIP but no flow selector matched; no_targets: a flow matched but no active targets). Absent when drop accounting is disabled.
# TYPE meridio_2_lb_drops_total counter
meridio_2_lb_drops_total{gateway="gw-a",reason="no_flow"} 5
`
	require.NoError(t, testutil.CollectAndCompare(c, strings.NewReader(expected),
		"meridio_2_lb_drops_total"))
}

// On a drops read error the collector SKIPS the series and counts the failure.
func TestNftablesCollector_Drops_Error(t *testing.T) {
	errs := newFakeCollectorErrors()
	c := newTestNftablesCollector(&fakeNftables{dropsErr: errors.New("drop read failed")}, errs)

	require.NoError(t, testutil.CollectAndCompare(c, strings.NewReader(""),
		"meridio_2_lb_drops_total"))
	require.Equal(t, 1, errs.count(CollectorNftables, CollectorReasonDrops))
}

// VIP read failing must not suppress the (healthy) drops read, and vice versa.
func TestNftablesCollector_OneSourceError_OtherStillCollected(t *testing.T) {
	errs := newFakeCollectorErrors()
	c := newTestNftablesCollector(&fakeNftables{
		err:   errors.New("vip read failed"),
		drops: map[string]uint64{"no_flow": 4},
	}, errs)

	expected := `
# HELP meridio_2_lb_drops_total Number of packets dropped by the LB's drop-accounting chain, by reason (no_flow: matched a VIP but no flow selector matched; no_targets: a flow matched but no active targets). Absent when drop accounting is disabled.
# TYPE meridio_2_lb_drops_total counter
meridio_2_lb_drops_total{gateway="gw-a",reason="no_flow"} 4
`
	// vip_set_size absent (errored), drops present.
	require.NoError(t, testutil.CollectAndCompare(c, strings.NewReader(expected),
		"meridio_2_lb_nftables_vip_set_size", "meridio_2_lb_drops_total"))
	require.Equal(t, 1, errs.count(CollectorNftables, CollectorReasonVIPSetSize))
	require.Equal(t, 0, errs.count(CollectorNftables, CollectorReasonDrops))
}
