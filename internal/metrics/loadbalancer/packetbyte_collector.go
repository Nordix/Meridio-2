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
	"sync"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/nordix/meridio-2/internal/nftables"
)

// PacketByteReader is the subset the PacketByteCollector reads at scrape time. It returns
// forwarded packet/byte totals from the target-accounting chain: perDG keyed by DistributionGroup
// name, and (only when per-target counters are installed) perTarget keyed by {DG, target}.
// The LB controller provides an implementation that supplies the DG<->fwmark-range mapping it
// owns (snapshotting it under its own lock); a fake is used in tests.
type PacketByteReader interface {
	PacketByteCounts() (perDG map[string]nftables.Counts, perTarget map[nftables.DGTarget]nftables.Counts, err error)
}

// PacketByteCollector is a prometheus.Collector exposing forwarded packet/byte totals per
// DistributionGroup for the single Gateway this LB Pod serves, read lazily at scrape time from the
// nftables target-accounting chain.
//
//   - <prefix>_lb_packets_total        (counter; gateway, dg)         — always
//   - <prefix>_lb_bytes_total          (counter; gateway, dg)         — always
//   - <prefix>_lb_target_packets_total (counter; gateway, dg, target) — only when per-target
//     counter rules are installed (the --enable-per-target-metrics opt-in); otherwise the
//     perTarget read is empty and no such series are emitted. Gating is thus data-plane driven:
//     the collector emits whatever the read returns.
//
// Per-DG and per-target both derive from one source (an nftables counter carries both Packets and
// Bytes). Per external-state safety (#236) Collect serializes with a mutex; on a read failure it
// SKIPS all series and increments the collector-errors counter (option-3), rather than emitting an
// invalid metric which would fail the whole scrape under controller-runtime's HTTPErrorOnError. No
// collect timeout: the google/nftables client has no context API and the reads are bounded local
// reads.
//
// Per-target bytes are deliberately NOT emitted. The kernel counter already carries the byte total
// (it is read and discarded), but emitting <prefix>_lb_target_bytes_total would double the opt-in
// per-target series count for little gain; per-DG bytes (_lb_bytes_total) already give byte volume.
//
// Caveat when per-target is enabled: in per-target mode the per-DG totals (_lb_packets_total /
// _lb_bytes_total) are summed from the per-target rules, which are rebuilt on every reconcile, so
// those two always-on counters become reset-prone (consume via rate()/increase()). The real fix
// (diff instead of rebuild) is tracked in the counter-map follow-up. See the per-DG metric help.
type PacketByteCollector struct {
	reader      PacketByteReader
	gatewayName string
	errs        errorRecorder

	mu sync.Mutex

	packetsDesc       *prometheus.Desc
	bytesDesc         *prometheus.Desc
	targetPacketsDesc *prometheus.Desc
}

// NewPacketByteCollector creates a PacketByteCollector. prefix must already be validated (see
// internal/common/metrics.ValidatePrefix). gatewayName is the bare Gateway name (constant
// "gateway" label). errs records read failures (nil-safe).
func NewPacketByteCollector(reader PacketByteReader, gatewayName, prefix string, errs errorRecorder) *PacketByteCollector {
	return &PacketByteCollector{
		reader:      reader,
		gatewayName: gatewayName,
		errs:        errs,
		packetsDesc: prometheus.NewDesc(
			prefix+"_lb_packets_total",
			"Number of packets forwarded to targets per DistributionGroup, counted by the LB's "+
				"nftables target-accounting chain (by fwmark). Note: when per-target metrics are "+
				"enabled this counter is reset-prone (summed from per-target rules that are rebuilt "+
				"on reconcile); consume via rate()/increase().",
			[]string{"gateway", "dg"}, nil,
		),
		bytesDesc: prometheus.NewDesc(
			prefix+"_lb_bytes_total",
			"Number of bytes forwarded to targets per DistributionGroup, counted by the LB's "+
				"nftables target-accounting chain (by fwmark). Note: when per-target metrics are "+
				"enabled this counter is reset-prone (summed from per-target rules that are rebuilt "+
				"on reconcile); consume via rate()/increase().",
			[]string{"gateway", "dg"}, nil,
		),
		targetPacketsDesc: prometheus.NewDesc(
			prefix+"_lb_target_packets_total",
			"Number of packets forwarded per target, counted by the LB's nftables target-accounting "+
				"chain. Only present when per-target metrics are enabled. The 'target' label is the "+
				"Maglev identifier, which is recycled: when a backend Pod is removed its id returns "+
				"to a free pool and may later be reassigned to a different Pod, so a single series "+
				"can change subject over time. Not a stable per-Pod identity.",
			[]string{"gateway", "dg", "target"}, nil,
		),
	}
}

// Describe implements prometheus.Collector.
func (c *PacketByteCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.packetsDesc
	ch <- c.bytesDesc
	ch <- c.targetPacketsDesc
}

// Collect implements prometheus.Collector. Reads per-DG (and, when installed, per-target)
// forwarded totals fresh on every scrape, serialized by mu. On a read failure it skips the series
// and counts the failure (option-3).
func (c *PacketByteCollector) Collect(ch chan<- prometheus.Metric) {
	c.mu.Lock()
	defer c.mu.Unlock()

	perDG, perTarget, err := c.reader.PacketByteCounts()
	if err != nil {
		c.errs.Inc(CollectorPacketByte)
		return
	}
	for dg, counts := range perDG {
		ch <- prometheus.MustNewConstMetric(
			c.packetsDesc, prometheus.CounterValue, float64(counts.Packets), c.gatewayName, dg,
		)
		ch <- prometheus.MustNewConstMetric(
			c.bytesDesc, prometheus.CounterValue, float64(counts.Bytes), c.gatewayName, dg,
		)
	}
	// Per-target series only exist when per-target rules are installed (opt-in); otherwise
	// perTarget is empty and this loop emits nothing.
	for dt, counts := range perTarget {
		ch <- prometheus.MustNewConstMetric(
			c.targetPacketsDesc, prometheus.CounterValue, float64(counts.Packets), c.gatewayName, dt.DG, dt.Target,
		)
	}
}
