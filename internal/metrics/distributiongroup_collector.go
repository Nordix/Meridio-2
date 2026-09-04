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

package metrics

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	meridio2v1alpha1 "github.com/nordix/meridio-2/api/v1alpha1"
	"github.com/nordix/meridio-2/internal/common/gatewayutil"
	"github.com/nordix/meridio-2/internal/controller/distributiongroup"
)

// DistributionGroupCollector is a prometheus.Collector exposing DistributionGroup-derived metrics:
//   - <prefix>_distributiongroup_endpoints: current endpoint count for this DG under a given Gateway
//   - <prefix>_distributiongroup_max_endpoints: upper bound on endpoint count for this DG under a given Gateway
//   - <prefix>_distributiongroup_ready: 0/1 per DG, from the Ready status condition (DG-wide, no Gateway dimension)
//
// # Namespace disambiguation
//
// ready carries a "namespace" label (the DG's); endpoints and max_endpoints additionally carry a
// separate "gateway_namespace" alongside "gateway". This follows the kube-state-metrics
// convention of a name label paired with its own namespace label rather than folding namespace
// into the name. It matters because the controller-manager can watch all namespaces (empty
// --namespace), and a DG's resolved Gateway can live in a different namespace than the DG — so
// "gateway"/"dg" names alone are not unique across the metric stream.
//
// # ready has no Gateway dimension
//
// Ready is DG-wide, not per-Gateway: DistributionGroupReconciler.updateStatus sets it from
// hasEndpoints := len(desiredSlices) > 0, an OR across every Gateway's slices — the reconciler
// has no "Ready under Gateway A but not B" concept. So ready carries only "dg"/"namespace", one
// series per DG, reflecting distributiongroup.IsReady(dg) as-is.
//
// Note: IsReady currently means "has any assigned endpoint" (a slice exists), not "has any ready
// endpoint" — per-endpoint LoadBalancerEndpoint.Ready is not consulted. This metric mirrors the
// existing condition as-is rather than inventing a metrics-only readiness under the same name;
// whether the condition itself should consider per-endpoint readiness is a separate open design
// question for the reconciler.
//
// # Gateway label semantics (endpoints, max_endpoints)
//
// A DG's Gateway association for these two metrics is the union of two independently-derived sets:
//   - Referenced-and-accepted Gateways: distributiongroup.ListReferencedGateways (the reconciler's
//     own parentRef/L34Route-backendRef walk) filtered by gatewayutil.IsGatewayAcceptedByController. Covers
//     a DG newly bound to a Gateway before any slices exist — endpoints is correctly 0 there,
//     rather than the Gateway being absent from the stream.
//   - Gateways with currently-owned LoadBalancerEndpointSlices, read off each slice's
//     Spec.GatewayRef. Covers slices still lingering for a Gateway no longer referenced/accepted
//     (e.g. between unlinking and the reconciler's next cleanup) — reported as-is, since the goal
//     is to report durable state, not adjudicate staleness.
//
// A DG with an empty union (unbound: no accepted reference, no owned slices) still emits
// endpoints=0 and max_endpoints under gateway=""/gateway_namespace="", so it stays visible.
//
// # endpoints and max_endpoints are both per-Gateway
//
// endpoints is per-Gateway because each owned slice is scoped to one Gateway via Spec.GatewayRef,
// so a DG spanning multiple Gateways has a distinct count under each; a single DG-wide sum
// broadcast across every Gateway label would misreport (A's count would include B's slices).
//
// max_endpoints is a genuine per-Gateway cap, not just a per-Gateway-attributed shared value: for
// Maglev, MaglevConfig's doc states IDs are assigned per DG and per Gateway — each Gateway gets
// its own independent Maglev table, and the LB controller uses per-DG-per-Gateway ID offsets for
// fwmark routing. So endpoints{gateway="A"}/max_endpoints{gateway="A"} is a valid per-Gateway
// utilization query. dg.Spec.Maglev.MaxEndpoints is one DG-wide configured value by design,
// applied independently to each Gateway's table (not a shared pool split across them); this
// metric reports that same cap per Gateway, matching how it is enforced.
//
// # max_endpoints is not Maglev-specific
//
// DistributionGroupSpec.Type is an extensible discriminator (only Maglev exists today). This
// metric is kept generic — one name, always emitted for every DG regardless of Type — since a
// capacity concept is plausible for future strategies. resolveMaxEndpoints switches on Type so a
// future type gets a deliberate branch rather than silently inheriting Maglev's default. A type
// with no bounded-capacity concept reports +Inf ("no upper bound") rather than omitting the
// series: an absent series is indistinguishable from "collection failed"/"DG gone", whereas +Inf
// is a normal queryable float (Prometheus's own le="+Inf" convention) that makes
// endpoints/max_endpoints evaluate to 0, not NaN.
type DistributionGroupCollector struct {
	client         client.Client
	syncGate       *syncGate
	collectTimeout time.Duration
	namespace      string // "" watches all namespaces, mirrors ManagerConfig.Namespace
	controllerName string

	endpointsDesc    *prometheus.Desc
	maxEndpointsDesc *prometheus.Desc
	readyDesc        *prometheus.Desc
}

