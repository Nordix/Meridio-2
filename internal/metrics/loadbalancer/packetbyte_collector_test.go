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

	"github.com/nordix/meridio-2/internal/nftables"
)

// fakePacketBytes is a PacketByteReader test double.
type fakePacketBytes struct {
	perDG     map[string]nftables.Counts
	perTarget map[nftables.DGTarget]nftables.Counts
	err       error
}

func (f *fakePacketBytes) PacketByteCounts() (map[string]nftables.Counts, map[nftables.DGTarget]nftables.Counts, error) {
	pt := f.perTarget
	if pt == nil {
		pt = map[nftables.DGTarget]nftables.Counts{}
	}
	return f.perDG, pt, f.err
}

func newTestPacketByteCollector(r PacketByteReader, errs errorRecorder) *PacketByteCollector {
	return NewPacketByteCollector(r, testGateway, testPrefix, errs)
}

func TestPacketByteCollector_PerDG(t *testing.T) {
	c := newTestPacketByteCollector(&fakePacketBytes{perDG: map[string]nftables.Counts{
		"dg-a": {Packets: 10, Bytes: 1400},
	}}, newFakeCollectorErrors())

	expected := `
# HELP meridio_2_lb_packets_total Number of packets forwarded to targets per DistributionGroup, counted by the LB's nftables target-accounting chain (by fwmark).
# TYPE meridio_2_lb_packets_total counter
meridio_2_lb_packets_total{dg="dg-a",gateway="gw-a"} 10
# HELP meridio_2_lb_bytes_total Number of bytes forwarded to targets per DistributionGroup, counted by the LB's nftables target-accounting chain (by fwmark).
# TYPE meridio_2_lb_bytes_total counter
meridio_2_lb_bytes_total{dg="dg-a",gateway="gw-a"} 1400
`
	require.NoError(t, testutil.CollectAndCompare(c, strings.NewReader(expected),
		"meridio_2_lb_packets_total", "meridio_2_lb_bytes_total"))
}

func TestPacketByteCollector_NoDGs_NoSeries(t *testing.T) {
	c := newTestPacketByteCollector(&fakePacketBytes{perDG: map[string]nftables.Counts{}}, newFakeCollectorErrors())
	require.NoError(t, testutil.CollectAndCompare(c, strings.NewReader(""),
		"meridio_2_lb_packets_total", "meridio_2_lb_bytes_total"))
}

// On a read error the collector SKIPS the series and counts the failure (no invalid metric).
func TestPacketByteCollector_Error(t *testing.T) {
	errs := newFakeCollectorErrors()
	c := newTestPacketByteCollector(&fakePacketBytes{err: errors.New("nftables read failed")}, errs)

	require.NoError(t, testutil.CollectAndCompare(c, strings.NewReader(""),
		"meridio_2_lb_packets_total", "meridio_2_lb_bytes_total"))
	require.Equal(t, 1, errs.count(CollectorNftables))
}

func TestPacketByteCollector_PerTarget(t *testing.T) {
	c := newTestPacketByteCollector(&fakePacketBytes{
		perDG: map[string]nftables.Counts{
			"dg-a": {Packets: 30, Bytes: 4200},
		},
		perTarget: map[nftables.DGTarget]nftables.Counts{
			{DG: "dg-a", Target: "0"}: {Packets: 10, Bytes: 1400},
			{DG: "dg-a", Target: "1"}: {Packets: 20, Bytes: 2800},
		},
	}, newFakeCollectorErrors())

	expected := `
# HELP meridio_2_lb_packets_total Number of packets forwarded to targets per DistributionGroup, counted by the LB's nftables target-accounting chain (by fwmark).
# TYPE meridio_2_lb_packets_total counter
meridio_2_lb_packets_total{dg="dg-a",gateway="gw-a"} 30
# HELP meridio_2_lb_bytes_total Number of bytes forwarded to targets per DistributionGroup, counted by the LB's nftables target-accounting chain (by fwmark).
# TYPE meridio_2_lb_bytes_total counter
meridio_2_lb_bytes_total{dg="dg-a",gateway="gw-a"} 4200
# HELP meridio_2_lb_target_packets_total Number of packets forwarded per target (by Maglev identifier), counted by the LB's nftables target-accounting chain. Only present when per-target metrics are enabled.
# TYPE meridio_2_lb_target_packets_total counter
meridio_2_lb_target_packets_total{dg="dg-a",gateway="gw-a",target="0"} 10
meridio_2_lb_target_packets_total{dg="dg-a",gateway="gw-a",target="1"} 20
`
	require.NoError(t, testutil.CollectAndCompare(c, strings.NewReader(expected),
		"meridio_2_lb_packets_total", "meridio_2_lb_bytes_total", "meridio_2_lb_target_packets_total"))
}
