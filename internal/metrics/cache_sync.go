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
	"sync/atomic"
)

// CacheSyncWaiter is the minimal interface a collector needs to guard Collect against reading
// from an informer cache that hasn't finished its initial sync yet.
//
// Why the guard is needed: controller-runtime starts the metrics HTTP server before it syncs
// the manager's caches (the metrics server shares the HTTP-server runnable group, which starts
// early so health probes and webhooks stay reachable during cache sync). So a scrape can run a
// collector's Collect before the caches are synced. Without a guard, client.Client.List would
// block on the missing sync using context.Background() with no way to time out, since
// prometheus.Collector.Collect has no context to inherit a deadline from.
//
// Why guard once at the top of Collect (rather than relying on each List): a cached List never
// returns partial data — it returns the fully-synced result, or errors if sync doesn't finish
// within collectTimeout. But Collect can emit metrics incrementally, so a scrape in the startup
// window could emit early types' series and then have a later type's List time out — a
// half-populated scrape that is misleading to read. Gating once up front makes that window fail
// fast with a single error before any metric is emitted.
//
// Satisfied directly by controller-runtime's cache.Cache (and thus ctrl.Manager.GetCache());
// declared narrowly so collectors depend only on this one method.
//
// Semantics relied on by syncGate (below): WaitForCacheSync (client-go's) ANDs the HasSynced of
// EVERY currently-tracked informer (not just the GVKs a collector reads), polling until all are
// true or ctx is done. Each HasSynced is a one-way latch — once its first full LIST completes it
// never regresses to false — so syncGate can skip the check permanently once it has observed true.
type CacheSyncWaiter interface {
	WaitForCacheSync(ctx context.Context) bool
}

// syncGate wraps a CacheSyncWaiter with a "synced once" latch: once WaitForCacheSync returns
// true, later Wait calls are a cheap atomic load instead of re-running the poll-every-informer
// path on every scrape — safe because that result never regresses (see CacheSyncWaiter). Until
// it first observes true, Wait delegates on every call and retries, never caching a false.
type syncGate struct {
	waiter CacheSyncWaiter
	synced atomic.Bool
}

// newSyncGate wraps waiter in a syncGate.
func newSyncGate(waiter CacheSyncWaiter) *syncGate {
	return &syncGate{waiter: waiter}
}

// Wait reports whether the cache is synced, per the latch behavior described on syncGate.
func (g *syncGate) Wait(ctx context.Context) bool {
	if g.synced.Load() {
		return true
	}
	if g.waiter.WaitForCacheSync(ctx) {
		g.synced.Store(true)
		return true
	}
	return false
}
