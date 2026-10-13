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

package router

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/nordix/meridio-2/internal/bird"
)

// Config-error reason label values, re-exported from internal/bird so callers of this package can
// name them without importing bird directly. They are defined there because the increments happen
// at the netlink failure sites in that package, which cannot import this one (this package imports
// bird for the collector, so the reverse direction is an import cycle).
//
//   - ReasonRule: policy-routing rule add/delete for the BIRD and blackhole tables.
//   - ReasonRoute: blackhole default route installation in the blackhole table.
//
// Unlike the sidecar's equivalent there is no link reason: the router does not enumerate or
// configure interfaces, it only installs rules and routes.
const (
	ReasonRule  = bird.ReasonRule
	ReasonRoute = bird.ReasonRoute
)

// ConfigErrors is a push-style counter of failed netlink operations in the router's VIP
// routing path (<prefix>_router_route_config_errors_total, labels: gateway, reason). Unlike the
// BIRD-derived gauges it is not lazily collected: a failed netlink op is a point-in-time event
// with no durable external source to read at scrape time, so it is counted at the failure site.
//
// It carries no pod label: each router exposes its own per-Pod /metrics endpoint, so Pod identity
// is 1:1 with the scrape target and supplied by the scraper (instance/pod). It does carry gateway,
// matching the collector's gauges, since one router serves exactly one Gateway.
//
// Being a counter, the value resets to zero on process restart. Consume via rate()/increase()
// (both reset-aware) and, for restart-aware interpretation, pair with process_start_time_seconds
// (exposed for free by controller-runtime's process collector) rather than container-restart
// metrics: the former reflects controller-process restarts even when a supervisor keeps the
// container alive.
//
// A nil *ConfigErrors is a valid no-op: Inc on a nil receiver does nothing, so the routing path
// can call it unconditionally and construction/registration stays gated on metrics being enabled.
type ConfigErrors struct {
	total   *prometheus.CounterVec
	gateway string
}

// NewConfigErrors builds the counter and pre-initializes both reason series to 0 so they are
// present (not absent) from the first scrape and rate()/increase() have a baseline. prefix must
// already be validated (see internal/common/metrics.ValidatePrefix).
func NewConfigErrors(prefix, gatewayName string) *ConfigErrors {
	c := &ConfigErrors{
		total: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: prefix + "_router_route_config_errors_total",
			Help: "Number of failed netlink operations in the router's VIP routing path, by reason.",
		}, []string{"gateway", "reason"}),
		gateway: gatewayName,
	}
	for _, reason := range []string{ReasonRule, ReasonRoute} {
		c.total.WithLabelValues(gatewayName, reason)
	}
	return c
}

// Collector returns the underlying prometheus.Collector for registration.
func (c *ConfigErrors) Collector() prometheus.Collector { return c.total }

// Inc increments the counter for the given reason. Safe on a nil receiver (no-op) and safe for
// concurrent use (prometheus.CounterVec is documented concurrent-safe).
func (c *ConfigErrors) Inc(reason string) {
	if c == nil {
		return
	}
	c.total.WithLabelValues(c.gateway, reason).Inc()
}
