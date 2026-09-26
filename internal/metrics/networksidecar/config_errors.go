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

package networksidecar

import "github.com/prometheus/client_golang/prometheus"

// Config-error reason label values for ConfigErrors. Each maps to a distinct netlink object
// layer the sidecar operates at, so a failure points at a specific diagnosis:
//   - link: interface lookup/enumeration (LinkByName/LinkList) — Pod networking / Multus / interface
//     not ready.
//   - address: VIP address add/remove (AddrAdd/AddrDel).
//   - route: policy-routing rule and route ops (RuleAdd/RouteReplace/...); rules and routes share
//     this bucket as two halves of one table's source-based routing config.
const (
	ReasonLink    = "link"
	ReasonAddress = "address"
	ReasonRoute   = "route"
)

// ConfigErrors is a push-style counter of failed netlink operations in the sidecar's reconcile
// path (<prefix>_sidecar_config_errors_total, label: reason). Unlike the ENC-derived gauges it is
// not lazily collected: a failed netlink op is a point-in-time event with no durable external
// source to read at scrape time, so it is counted at the failure site (see #236).
//
// It carries no pod label — each sidecar exposes its own per-Pod /metrics endpoint, so Pod
// identity is 1:1 with the scrape target and supplied by the scraper (instance/pod).
//
// Being a counter, the value resets to zero on process restart. Consume via rate()/increase()
// (both reset-aware) and, for restart-aware interpretation, pair with process_start_time_seconds
// (exposed for free by controller-runtime's process collector) rather than container-restart
// metrics — the former reflects controller-process restarts even when a supervisor keeps the
// container alive.
//
// A nil *ConfigErrors is a valid no-op: Inc on a nil receiver does nothing, so the reconcile path
// can call it unconditionally and construction/registration stays gated on metrics being enabled.
type ConfigErrors struct {
	total *prometheus.CounterVec
}

// NewConfigErrors builds the counter and pre-initializes all reason series to 0 so they are
// present (not absent) from the first scrape and rate()/increase() have a baseline. prefix must
// already be validated (see internal/common/metrics.ValidatePrefix).
func NewConfigErrors(prefix string) *ConfigErrors {
	c := &ConfigErrors{
		total: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: prefix + "_sidecar_config_errors_total",
			Help: "Number of failed netlink operations in the sidecar reconcile path, by reason.",
		}, []string{"reason"}),
	}
	for _, reason := range []string{ReasonLink, ReasonAddress, ReasonRoute} {
		c.total.WithLabelValues(reason)
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
	c.total.WithLabelValues(reason).Inc()
}
