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
	"testing"

	"github.com/stretchr/testify/assert"
)

// fakeCacheSyncWaiter is a CacheSyncWaiter test double whose WaitForCacheSync return value and
// call count are controlled by the test.
type fakeCacheSyncWaiter struct {
	results []bool // consumed in order, one per call; last value repeats once exhausted
	calls   int
}

func (f *fakeCacheSyncWaiter) WaitForCacheSync(_ context.Context) bool {
	f.calls++
	if len(f.results) == 0 {
		return false
	}
	idx := f.calls - 1
	if idx >= len(f.results) {
		idx = len(f.results) - 1
	}
	return f.results[idx]
}

func TestSyncGate_WaitTrue_LatchesAndSkipsFurtherCalls(t *testing.T) {
	waiter := &fakeCacheSyncWaiter{results: []bool{true}}
	gate := newSyncGate(waiter)

	assert.True(t, gate.Wait(context.Background()))
	assert.True(t, gate.Wait(context.Background()))
	assert.True(t, gate.Wait(context.Background()))

	// Once synced, Wait must not call the underlying waiter again — that's the whole point of
	// the latch (avoid re-invoking WaitForCacheSync's lock-and-poll path on every scrape).
	assert.Equal(t, 1, waiter.calls, "expected exactly one underlying call once synced")
}

func TestSyncGate_WaitFalse_RetriesOnEveryCallUntilSuccess(t *testing.T) {
	waiter := &fakeCacheSyncWaiter{results: []bool{false, false, true}}
	gate := newSyncGate(waiter)

	assert.False(t, gate.Wait(context.Background()))
	assert.False(t, gate.Wait(context.Background()))
	assert.True(t, gate.Wait(context.Background()))

	// Three underlying calls: the negative results must not be cached, only the positive one.
	assert.Equal(t, 3, waiter.calls)

	// After the first success, subsequent calls must not hit the waiter again.
	assert.True(t, gate.Wait(context.Background()))
	assert.Equal(t, 3, waiter.calls, "expected no additional calls after first success")
}

func TestSyncGate_NeverSucceeds_CallsEveryTime(t *testing.T) {
	waiter := &fakeCacheSyncWaiter{results: []bool{false}}
	gate := newSyncGate(waiter)

	for i := 1; i <= 5; i++ {
		assert.False(t, gate.Wait(context.Background()))
		assert.Equal(t, i, waiter.calls, "expected one underlying call per Wait while never synced")
	}
}
