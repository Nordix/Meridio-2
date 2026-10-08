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
)

// NftablesReader is the subset of nftables.Manager the NftablesCollector reads at scrape time.
// *nftables.Manager satisfies it; a fake is used in tests. The nftables-sourced metrics share
// one collector because they read the same data source (the google/nftables client on the
// meridio-lb table), kept separate from the vishvananda/netlink route reads which are a
// different source/library.
type NftablesReader interface {
	// VIPSetSize returns the number of VIP entries in the IPv4 and IPv6 VIP sets.
	VIPSetSize() (ipv4, ipv6 int, err error)
	// DropCounts returns per-reason dropped-packet totals from the drop-accounting chain,
	// keyed by reason. Empty when drop accounting is disabled.
	DropCounts() (map[string]uint64, error)
}

// NftablesCollector is a prometheus.Collector exposing the nftables-sourced LB metrics for the
// single Gateway this LB Pod serves. Read lazily (pull-based) at scrape time via the
// google/nftables client.
//
// For external-state safety (#236) Collect serializes with a mutex so overlapping scrapes cannot
// issue concurrent netlink reads, and it owns that mutex independently of the route and nfqlb
// collectors. Unlike the nfqlb collector it carries NO collect timeout: the google/nftables
// client issues synchronous netlink syscalls against the local meridio-lb table with no context
// support, and such local-table reads do not block in practice — so there is no context to
// bound. (The route collector, which can scan all host rules, is where a timeout is warranted.)
type NftablesCollector struct {
	nft         NftablesReader
	gatewayName string
	errs        errorRecorder

	mu sync.Mutex

	vipSetSizeDesc *prometheus.Desc
	dropsDesc      *prometheus.Desc
}

// NewNftablesCollector creates an NftablesCollector. prefix must already be validated (see
// internal/common/metrics.ValidatePrefix). gatewayName is the bare Gateway name (constant
// "gateway" label). errs records read failures (nil-safe); on a read error the data series is
// skipped and the failure counted, so one failing read does not fail the whole scrape.
func NewNftablesCollector(nft NftablesReader, gatewayName, prefix string, errs errorRecorder) *NftablesCollector {
	return &NftablesCollector{
		nft:         nft,
		gatewayName: gatewayName,
		errs:        errs,
		vipSetSizeDesc: prometheus.NewDesc(
			prefix+"_lb_nftables_vip_set_size",
			"Number of VIP entries currently programmed in the LB's nftables VIP sets "+
				"(IPv4 and IPv6 combined), from the meridio-lb table.",
			[]string{"gateway"}, nil,
		),
		dropsDesc: prometheus.NewDesc(
			prefix+"_lb_drops_total",
			"Number of packets dropped by the LB's drop-accounting chain, by reason "+
				"(no_flow: matched a VIP but no flow selector matched; no_targets: a flow "+
				"matched but no active targets). Absent when drop accounting is disabled.",
			[]string{"gateway", "reason"}, nil,
		),
	}
}

// Describe implements prometheus.Collector.
func (c *NftablesCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.vipSetSizeDesc
	ch <- c.dropsDesc
}

// Collect implements prometheus.Collector. Reads nftables state fresh on every scrape, serialized
// by mu. On a read failure it SKIPS that source's series and increments the collector-errors
// counter, rather than emitting an invalid metric — under controller-runtime's HTTPErrorOnError an
// invalid metric would fail the entire scrape. The two reads are independent.
func (c *NftablesCollector) Collect(ch chan<- prometheus.Metric) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// VIP set size: one gauge per gateway, IPv4+IPv6 summed. Family split is intentionally
	// omitted — a VIP count per family adds little diagnostic value (mirrors the sidecar's
	// vips_configured, which is also gateway-only).
	if ipv4, ipv6, err := c.nft.VIPSetSize(); err != nil {
		c.errs.Inc(CollectorNftables, CollectorReasonVIPSetSize)
	} else {
		ch <- prometheus.MustNewConstMetric(
			c.vipSetSizeDesc, prometheus.GaugeValue, float64(ipv4+ipv6), c.gatewayName,
		)
	}

	// Drops: one counter series per reason that has a drop rule. Absent reasons (and a fully
	// disabled drop-accounting chain) emit nothing — a disabled feature is not reported as 0.
	if drops, err := c.nft.DropCounts(); err != nil {
		c.errs.Inc(CollectorNftables, CollectorReasonDrops)
	} else {
		for reason, count := range drops {
			ch <- prometheus.MustNewConstMetric(
				c.dropsDesc, prometheus.CounterValue, float64(count), c.gatewayName, reason,
			)
		}
	}
}
