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
// The package registers five things with the controller-runtime registry (see run.go):
// three lazy, pull-based collectors that read fresh at scrape time, and two push counters that
// the controller increments as it runs.
//
// Lazy collectors (read on each scrape; never from a background loop or the reconcile/packet path):
//
//   - nfqlb collector (this file) — <prefix>_lb_flow_matches_total (counter; gateway, l34route)
//     and <prefix>_lb_active_targets (gauge; gateway, dg), from the nfqlb subprocess
//     (`nfqlb flow-list` and `nfqlb show`).
//   - nftables collector (nftables_collector.go) — <prefix>_lb_nftables_vip_set_size (gauge) and
//     <prefix>_lb_drops_total (counter; gateway, reason), from the shared nftables manager.
//   - route collector (route_collector.go) — <prefix>_lb_policy_routes (gauge; gateway, ip_family),
//     from the kernel's netlink policy rules.
//
// Push counters (incremented by the controller, registered here):
//
//   - <prefix>_lb_collector_errors_total (gateway, collector) — a lazy collector increments this
//     and skips the failing series when a scrape-time read fails (see collector_errors.go).
//   - <prefix>_lb_route_config_errors_total (gateway, reason) — failed data-plane route/VIP config
//     operations (see config_errors.go).
//
// One collector per data source. The three lazy collectors are split by where they read from —
// the nfqlb subprocess, the nftables table, and netlink policy rules — because those are separate
// sources with separate failure modes, and each owns its own mutex.
//
// Lazy collection gives correct lifecycle for free: a removed flow/DG/route simply stops appearing
// in the next scrape (Prometheus marks the series stale) — no explicit unregistration.
//
// # Scrape-time safety (per #236)
//
// These collectors read external state at scrape time, so each limits concurrent reads of the
// same source. Two safety shapes exist, by source:
//
//   - nfqlb collector: a context timeout (collectTimeout) PLUS TryLock. It shells out to the nfqlb
//     subprocess, so a hung subprocess must be bounded or stuck goroutines would pile up. It uses
//     TryLock (not Lock) so that if a scrape is already reading nfqlb, an overlapping scrape skips
//     immediately instead of queuing — a plain Lock() is not context-aware, so a queued scrape
//     could wait far longer than collectTimeout behind a slow read.
//   - nftables and route collectors: a plain mutex, NO timeout. They do single bounded local reads
//     (google/nftables and vishvananda/netlink syscalls) with no context API and no realistic way
//     to block, so there is nothing to time out and queuing is naturally short.
//
// Document a minimum scrape interval for the LB: one scrape runs one `nfqlb flow-list` plus one
// `nfqlb show` per DG, plus the local nftables/netlink reads.
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

// Collector is a prometheus.Collector exposing the nfqlb-sourced LB metrics for the
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
// by collectTimeout. On a read failure it SKIPS that source's series and increments the
// collector-errors counter, rather than emitting an invalid metric — under controller-runtime's
// HTTPErrorOnError an invalid metric would fail the entire scrape. The two sources are
// independent: a failure in one does not suppress the other.
//
// Concurrency: it uses TryLock, not Lock. If another scrape is already reading nfqlb, this one
// returns immediately (skipping its series and counting an error) instead of queuing behind the
// in-flight read. This keeps a single scrape's cost bounded by collectTimeout alone: sync.Mutex
// is not context-aware, so a plain Lock() could make a queued scrape wait far longer than
// collectTimeout while a slow nfqlb read holds the lock. Only one nfqlb read runs at a time, as
// before; overlapping scrapes simply skip rather than pile up.
func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	if !c.mu.TryLock() {
		// Another scrape is already reading nfqlb; skip this one rather than queue behind it.
		c.errs.Inc(CollectorNfqlb)
		return
	}
	defer c.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), c.collectTimeout)
	defer cancel()

	// Flow matches (per L34Route).
	if flows, err := c.stats.FlowMatches(ctx); err != nil {
		c.errs.Inc(CollectorNfqlb)
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
		c.errs.Inc(CollectorNfqlb)
	} else {
		for dg, count := range targets {
			ch <- prometheus.MustNewConstMetric(
				c.activeTargetsDesc, prometheus.GaugeValue, float64(count),
				c.gatewayName, dg,
			)
		}
	}
}
