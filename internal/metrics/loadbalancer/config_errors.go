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

// Config-error reason label values for ConfigErrors.
//   - route_config: a policy rule/route apply failed in the LB reconcile path. NOTE: this covers
//     any AddTarget failure, which bundles policy-route creation (RuleAdd/RouteReplace) AND the
//     nfqlb target activation (activate) step — the controller cannot distinguish them from the
//     returned error, so an activation failure is also counted here. Read it as
//     "route-or-activation failure", not strictly a netlink-route failure.
//   - vip_config: an nftables VIP set update (SetVIPs) failed. Unambiguous (direct SetVIPs call).
const (
	ReasonRouteConfig = "route_config"
	ReasonVIPConfig   = "vip_config"
)

// ConfigErrors is a push-style counter of failed data-plane config operations in the LB's
// reconcile path (<prefix>_lb_route_config_errors_total, labels: gateway, reason). Unlike the
// lazily-collected read metrics it is not pulled at scrape time: a failed apply is a point-in-time
// event with no durable external source to read, so it is counted at the failure site.
//
// The gateway label is the bare Gateway name this LB serves (constant for the process lifetime);
// reason is a fixed enum (ReasonRouteConfig / ReasonVIPConfig).
//
// Being a counter, the value resets to zero on process restart. Consume via rate()/increase().
//
// A nil *ConfigErrors is a valid no-op: Inc on a nil receiver does nothing, so the reconcile path
// can call it unconditionally while construction/registration stays gated on metrics being enabled.
type ConfigErrors struct {
	gatewayName string
	total       *prometheus.CounterVec
}

// NewConfigErrors builds the counter and pre-initializes both reason series to 0 so they are
// present (not absent) from the first scrape and rate()/increase() have a baseline. prefix must
// already be validated (see internal/common/metrics.ValidatePrefix); gatewayName is the bare
// Gateway name used as the constant gateway label.
func NewConfigErrors(gatewayName, prefix string) *ConfigErrors {
	c := &ConfigErrors{
		gatewayName: gatewayName,
		total: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: prefix + "_lb_route_config_errors_total",
			Help: "Number of failed data-plane config operations in the LB reconcile path, by " +
				"reason (route_config: policy rule/route or target activation failure; " +
				"vip_config: nftables VIP set update failure).",
		}, []string{"gateway", "reason"}),
	}
	for _, reason := range []string{ReasonRouteConfig, ReasonVIPConfig} {
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
	c.total.WithLabelValues(c.gatewayName, reason).Inc()
}
