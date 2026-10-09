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

// Package loadbalancer implements the stateless-load-balancer binary's custom Prometheus
// metrics.
//
// This collector exposes two nfqlb-sourced metrics, both read lazily (pull-based) at scrape time
// via the nfqlb subprocess — never from a background loop or the reconcile/packet path:
//
//   - <prefix>_lb_flow_matches_total (Counter, labels: gateway, l34route) — packets matched per
//     L34Route, from `nfqlb flow-list` (matches_count). Monotonic while a flow exists; resets if
//     the flow is re-created, so it is exposed as a counter (consume via rate()/increase()).
//   - <prefix>_lb_active_targets (Gauge, labels: gateway, dg) — backends currently receiving
//     traffic, from `nfqlb show --shm=<dg>` (the Maglev "Active:" activation state).
//
// Lazy collection gives correct lifecycle for free: a removed flow/DG simply stops appearing in
// the next scrape (Prometheus marks the series stale) — no explicit unregistration.
//
// # Subprocess-backed safety (per #236)
//
// Unlike cache-backed collectors, this one shells out to nfqlb. Collect therefore (a) bounds the
// work with a context timeout (collectTimeout) so a hung nfqlb cannot accumulate stuck
// goroutines, and (b) serializes with a mutex so an overlapping/retried scrape cannot spawn a
// second concurrent set of nfqlb subprocesses. Document a minimum scrape interval for the LB —
// each scrape runs one `nfqlb flow-list` plus one `nfqlb show` per DG.
package loadbalancer

import (
	"context"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/nordix/meridio-2/internal/nfqlb"
)

// StatsReader is the subset of the nfqlb load balancer the collector reads at scrape time.
// *nfqlb.NFQueueLoadBalancer satisfies it; a fake is used in tests.
type StatsReader interface {
	// FlowMatches returns the per-flow match counters (flow name == L34Route name).
	FlowMatches(ctx context.Context) ([]nfqlb.FlowMatch, error)
	// ActiveTargets returns the active-target count per instance (instance name == DG name).
	ActiveTargets(ctx context.Context) (map[string]int, error)
}

// Collector is a prometheus.Collector exposing the Phase-1 nfqlb-sourced LB metrics for the
// single Gateway this LB Pod serves. The gateway label is a constant (gatewayName) for the
// process lifetime; dg/l34route labels are derived fresh from nfqlb on each scrape.
type Collector struct {
	stats          StatsReader
	gatewayName    string
	collectTimeout time.Duration
	errs           errorRecorder

	// mu serializes Collect so overlapping scrapes do not spawn concurrent nfqlb subprocesses.
	mu sync.Mutex

	flowMatchesDesc   *prometheus.Desc
	activeTargetsDesc *prometheus.Desc
}

// NewCollector creates a Collector. prefix must already be validated (see
// internal/common/metrics.ValidatePrefix). gatewayName is the bare Gateway name this LB serves
// (used as the constant "gateway" label). collectTimeout bounds each scrape's nfqlb subprocess
// work. errs records read failures (nil-safe); on a read error the data series is skipped and the
// failure counted, so one failing read does not fail the whole scrape.
func NewCollector(stats StatsReader, gatewayName, prefix string, collectTimeout time.Duration, errs errorRecorder) *Collector {
	return &Collector{
		stats:          stats,
		gatewayName:    gatewayName,
		collectTimeout: collectTimeout,
		errs:           errs,
		flowMatchesDesc: prometheus.NewDesc(
			prefix+"_lb_flow_matches_total",
			"Number of packets matched per L34Route flow by nfqlb (from flow-list matches_count). "+
				"Resets if the flow is re-created; consume via rate()/increase().",
			[]string{"gateway", "l34route"}, nil,
		),
		activeTargetsDesc: prometheus.NewDesc(
			prefix+"_lb_active_targets",
			"Number of backends currently receiving traffic for the DistributionGroup "+
				"(active Maglev targets, from nfqlb show).",
			[]string{"gateway", "dg"}, nil,
		),
	}
}

// Describe implements prometheus.Collector.
func (c *Collector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.flowMatchesDesc
	ch <- c.activeTargetsDesc
}

// Collect implements prometheus.Collector. It reads nfqlb state fresh on every scrape, bounded
// by collectTimeout and serialized by mu (see package doc). On a read failure it SKIPS that
// source's series and increments the collector-errors counter, rather than emitting an invalid
// metric — under controller-runtime's HTTPErrorOnError an invalid metric would fail the entire
// scrape. The two sources are independent: a failure in one does not suppress the other.
func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	c.mu.Lock()
	defer c.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), c.collectTimeout)
	defer cancel()

	// Flow matches (per L34Route).
	if flows, err := c.stats.FlowMatches(ctx); err != nil {
		c.errs.Inc(CollectorNfqlb, CollectorReasonFlowMatches)
	} else {
		for _, f := range flows {
			ch <- prometheus.MustNewConstMetric(
				c.flowMatchesDesc, prometheus.CounterValue, float64(f.MatchesCount),
				c.gatewayName, f.Name,
			)
		}
	}

	// Active targets (per DistributionGroup).
	if targets, err := c.stats.ActiveTargets(ctx); err != nil {
		c.errs.Inc(CollectorNfqlb, CollectorReasonActiveTargets)
	} else {
		for dg, count := range targets {
			ch <- prometheus.MustNewConstMetric(
				c.activeTargetsDesc, prometheus.GaugeValue, float64(count),
				c.gatewayName, dg,
			)
		}
	}
}