// gatewayRef identifies a Gateway by name and namespace, used both as the map key for
// per-Gateway endpoint counts and as the source of the "gateway"/"gateway_namespace" label pair.
type gatewayRef struct {
	name      string
	namespace string
}

// NewDistributionGroupCollector creates a DistributionGroupCollector. prefix must already be
// validated (see internal/common/metrics.ValidatePrefix). cacheWaiter is typically the
// manager's own cache (mgr.GetCache()); collectTimeout bounds how long Collect will wait for it
// to sync before giving up and reporting a collection error for that scrape — see
// CacheSyncWaiter.
func NewDistributionGroupCollector(
	c client.Client, cacheWaiter CacheSyncWaiter, collectTimeout time.Duration, namespace, controllerName, prefix string,
) *DistributionGroupCollector {
	gatewayLabels := []string{"gateway", "gateway_namespace", "dg", "namespace"}
	return &DistributionGroupCollector{
		client:         c,
		syncGate:       newSyncGate(cacheWaiter),
		collectTimeout: collectTimeout,
		namespace:      namespace,
		controllerName: controllerName,
		endpointsDesc: prometheus.NewDesc(
			prefix+"_distributiongroup_endpoints",
			"Current endpoint count for this DistributionGroup under the given Gateway.",
			gatewayLabels, nil,
		),
		maxEndpointsDesc: prometheus.NewDesc(
			prefix+"_distributiongroup_max_endpoints",
			"Upper bound on endpoint count for this DistributionGroup under the given Gateway, "+
				"per its distribution strategy (spec.type). For Maglev this is the hash table "+
				"capacity (spec.maglev.maxEndpoints); +Inf for any strategy without a bounded "+
				"capacity concept.",
			gatewayLabels, nil,
		),
		readyDesc: prometheus.NewDesc(
			prefix+"_distributiongroup_ready",
			"Whether the DistributionGroup's Ready status condition is currently True, as 0 or 1. "+
				"DG-wide (no Gateway dimension).",
			[]string{"dg", "namespace"}, nil,
		),
	}
}

// Describe implements prometheus.Collector.
func (c *DistributionGroupCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.endpointsDesc
	ch <- c.maxEndpointsDesc
	ch <- c.readyDesc
}

// Collect implements prometheus.Collector. It lists DistributionGroups (and per DG resolves
// referenced Gateways and owned slices) from the informer cache fresh on every call (safe per
// the package doc), first waiting for the cache to sync — bounded by collectTimeout, a cheap
// no-op once synced (see CacheSyncWaiter / syncGate). The up-front gate matters especially here,
// where one Collect reads four object types.
func (c *DistributionGroupCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), c.collectTimeout)
	defer cancel()

	if !c.syncGate.Wait(ctx) {
		ch <- prometheus.NewInvalidMetric(c.readyDesc, fmt.Errorf("informer cache did not sync within %s", c.collectTimeout))
		return
	}

	var dgList meridio2v1alpha1.DistributionGroupList
	listOpts := []client.ListOption{}
	if c.namespace != "" {
		listOpts = append(listOpts, client.InNamespace(c.namespace))
	}
	if err := c.client.List(ctx, &dgList, listOpts...); err != nil {
		ch <- prometheus.NewInvalidMetric(c.readyDesc, err)
		return
	}

	for i := range dgList.Items {
		c.collectDG(ctx, ch, &dgList.Items[i])
	}
}

// collectDG emits the DistributionGroup metrics for a single DG: ready once (DG-wide, no
// Gateway dimension), and endpoints/max_endpoints once per Gateway in the resolved union (see
// the "Gateway label semantics" section of the DistributionGroupCollector doc).
func (c *DistributionGroupCollector) collectDG(
	ctx context.Context, ch chan<- prometheus.Metric, dg *meridio2v1alpha1.DistributionGroup,
) {
	ready := 0.0
	if distributiongroup.IsReady(dg) {
		ready = 1.0
	}
	ch <- prometheus.MustNewConstMetric(c.readyDesc, prometheus.GaugeValue, ready, dg.Name, dg.Namespace)

	endpointsByGateway, err := c.countOwnedEndpointsByGateway(ctx, dg)
	if err != nil {
		ch <- prometheus.NewInvalidMetric(c.endpointsDesc, err)
		return
	}

	maxEndpoints := resolveMaxEndpoints(dg)

	gateways, err := c.resolveGatewayRefs(ctx, dg, endpointsByGateway)
	if err != nil {
		ch <- prometheus.NewInvalidMetric(c.endpointsDesc, err)
		return
	}

	for _, gw := range gateways {
		endpoints := endpointsByGateway[gw] // 0 for a Gateway with no owned slices yet
		ch <- prometheus.MustNewConstMetric(c.endpointsDesc, prometheus.GaugeValue, endpoints, gw.name, gw.namespace, dg.Name, dg.Namespace)
		ch <- prometheus.MustNewConstMetric(c.maxEndpointsDesc, prometheus.GaugeValue, maxEndpoints, gw.name, gw.namespace, dg.Name, dg.Namespace)
	}
}

