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

// Package router implements the router binary's custom Prometheus metrics.
//
// The session metrics are a direct reflection of BIRD state: RouterCollector runs its own `birdc`
// queries and derives every series from that output alone. It does not consult GatewayRouter CRDs
// and does not depend on the connectivity monitor's BFD enrichment (applyBfdState in
// cmd/router/cmd/run.go), so each metric is keyed by its own BIRD object — a BGP protocol and a
// BFD session are distinct objects with distinct identities. See
// docs/design/router-metrics-source-of-truth.md for the full rationale (notably: a BFD session can
// back multiple protocols, so it cannot be keyed by a single GatewayRouter).
//
// Metrics are lazy/pull-based: the queries run only when a scrape invokes Collect, never from a
// background loop. This gives correct lifecycle behavior for free: a session BIRD no longer
// reports stops appearing in the next Collect, and Prometheus marks the series stale. Nothing is
// retained to zero-seed or unregister, and absence is the truthful "no such session" signal rather
// than a forged zero.
//
// Collection is decoupled from the router's BIRD management: the collector holds its own
// bird.Client, which is stateless (a socket path and a command runner), rather than the bird.Bird
// value the controller reconfigures through. A scrape therefore never touches that value or the
// mutex serializing its reconfiguration.
//
// Collect is deliberately not serialized against itself. A single Prometheus instance keeps one
// scrape per target in flight, so overlap requires a second scraper or a manual request, and the
// collect timeout (kept below Prometheus's scrape_timeout) bounds every invocation and reaps the
// birdc process with it. Concurrent scrapes therefore cost a few extra short-lived, read-only
// forks rather than piling up, and a mutex would instead make one scrape wait out another's full
// timeout.
package router

import (
	"context"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/nordix/meridio-2/internal/bird"
)

// protocolNamePrefix is the prefix the router's BIRD config gives every GatewayRouter-derived
// protocol, so stripping it recovers the GatewayRouter name.
//
// TODO: consolidate this prefix. It is currently hardcoded at every site that builds or matches a
// protocol name — the BGP and static protocol templates (internal/bird/config.go), the `birdc`
// name filter and the parser's prefix gate (internal/bird/monitor.go), the family map key
// (internal/controller/router/connectivity.go), and the BFD correlation lookup
// (cmd/router/cmd/run.go) — using three different construction forms. It belongs in internal/bird
// as one exported const with helpers for building the protocol name from a GatewayRouter name and
// inverting it, with all sites (including this one) switched over.
const protocolNamePrefix = "NBR-"

// Source label values for the collect-success gauge. They name the sub-part of this collector's
// own gathering, not the underlying `birdc` command.
const (
	sourceBGPSessions = "bgp_sessions"
	sourceBFDSessions = "bfd_sessions"
)

// RouterCollector is a prometheus.Collector exposing BIRD-derived BGP and BFD session metrics for
// the single Gateway this router serves:
//   - <prefix>_bgp_session_up (labels: gateway, gateway_router)
//   - <prefix>_bgp_routes_imported (labels: gateway, gateway_router)
//   - <prefix>_bgp_prefixes_announced (labels: gateway, gateway_router)
//   - <prefix>_bgp_session_uptime_seconds (labels: gateway, gateway_router)
//   - <prefix>_bfd_session_up (labels: gateway, neighbor, interface)
//   - <prefix>_router_metrics_collect_success (labels: gateway, source)
//
// Each metric is labeled with its own BIRD object's natural key, and each label name denotes
// exactly one kind of identity. A BGP protocol is keyed by name, so the bgp_* metrics carry
// `gateway_router`: the protocol name with the prefix stripped, which equals the GatewayRouter
// name — read from BIRD's own protocol key, not resolved against the CRD. A BFD session is keyed
// by its endpoint rather than by any protocol that requested it, so bfd_session_up carries
// `neighbor` plus `interface`: together they form the session key (a neighbor IP alone is not
// unique across interfaces), and neither ties the session to a GatewayRouter, which would be
// wrong since one session can back several.
//
// A single label such as "peer" shared across both — naming the GatewayRouter on the bgp_* metrics
// and a neighbor IP on bfd_session_up — is avoided on purpose. It would make one label name mean
// two different things depending on the metric, and leave it useless for joining across them.
//
// The bgp_* metrics are emitted only for BGP protocols; a GatewayRouter using static routing
// produces no bgp_* series at all (its connectivity is observed via bfd_session_up). The route
// count and timestamp series are additionally omitted when BIRD did not report the underlying
// value for that protocol — the parsed fields are nil in that case, meaning "not applicable",
// which is distinct from zero.
//
// No pod label is instrumented: each router exposes its own per-Pod /metrics endpoint, so Pod
// identity is 1:1 with the scrape target and is supplied by the scraper (instance/pod). The
// gateway label is kept because it is semantically meaningful and not auto-injected.
type RouterCollector struct {
	client         *bird.Client
	collectTimeout time.Duration
	gatewayName    string

	bgpSessionUp      *prometheus.Desc
	bgpRoutesImported *prometheus.Desc
	bgpPrefixesAnn    *prometheus.Desc
	bgpSessionUptime  *prometheus.Desc
	bfdSessionUp      *prometheus.Desc
	collectSuccess    *prometheus.Desc
}

