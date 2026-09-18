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

// Package metrics implements the controller-manager's custom Prometheus
// metrics, defined in https://github.com/Nordix/Meridio-2/issues/153 and
// tracked for implementation in https://github.com/Nordix/Meridio-2/issues/236.
//
// All metrics here are lazy/pull-based: each Collector reads current state from the manager's
// informer cache (via client.Client.List/Get) only when Collect is invoked by a scrape, never
// from a background loop or the reconcile path. This decouples metrics from reconciliation and
// gives correct lifecycle behavior for free — a deleted Gateway or DistributionGroup stops
// appearing in the next Collect, and Prometheus marks the series stale on its own.
//
// See CacheSyncWaiter (cache_sync.go) for why every Collect waits for the informer cache to
// sync, bounded by a timeout, before issuing any List calls.
package metrics

import (
	"context"
	"fmt"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/nordix/meridio-2/internal/common/gatewayutil"
)

// GatewayCollector is a prometheus.Collector exposing Gateway-derived metrics:
//   - <prefix>_gateway_count: number of Gateways with Accepted=True managed by this controller
//   - <prefix>_gateway_programmed: 0/1 per Gateway, from the Programmed status condition
//
// Only Gateways accepted by controllerName count as "managed by this controller", matching the
// billing-relevant semantics finalized in issue #153. gateway_programmed is reported per such
// Gateway (Programmed is only meaningful once Accepted; Gateways not accepted by us are not ours
// to report on).
//
// gateway_programmed carries "gateway" and "namespace" labels, following the kube-state-metrics
// convention of a name label paired with a separate namespace label rather than folding
// namespace into the name. This matters because the controller-manager can watch all namespaces
// (empty --namespace): identically-named Gateways in different namespaces would otherwise collide
// into one series.
type GatewayCollector struct {
	client         client.Client
	syncGate       *syncGate
	collectTimeout time.Duration
	namespace      string // "" watches all namespaces, mirrors ManagerConfig.Namespace
	controllerName string

	countDesc      *prometheus.Desc
	programmedDesc *prometheus.Desc
}

// NewGatewayCollector creates a GatewayCollector. prefix must already be validated
// (see internal/common/metrics.ValidatePrefix). cacheWaiter is typically the manager's own
// cache (mgr.GetCache()); collectTimeout bounds how long Collect will wait for it to sync
// before giving up and reporting a collection error for that scrape — see CacheSyncWaiter.
func NewGatewayCollector(
	c client.Client, cacheWaiter CacheSyncWaiter, collectTimeout time.Duration, namespace, controllerName, prefix string,
) *GatewayCollector {
	return &GatewayCollector{
		client:         c,
		syncGate:       newSyncGate(cacheWaiter),
		collectTimeout: collectTimeout,
		namespace:      namespace,
		controllerName: controllerName,
		countDesc: prometheus.NewDesc(
			prefix+"_gateway_count",
			"Number of Gateways with Accepted=True managed by this controller.",
			nil, nil,
		),
		programmedDesc: prometheus.NewDesc(
			prefix+"_gateway_programmed",
			"Whether the Gateway's LB Deployment has been successfully reconciled (Programmed condition), as 0 or 1.",
			[]string{"gateway", "namespace"}, nil,
		),
	}
}

// Describe implements prometheus.Collector.
func (c *GatewayCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.countDesc
	ch <- c.programmedDesc
}

// Collect implements prometheus.Collector. It lists Gateways from the informer cache fresh on
// every call (safe to do unconditionally per the package doc), first waiting for the cache to
// sync — bounded by collectTimeout, a cheap no-op once synced (see CacheSyncWaiter / syncGate).
//
// On sync-timeout or List failure it emits an invalid metric rather than returning silently:
// Collect has no error return, so this is the only way to distinguish a collection failure from
// "zero Gateways exist". The registry folds it into Gather's error; other collectors are
// unaffected.
func (c *GatewayCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), c.collectTimeout)
	defer cancel()

	if !c.syncGate.Wait(ctx) {
		ch <- prometheus.NewInvalidMetric(c.countDesc, fmt.Errorf("informer cache did not sync within %s", c.collectTimeout))
		return
	}

	var gwList gatewayv1.GatewayList
	listOpts := []client.ListOption{}
	if c.namespace != "" {
		listOpts = append(listOpts, client.InNamespace(c.namespace))
	}
	if err := c.client.List(ctx, &gwList, listOpts...); err != nil {
		ch <- prometheus.NewInvalidMetric(c.countDesc, err)
		return
	}

	var acceptedCount float64
	for i := range gwList.Items {
		gw := &gwList.Items[i]
		if !gatewayutil.IsGatewayAcceptedByController(gw, c.controllerName) {
			continue
		}
		acceptedCount++

		programmed := 0.0
		if gatewayutil.IsGatewayProgrammed(gw) {
			programmed = 1.0
		}
		ch <- prometheus.MustNewConstMetric(c.programmedDesc, prometheus.GaugeValue, programmed, gw.Name, gw.Namespace)
	}

	ch <- prometheus.MustNewConstMetric(c.countDesc, prometheus.GaugeValue, acceptedCount)
}
