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

import "github.com/prometheus/client_golang/prometheus"

// errorRecorder records a scrape-time collector read failure by (collector, reason). Satisfied by
// *CollectorErrors; a fake is used in tests. A typed-nil *CollectorErrors is a valid no-op.
type errorRecorder interface {
	Inc(collector, reason string)
}

// Collector label values for CollectorErrors (which lazy collector hit a read error).
const (
	CollectorNftables = "nftables"
	CollectorRoute    = "route"
	CollectorNfqlb    = "nfqlb"
)

// Reason label values for CollectorErrors (which specific read failed). Each belongs to one
// collector: vip_set_size/drops → nftables; policy_routes → route; flow_matches/active_targets →
// nfqlb.
const (
	CollectorReasonVIPSetSize    = "vip_set_size"
	CollectorReasonDrops         = "drops"
	CollectorReasonPolicyRoutes  = "policy_routes"
	CollectorReasonFlowMatches   = "flow_matches"
	CollectorReasonActiveTargets = "active_targets"
)

// collectorReasons lists the valid (collector, reason) pairs, used to pre-initialize series to 0.
var collectorReasons = []struct{ collector, reason string }{
	{CollectorNftables, CollectorReasonVIPSetSize},
	{CollectorNftables, CollectorReasonDrops},
	{CollectorRoute, CollectorReasonPolicyRoutes},
	{CollectorNfqlb, CollectorReasonFlowMatches},
	{CollectorNfqlb, CollectorReasonActiveTargets},
}

// CollectorErrors is a push-style counter of scrape-time read failures in the LB's lazy
// collectors (<prefix>_lb_collector_errors_total, labels: gateway, collector, reason).
//
// Why this exists: controller-runtime serves /metrics with promhttp.HTTPErrorOnError, so a
// collector that emits prometheus.NewInvalidMetric on a read failure makes the WHOLE scrape
// return HTTP 500 — zeroing out every metric from the Pod, not just the failing series. Instead,
// the LB collectors skip the failing series (Prometheus marks it stale) and increment this
// counter, keeping the failure observable/alertable without the whole-scrape blast radius.
//
// The gateway label is the bare Gateway name (constant for the process). collector/reason are a
// fixed enum (CollectorErrors pre-initializes each valid pair to 0 so rate()/increase() have a
// baseline). Being a counter, it resets to zero on process restart.
//
// A nil *CollectorErrors is a valid no-op: Inc on a nil receiver does nothing, so collectors can
// call it unconditionally while construction/registration stays gated on metrics being enabled.
type CollectorErrors struct {
	gatewayName string
	total       *prometheus.CounterVec
}

// NewCollectorErrors builds the counter and pre-initializes every valid (collector, reason) series
// to 0. prefix must already be validated (see internal/common/metrics.ValidatePrefix); gatewayName
// is the bare Gateway name used as the constant gateway label.
func NewCollectorErrors(gatewayName, prefix string) *CollectorErrors {
	c := &CollectorErrors{
		gatewayName: gatewayName,
		total: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: prefix + "_lb_collector_errors_total",
			Help: "Number of scrape-time read failures in the LB metrics collectors, by " +
				"collector and reason. A non-zero rate means a collector could not read its " +
				"source for some scrapes (the corresponding data series is absent for those scrapes).",
		}, []string{"gateway", "collector", "reason"}),
	}
	for _, cr := range collectorReasons {
		c.total.WithLabelValues(gatewayName, cr.collector, cr.reason)
	}
	return c
}

// Collector returns the underlying prometheus.Collector for registration.
func (c *CollectorErrors) Collector() prometheus.Collector { return c.total }

// Inc increments the error counter for the given collector and reason. Safe on a nil receiver
// (no-op) and safe for concurrent use (prometheus.CounterVec is documented concurrent-safe).
func (c *CollectorErrors) Inc(collector, reason string) {
	if c == nil {
		return
	}
	c.total.WithLabelValues(c.gatewayName, collector, reason).Inc()
}