// NewRouterCollector creates a RouterCollector. prefix must already be validated (see
// internal/common/metrics.ValidatePrefix). client queries the BIRD control socket; give the
// collector its own rather than sharing the router's, so it does not depend on how BIRD management
// is constructed. collectTimeout bounds the total time a single scrape spends on those queries, and
// should stay below Prometheus's scrape_timeout. gatewayName is the single Gateway this router
// serves, used as the `gateway` label value on every series.
//
// The metric help texts refer to the GatewayRouter as the remote end of the session, matching the
// CRD: a GatewayRouter describes the external router this side peers with (its address and remote
// ASN), so routes are imported from it and prefixes announced to it.
func NewRouterCollector(
	client *bird.Client, collectTimeout time.Duration, gatewayName, prefix string,
) *RouterCollector {
	bgpLabels := []string{"gateway", "gateway_router"}
	return &RouterCollector{
		client:         client,
		collectTimeout: collectTimeout,
		gatewayName:    gatewayName,
		bgpSessionUp: prometheus.NewDesc(
			prefix+"_bgp_session_up",
			"Whether the BGP session to the given GatewayRouter is established (1) or not (0), as reported by BIRD.",
			bgpLabels, nil,
		),
		bgpRoutesImported: prometheus.NewDesc(
			prefix+"_bgp_routes_imported",
			"Number of routes imported from the given GatewayRouter, as reported by BIRD.",
			bgpLabels, nil,
		),
		bgpPrefixesAnn: prometheus.NewDesc(
			prefix+"_bgp_prefixes_announced",
			"Number of prefixes announced to the given GatewayRouter, as reported by BIRD.",
			bgpLabels, nil,
		),
		bgpSessionUptime: prometheus.NewDesc(
			prefix+"_bgp_session_uptime_seconds",
			"Seconds the BGP session to the given GatewayRouter has been established, 0 while it is not. "+
				"A value lower than the time since the previous scrape means the session reset in between, "+
				"which makes flapping detectable even when it never appears down in a scrape.",
			bgpLabels, nil,
		),
		bfdSessionUp: prometheus.NewDesc(
			prefix+"_bfd_session_up",
			"Whether the BFD session to the given neighbor on the given interface is up (1) or not (0), "+
				"as reported by BIRD.",
			[]string{"gateway", "neighbor", "interface"}, nil,
		),
		collectSuccess: prometheus.NewDesc(
			prefix+"_router_metrics_collect_success",
			"Whether this scrape successfully gathered the given source from BIRD (1) or not (0). "+
				"Reports only the health of serving these metrics: it is independent of BGP/BFD session "+
				"state and of the router's own BIRD monitoring, so 0 means the data could not be read, "+
				"not that connectivity is down.",
			[]string{"gateway", "source"}, nil,
		),
	}
}

// Describe implements prometheus.Collector.
func (c *RouterCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.bgpSessionUp
	ch <- c.bgpRoutesImported
	ch <- c.bgpPrefixesAnn
	ch <- c.bgpSessionUptime
	ch <- c.bfdSessionUp
	ch <- c.collectSuccess
}

// Collect implements prometheus.Collector, querying BIRD fresh on every call.
//
// A failed query emits no session series for the affected source, and in particular does not forge
// zeros: a failed fetch must never be reported as "every session down". The failure is instead
// reported through the collect-success gauge, which keeps "nothing configured" (success=1, no
// series) distinguishable from "could not read" (success=0, no series).
//
// The failure is deliberately not reported to the registry via prometheus.NewInvalidMetric, as the
// cache-backed collectors do. That would fail the entire scrape (controller-runtime's metrics
// handler uses promhttp.HTTPErrorOnError, so a Gather error serves HTTP 500 instead of the
// payload), and Prometheus treats a failed scrape as an empty one, writing stale markers for
// *every* series on this endpoint — including the router's controller-runtime, Go-runtime and
// process metrics. Since `birdc` is a subprocess call against a Unix socket and can fail
// transiently, that collateral loss is worse than the absence of the session series alone. A
// scrape performs no actuation, so serving no session series for one scrape is benign. See the
// "Fetch-failure handling" section of docs/design/router-metrics-source-of-truth.md.
//
// The BGP and BFD queries are independent: if one fails the other's series are still emitted, and
// each reports its own collect-success value.
func (c *RouterCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), c.collectTimeout)
	defer cancel()

	protocols, protocolsErr := c.client.GetProtocolStatuses(ctx)
	if protocolsErr == nil {
		c.collectBGP(ch, protocols)
	}
	c.reportCollectOutcome(ch, sourceBGPSessions, protocolsErr)

	sessions, sessionsErr := c.client.GetBfdSessions(ctx)
	if sessionsErr == nil {
		c.collectBFD(ch, sessions)
	}
	c.reportCollectOutcome(ch, sourceBFDSessions, sessionsErr)
}

