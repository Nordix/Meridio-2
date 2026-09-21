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

// Package networksidecar implements the network-sidecar binary's custom Prometheus metrics.
//
// Metrics are lazy/pull-based: SidecarCollector reads the single EndpointNetworkConfiguration
// named after this Pod from the sidecar's informer cache (desired state, not applied kernel
// state) only when a scrape invokes Collect — never from a background loop or the reconcile path.
// This gives correct lifecycle behavior for free: a deleted ENC or a removed gateway/domain stops
// appearing in the next Collect, and Prometheus marks the series stale. Reading the cache is
// cheap and carries none of the subprocess/netlink safety burden that external-state collectors
// need.
//
// See internal/metrics/util.CacheSyncWaiter for why every Collect waits (bounded by a timeout)
// for the informer cache to sync before its Get.
//
// # Per-family coverage depends on ENC domain existence
//
// Series are emitted per Gateway (and per IP family for nexthops) only for families that have a
// domain in the ENC. The ENC controller's buildGatewayConnection
// (internal/controller/endpointnetworkconfiguration/resolve.go) emits a domain only when the
// family's subnet is NAD-attached, has non-empty VIPs or next-hops, and the Pod has a matching
// interface — a GatewayConfiguration declaring the family's InternalSubnet is not sufficient on
// its own. So a supported family with nothing resolved produces no domain, hence no series at all
// for it (indistinguishable from "family not configured"); only the within-domain empty case is
// visible (e.g. next-hops but no VIPs still emits vips_configured=0, since that requires the
// domain to exist). If this is not acceptable, the fix belongs in the ENC controller (emit the
// domain with empty VIPs/next-hops so the family is present in the ENC).
package networksidecar

import (
	"context"
	"fmt"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	meridio2v1alpha1 "github.com/nordix/meridio-2/api/v1alpha1"
	metricsutil "github.com/nordix/meridio-2/internal/metrics/util"
)

// SidecarCollector is a prometheus.Collector exposing EndpointNetworkConfiguration-derived
// metrics for the single Pod this sidecar runs in:
//   - <prefix>_sidecar_vips_configured: count of VIPs per Gateway (label: gateway)
//   - <prefix>_sidecar_nexthops: count of next-hops per Gateway and IP family (labels: gateway, ip_family)
//
// Both read from the ENC spec (desired state). The label sets follow the finalized #153 proposal
// as-is: vips_configured is split only by gateway, while nexthops is additionally split by
// ip_family (useful for debugging ECMP, where v4 and v6 return paths diverge). Neither carries a
// pod label — the sidecar exposes its own per-Pod /metrics endpoint, so Pod identity is 1:1 with
// the scrape target and is supplied by the scraper's target labels (instance/pod) rather than
// instrumented here.
type SidecarCollector struct {
	client         client.Client
	syncGate       *metricsutil.SyncGate
	collectTimeout time.Duration
	podName        string
	podNamespace   string

	vipsDesc     *prometheus.Desc
	nexthopsDesc *prometheus.Desc
}

// NewSidecarCollector creates a SidecarCollector. prefix must already be validated (see
// internal/common/metrics.ValidatePrefix). cacheWaiter is typically the manager's own cache
// (mgr.GetCache()); collectTimeout bounds how long Collect will wait for it to sync before
// giving up and reporting a collection error for that scrape — see metricsutil.CacheSyncWaiter.
//
// podName/podNamespace identify the single ENC to read (named after the Pod, in the Pod's
// namespace) — matching the sidecar controller's own single-object cache scoping.
func NewSidecarCollector(
	c client.Client, cacheWaiter metricsutil.CacheSyncWaiter, collectTimeout time.Duration,
	podName, podNamespace, prefix string,
) *SidecarCollector {
	return &SidecarCollector{
		client:         c,
		syncGate:       metricsutil.NewSyncGate(cacheWaiter),
		collectTimeout: collectTimeout,
		podName:        podName,
		podNamespace:   podNamespace,
		vipsDesc: prometheus.NewDesc(
			prefix+"_sidecar_vips_configured",
			"Number of VIP addresses configured on this Pod for the given Gateway, from the ENC spec.",
			[]string{"gateway"}, nil,
		),
		nexthopsDesc: prometheus.NewDesc(
			prefix+"_sidecar_nexthops",
			"Number of next-hops configured on this Pod for the given Gateway and IP family, from the ENC spec.",
			[]string{"gateway", "ip_family"}, nil,
		),
	}
}

// Describe implements prometheus.Collector.
func (c *SidecarCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.vipsDesc
	ch <- c.nexthopsDesc
}

// Collect implements prometheus.Collector. It reads the Pod's ENC from the informer cache fresh
// on every call, first waiting for the cache to sync — bounded by collectTimeout, a cheap no-op
// once synced (see metricsutil.CacheSyncWaiter / SyncGate).
//
// A missing ENC (NotFound) is not an error: it means the Pod has no network configuration yet,
// so no series are emitted (the correct "nothing configured" state — Prometheus marks any
// previously-emitted series stale). On sync-timeout or a non-NotFound Get failure it emits an
// invalid metric rather than returning silently: Collect has no error return, so this is the
// only way to distinguish a collection failure from "nothing configured". The registry folds it
// into Gather's error; other collectors are unaffected.
func (c *SidecarCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), c.collectTimeout)
	defer cancel()

	if !c.syncGate.Wait(ctx) {
		ch <- prometheus.NewInvalidMetric(c.vipsDesc, fmt.Errorf("informer cache did not sync within %s", c.collectTimeout))
		return
	}

	var enc meridio2v1alpha1.EndpointNetworkConfiguration
	key := types.NamespacedName{Name: c.podName, Namespace: c.podNamespace}
	if err := c.client.Get(ctx, key, &enc); err != nil {
		if apierrors.IsNotFound(err) {
			return // no ENC yet: nothing configured, emit no series
		}
		ch <- prometheus.NewInvalidMetric(c.vipsDesc, err)
		return
	}

	for i := range enc.Spec.Gateways {
		gw := &enc.Spec.Gateways[i]

		// VIPs: summed across the gateway's domains (label: gateway only).
		var vipCount float64
		// Next-hops: summed per IP family across the gateway's domains (labels: gateway, ip_family).
		// A domain contributes its family key even when it has zero next-hops, so a
		// configured-but-empty family emits nexthops{ip_family=...}=0 rather than being omitted —
		// "present but empty" stays distinguishable from "absent".
		nexthopsByFamily := make(map[string]float64)
		for j := range gw.Domains {
			domain := &gw.Domains[j]
			vipCount += float64(len(domain.VIPs))
			nexthopsByFamily[domain.IPFamily] += float64(len(domain.NextHops))
		}

		ch <- prometheus.MustNewConstMetric(c.vipsDesc, prometheus.GaugeValue, vipCount, gw.Name)
		for family, count := range nexthopsByFamily {
			ch <- prometheus.MustNewConstMetric(c.nexthopsDesc, prometheus.GaugeValue, count, gw.Name, family)
		}
	}
}
