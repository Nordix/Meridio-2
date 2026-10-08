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

// RouteReader is the subset of the nfqlb routing layer the RouteCollector reads at scrape time.
// It is satisfied by a thin adapter over nfqlb.PolicyRouteCounts (see run.go); a fake is used in
// tests. This is a different data source (vishvananda/netlink policy rules) from the nftables
// reads, hence a separate collector.
type RouteReader interface {
	// PolicyRouteCounts returns the number of LB-owned policy rules per IP family
	// (keys "IPv4"/"IPv6").
	PolicyRouteCounts() (map[string]int, error)
}

// RouteCollector is a prometheus.Collector exposing the LB policy-route count for the single
// Gateway this LB Pod serves, split by IP family. Read lazily (pull-based) at scrape time via
// vishvananda/netlink.
//
// For external-state safety (#236) Collect serializes with a mutex so overlapping scrapes cannot
// issue concurrent rule-list reads, and it owns that mutex independently of the nftables and
// nfqlb collectors. Like the nftables collector it carries NO collect timeout: vishvananda/netlink
// has no context API, and RuleList is a single bounded NETLINK_ROUTE dump that does not block in
// practice — so there is no context to bound. The mutex (the real concurrency concern) remains.
type RouteCollector struct {
	routes      RouteReader
	gatewayName string
	errs        errorRecorder

	mu sync.Mutex

	policyRoutesDesc *prometheus.Desc
}

// NewRouteCollector creates a RouteCollector. prefix must already be validated (see
// internal/common/metrics.ValidatePrefix). gatewayName is the bare Gateway name (constant
// "gateway" label). errs records read failures (nil-safe); on a read error the data series is
// skipped and the failure counted, so one failing read does not fail the whole scrape.
func NewRouteCollector(routes RouteReader, gatewayName, prefix string, errs errorRecorder) *RouteCollector {
	return &RouteCollector{
		routes:      routes,
		gatewayName: gatewayName,
		errs:        errs,
		policyRoutesDesc: prometheus.NewDesc(
			prefix+"_lb_policy_routes",
			"Number of LB-owned policy routing rules currently installed, per IP family "+
				"(one source-based routing rule per active target).",
			[]string{"gateway", "ip_family"}, nil,
		),
	}
}

// Describe implements prometheus.Collector.
func (c *RouteCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.policyRoutesDesc
}

// Collect implements prometheus.Collector. Reads policy-rule counts fresh on every scrape,
// serialized by mu. On a read failure it SKIPS the series and increments the collector-errors
// counter, rather than emitting an invalid metric — under controller-runtime's HTTPErrorOnError an
// invalid metric would fail the entire scrape.
func (c *RouteCollector) Collect(ch chan<- prometheus.Metric) {
	c.mu.Lock()
	defer c.mu.Unlock()

	counts, err := c.routes.PolicyRouteCounts()
	if err != nil {
		c.errs.Inc(CollectorRoute, CollectorReasonPolicyRoutes)
		return
	}
	// Both families are emitted (0 when empty) so a configured-but-empty family stays
	// distinguishable from an absent one (matches the sidecar nexthops convention).
	for family, count := range counts {
		ch <- prometheus.MustNewConstMetric(
			c.policyRoutesDesc, prometheus.GaugeValue, float64(count), c.gatewayName, family,
		)
	}
}