// reportCollectOutcome emits the collect-success gauge for one source, for both sources on every
// scrape, so the series exist from the first scrape on and a failure shows up as a 0 sample rather
// than as a newly-appearing series.
//
// The failure is deliberately not logged here. internal/bird already logs the underlying `birdc`
// failure at V(1) with the command, error and output, and this gauge is the signal intended for
// detection and alerting. A log line at scrape frequency would add nothing actionable while letting
// the scrape interval drive default-level log volume for as long as the fetch keeps failing.
func (c *RouterCollector) reportCollectOutcome(ch chan<- prometheus.Metric, source string, err error) {
	ch <- prometheus.MustNewConstMetric(
		c.collectSuccess, prometheus.GaugeValue, boolToFloat(err == nil), c.gatewayName, source)
}

// collectBGP emits the bgp_* series for every BGP protocol in protocols. Non-BGP protocols are
// skipped entirely. The route counts are emitted only when BIRD reported them, and the uptime only
// when it can be determined (see sessionUptime).
func (c *RouterCollector) collectBGP(ch chan<- prometheus.Metric, protocols []bird.ProtocolStatus) {
	now := time.Now().Unix()

	for i := range protocols {
		p := &protocols[i]
		if p.Proto != "BGP" {
			continue
		}
		gatewayRouter := strings.TrimPrefix(p.Name, protocolNamePrefix)
		established := p.IsEstablished()

		ch <- prometheus.MustNewConstMetric(
			c.bgpSessionUp, prometheus.GaugeValue, boolToFloat(established), c.gatewayName, gatewayRouter)

		if p.RoutesImported != nil {
			ch <- prometheus.MustNewConstMetric(
				c.bgpRoutesImported, prometheus.GaugeValue, float64(*p.RoutesImported), c.gatewayName, gatewayRouter)
		}
		if p.RoutesExported != nil {
			ch <- prometheus.MustNewConstMetric(
				c.bgpPrefixesAnn, prometheus.GaugeValue, float64(*p.RoutesExported), c.gatewayName, gatewayRouter)
		}
		if uptime, ok := sessionUptime(p, established, now); ok {
			ch <- prometheus.MustNewConstMetric(
				c.bgpSessionUptime, prometheus.GaugeValue, uptime, c.gatewayName, gatewayRouter)
		}
	}
}

// sessionUptime derives how long a BGP session has been established from BIRD's Since field,
// reporting 0 whenever the session is not established.
//
// The gate on established is what makes the value an uptime at all: BIRD reports Since for the
// protocol's current state, whatever that state is, so for a session that is down it is the time it
// went down. Subtracting that would yield a growing "downtime" under an uptime name.
//
// Returns ok=false when the duration cannot be determined — an established session whose Since was
// not parseable, i.e. bird.conf is missing `timeformat protocol "%s"`. The series is then omitted
// rather than reported as 0, which would be indistinguishable from a session that just reset.
//
// now is passed in so every protocol in one scrape shares a single reading, and comes from the same
// host clock that produced Since (BIRD runs in this container), so the subtraction carries no
// cross-host skew.
func sessionUptime(p *bird.ProtocolStatus, established bool, now int64) (float64, bool) {
	if !established {
		return 0, true
	}
	if p.LastStateChangeUnix == nil {
		return 0, false
	}
	return max(float64(now-*p.LastStateChangeUnix), 0), true
}

// collectBFD emits bfd_session_up per BFD session, keyed by neighbor IP and interface — the
// session's own identity in BIRD. Sessions are reported as-is, with no correlation to a
// GatewayRouter, because one session can underlie multiple protocols.
func (c *RouterCollector) collectBFD(ch chan<- prometheus.Metric, sessions []bird.BfdSession) {
	for _, s := range sessions {
		ch <- prometheus.MustNewConstMetric(
			c.bfdSessionUp, prometheus.GaugeValue, boolToFloat(s.IsUp()), c.gatewayName, s.IP, s.Interface)
	}
}

func boolToFloat(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