// resolveGatewayRefs returns the Gateway(s) to emit endpoints/max_endpoints under for dg: the
// union of referenced-and-accepted Gateways (distributiongroup.ListReferencedGateways +
// gatewayutil.IsGatewayAcceptedByController) and Gateways with owned slices (endpointsByGateway's keys) —
// see the "Gateway label semantics" section of the DistributionGroupCollector doc for why both
// sets matter. Falls back to a single zero-value gatewayRef only when the union is empty
// (genuinely unbound).
func (c *DistributionGroupCollector) resolveGatewayRefs(
	ctx context.Context, dg *meridio2v1alpha1.DistributionGroup, endpointsByGateway map[gatewayRef]float64,
) ([]gatewayRef, error) {
	refSet := make(map[gatewayRef]struct{}, len(endpointsByGateway))
	for ref := range endpointsByGateway {
		refSet[ref] = struct{}{}
	}

	referenced, err := distributiongroup.ListReferencedGateways(ctx, c.client, c.namespace, dg)
	if err != nil {
		return nil, err
	}
	for i := range referenced {
		if gatewayutil.IsGatewayAcceptedByController(&referenced[i], c.controllerName) {
			refSet[gatewayRef{name: referenced[i].Name, namespace: referenced[i].Namespace}] = struct{}{}
		}
	}

	if len(refSet) == 0 {
		return []gatewayRef{{}}, nil
	}

	refs := make([]gatewayRef, 0, len(refSet))
	for ref := range refSet {
		refs = append(refs, ref)
	}
	return refs, nil
}

// countOwnedEndpointsByGateway returns the endpoint count per Gateway across
// LoadBalancerEndpointSlices owned by dg, mirroring the reconciler's own ownership check (see
// DistributionGroupReconciler.listOwnedSlices): field-indexed by spec.distributionGroupName,
// narrowed to slices actually controlled by this DG (guards against manually-created slices with
// a matching name but no ownerRef). Each slice is scoped to one Gateway via Spec.GatewayRef, so
// counts are keyed per (name, namespace) rather than summed DG-wide — see the "endpoints and
// max_endpoints are both per-Gateway" section of the DistributionGroupCollector doc for why.
func (c *DistributionGroupCollector) countOwnedEndpointsByGateway(
	ctx context.Context, dg *meridio2v1alpha1.DistributionGroup,
) (map[gatewayRef]float64, error) {
	var sliceList meridio2v1alpha1.LoadBalancerEndpointSliceList
	if err := c.client.List(ctx, &sliceList,
		client.InNamespace(dg.Namespace),
		client.MatchingFields{"spec.distributionGroupName": dg.Name},
	); err != nil {
		return nil, err
	}

	counts := make(map[gatewayRef]float64)
	for i := range sliceList.Items {
		slice := &sliceList.Items[i]
		if !metav1.IsControlledBy(slice, dg) {
			continue
		}
		ref := gatewayRef{name: slice.Spec.GatewayRef.Name, namespace: slice.Spec.GatewayRef.Namespace}
		counts[ref] += float64(len(slice.Spec.Endpoints))
	}
	return counts, nil
}

// resolveMaxEndpoints returns the upper bound on endpoint count for dg, per its distribution
// strategy (dg.Spec.Type). Switches explicitly on Type rather than only checking
// dg.Spec.Maglev != nil, so that a future DistributionGroupType gets a deliberate branch here
// instead of silently falling into the Maglev default — see the "max_endpoints is not
// Maglev-specific" doc on DistributionGroupCollector.
func resolveMaxEndpoints(dg *meridio2v1alpha1.DistributionGroup) float64 {
	switch dg.Spec.Type {
	case meridio2v1alpha1.DistributionGroupTypeMaglev:
		if dg.Spec.Maglev != nil {
			return float64(dg.Spec.Maglev.MaxEndpoints)
		}
		return float64(meridio2v1alpha1.DefaultMaglevMaxEndpoints)
	default:
		// No known bounded-capacity concept for this distribution strategy: report "no upper
		// bound" rather than omitting the series (see the doc above for why +Inf, not omission).
		return math.Inf(1)
	}
}
