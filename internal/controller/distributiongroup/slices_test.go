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

package distributiongroup

import (
	"net"
	"strconv"
	"testing"

	meridio2v1alpha1 "github.com/nordix/meridio-2/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func newTestReconciler() *DistributionGroupReconciler {
	return &DistributionGroupReconciler{MaxEndpointsPerSlice: 200}
}

// newTestPod creates a podWithAddresses with the given IPs. Empty strings are skipped.
func newTestPod(uid, name string, ips ...string) podWithAddresses {
	addresses := make([]meridio2v1alpha1.EndpointAddress, 0, 2)
	for _, ip := range ips {
		if ip == "" {
			continue
		}
		family := meridio2v1alpha1.IPv4
		if net.ParseIP(ip).To4() == nil {
			family = meridio2v1alpha1.IPv6
		}
		addresses = append(addresses, meridio2v1alpha1.EndpointAddress{IP: ip, Family: family})
	}
	return podWithAddresses{
		pod:       corev1.Pod{ObjectMeta: metav1.ObjectMeta{UID: types.UID(uid), Name: name, Namespace: "default"}},
		addresses: addresses,
	}
}

// testGWCtx returns a minimal Gateway context for tests that don't exercise scraping.
// Only the gateway identity (name/namespace) matters — it populates spec.gatewayRef.
func testGWCtx() gatewayNetworkContext {
	return gatewayNetworkContext{
		gateway: client.ObjectKey{Name: "gw-a", Namespace: "default"},
	}
}

// getIDMap extracts UID → Identifier from slices
func getIDMap(slices []meridio2v1alpha1.LoadBalancerEndpointSlice) map[string]int32 {
	ids := make(map[string]int32)
	for _, s := range slices {
		for _, ep := range s.Spec.Endpoints {
			if ep.Identifier != nil {
				ids[ep.Target.UID] = *ep.Identifier
			}
		}
	}
	return ids
}

func TestCalculateMaglevSlices_StableAcrossReconciles(t *testing.T) {
	r := newTestReconciler()
	dg := &meridio2v1alpha1.DistributionGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "test-dg", Namespace: "default"},
		Spec: meridio2v1alpha1.DistributionGroupSpec{
			Type:   meridio2v1alpha1.DistributionGroupTypeMaglev,
			Maglev: &meridio2v1alpha1.MaglevConfig{MaxEndpoints: 32},
		},
	}

	scrapedPods := make([]podWithAddresses, 32)
	for i := range 32 {
		scrapedPods[i] = newTestPod(
			"uid-"+strconv.Itoa(i), "pod-"+strconv.Itoa(i),
			"192.168.100."+strconv.Itoa(10+i),
			"2001:db8:100::"+strconv.Itoa(10+i),
		)
	}

	gwCtx := testGWCtx()

	// First reconcile
	slices1, _ := r.calculateMaglevSlices(dg, gwCtx, scrapedPods, nil)

	// Second reconcile with first output as existing
	slices2, _ := r.calculateMaglevSlices(dg, gwCtx, scrapedPods, slices1)

	ids1 := getIDMap(slices1)
	ids2 := getIDMap(slices2)

	if len(ids1) != 32 || len(ids2) != 32 {
		t.Fatalf("Expected 32 IDs in each reconcile, got %d and %d", len(ids1), len(ids2))
	}

	for uid, id1 := range ids1 {
		if id2, exists := ids2[uid]; !exists {
			t.Errorf("Pod %s missing in second reconcile", uid)
		} else if id1 != id2 {
			t.Errorf("Pod %s ID changed between reconciles: %d → %d", uid, id1, id2)
		}
	}
}

func TestCalculateMaglevSlices_AsymmetricNetworkPresence(t *testing.T) {
	r := newTestReconciler()
	dg := &meridio2v1alpha1.DistributionGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "test-dg", Namespace: "default"},
		Spec: meridio2v1alpha1.DistributionGroupSpec{
			Type:   meridio2v1alpha1.DistributionGroupTypeMaglev,
			Maglev: &meridio2v1alpha1.MaglevConfig{MaxEndpoints: 32},
		},
	}

	scrapedPods := []podWithAddresses{
		newTestPod("uid-1", "pod-1", "192.168.100.10", "2001:db8:100::10"), // dual-stack
		newTestPod("uid-2", "pod-2", "192.168.100.11", "2001:db8:100::11"), // dual-stack
		newTestPod("uid-3", "pod-3", "192.168.100.12"),                     // IPv4 only
		newTestPod("uid-4", "pod-4", "2001:db8:100::14"),                   // IPv6 only
	}

	slices, _ := r.calculateMaglevSlices(dg, testGWCtx(), scrapedPods, nil)

	if len(slices) != 1 {
		t.Fatalf("Expected 1 slice, got %d", len(slices))
	}

	endpoints := slices[0].Spec.Endpoints
	if len(endpoints) != 4 {
		t.Fatalf("Expected 4 endpoints, got %d", len(endpoints))
	}

	// All 4 Pods should have unique identifiers
	usedIDs := make(map[int32]string)
	for _, ep := range endpoints {
		if ep.Identifier == nil {
			t.Errorf("Endpoint %s has no identifier", ep.Target.Name)
			continue
		}
		if prev, exists := usedIDs[*ep.Identifier]; exists {
			t.Errorf("ID %d collision between %s and %s", *ep.Identifier, prev, ep.Target.UID)
		}
		usedIDs[*ep.Identifier] = ep.Target.UID
	}

	// Verify address counts
	for _, ep := range endpoints {
		switch ep.Target.UID {
		case "uid-1", "uid-2":
			if len(ep.Addresses) != 2 {
				t.Errorf("Dual-stack pod %s should have 2 addresses, got %d (%v)", ep.Target.UID, len(ep.Addresses), ep.Addresses)
			}
		case "uid-3", "uid-4":
			if len(ep.Addresses) != 1 {
				t.Errorf("Single-stack pod %s should have 1 address, got %d", ep.Target.UID, len(ep.Addresses))
			}
		}
	}
}

func TestCalculateMaglevSlices_PartialPodReplacement(t *testing.T) {
	r := newTestReconciler()
	dg := &meridio2v1alpha1.DistributionGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "test-dg", Namespace: "default"},
		Spec: meridio2v1alpha1.DistributionGroupSpec{
			Type:   meridio2v1alpha1.DistributionGroupTypeMaglev,
			Maglev: &meridio2v1alpha1.MaglevConfig{MaxEndpoints: 32},
		},
	}

	gwCtx := testGWCtx()

	// Initial: 32 dual-stack Pods
	origPods := make([]podWithAddresses, 32)
	for i := range 32 {
		origPods[i] = newTestPod(
			"orig-"+strconv.Itoa(i), "pod-orig-"+strconv.Itoa(i),
			"192.168.100."+strconv.Itoa(10+i),
			"2001:db8:100::"+strconv.Itoa(10+i),
		)
	}

	// First reconcile
	slices1, _ := r.calculateMaglevSlices(dg, gwCtx, origPods, nil)
	ids1 := getIDMap(slices1)

	// Replace half (indices 16-31) with new Pods
	newPods := make([]podWithAddresses, 16)
	for i := range 16 {
		newPods[i] = newTestPod(
			"new-"+strconv.Itoa(i), "pod-new-"+strconv.Itoa(i),
			"192.168.100."+strconv.Itoa(50+i),
			"2001:db8:100::"+strconv.Itoa(50+i),
		)
	}
	mixedPods := append(origPods[:16], newPods...)

	// Second reconcile
	slices2, _ := r.calculateMaglevSlices(dg, gwCtx, mixedPods, slices1)
	ids2 := getIDMap(slices2)

	if len(ids2) != 32 {
		t.Fatalf("Expected 32 IDs, got %d", len(ids2))
	}

	// Surviving Pods must keep their IDs
	for i := range 16 {
		uid := "orig-" + strconv.Itoa(i)
		if ids1[uid] != ids2[uid] {
			t.Errorf("Surviving pod %s ID changed: %d → %d", uid, ids1[uid], ids2[uid])
		}
	}

	// New Pods must have IDs assigned
	for i := range 16 {
		uid := "new-" + strconv.Itoa(i)
		if _, exists := ids2[uid]; !exists {
			t.Errorf("New pod %s should have an ID", uid)
		}
	}
}

func TestCalculateMaglevSlices_CapacityExceeded(t *testing.T) {
	r := newTestReconciler()
	dg := &meridio2v1alpha1.DistributionGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "test-dg", Namespace: "default"},
		Spec: meridio2v1alpha1.DistributionGroupSpec{
			Type:   meridio2v1alpha1.DistributionGroupTypeMaglev,
			Maglev: &meridio2v1alpha1.MaglevConfig{MaxEndpoints: 4},
		},
	}

	// 6 Pods, capacity 4 — 2 should be excluded
	scrapedPods := make([]podWithAddresses, 6)
	for i := range 6 {
		scrapedPods[i] = newTestPod(
			"uid-"+strconv.Itoa(i), "pod-"+strconv.Itoa(i),
			"192.168.100."+strconv.Itoa(10+i),
			"2001:db8:100::"+strconv.Itoa(10+i),
		)
	}

	slices, capacityInfo := r.calculateMaglevSlices(dg, testGWCtx(), scrapedPods, nil)

	if capacityInfo == nil {
		t.Fatal("Expected capacity exceeded info")
	}
	if capacityInfo.excluded != 2 || capacityInfo.total != 6 {
		t.Errorf("Expected excluded=2, total=6, got excluded=%d, total=%d", capacityInfo.excluded, capacityInfo.total)
	}

	// Exactly 4 endpoints in result with unique identifiers
	usedIDs := make(map[int32]string)
	totalEndpoints := 0
	for _, s := range slices {
		totalEndpoints += len(s.Spec.Endpoints)
		for _, ep := range s.Spec.Endpoints {
			if ep.Identifier == nil {
				t.Errorf("Endpoint %s has no identifier", ep.Target.Name)
				continue
			}
			if prev, exists := usedIDs[*ep.Identifier]; exists {
				t.Errorf("ID %d collision between %s and %s", *ep.Identifier, prev, ep.Target.UID)
			}
			usedIDs[*ep.Identifier] = ep.Target.UID
		}

	}
	if totalEndpoints != 4 {
		t.Errorf("Expected 4 endpoints, got %d", totalEndpoints)
	}
}

func TestCreateSlices_NonMaglevIncludesAll(t *testing.T) {
	r := newTestReconciler()
	dg := &meridio2v1alpha1.DistributionGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "test-dg", Namespace: "default"},
	}

	scrapedPods := make([]podWithAddresses, 64)
	for i := range 64 {
		scrapedPods[i] = newTestPod(
			"uid-"+strconv.Itoa(i), "pod-"+strconv.Itoa(i),
			"10.0.0."+strconv.Itoa(10+i),
		)
	}

	slices := r.createSlices(dg, testGWCtx(), scrapedPods, nil, nil)

	totalEndpoints := 0
	for _, s := range slices {
		totalEndpoints += len(s.Spec.Endpoints)
	}
	if totalEndpoints != 64 {
		t.Errorf("Expected 64 endpoints (no capacity limit), got %d", totalEndpoints)
	}

	// No identifiers for non-Maglev
	for _, s := range slices {
		for _, ep := range s.Spec.Endpoints {
			if ep.Identifier != nil {
				t.Errorf("Non-Maglev endpoint should have no identifier, got %d", *ep.Identifier)
			}
		}
	}
}

func TestSliceNeedsUpdate(t *testing.T) {
	tests := []struct {
		name     string
		existing *meridio2v1alpha1.LoadBalancerEndpointSlice
		desired  *meridio2v1alpha1.LoadBalancerEndpointSlice
		expected bool
	}{
		{
			name: "no change",
			existing: &meridio2v1alpha1.LoadBalancerEndpointSlice{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"key": "value"}},
				Spec: meridio2v1alpha1.LoadBalancerEndpointSliceSpec{
					Endpoints: []meridio2v1alpha1.LoadBalancerEndpoint{
						{Target: meridio2v1alpha1.EndpointTarget{UID: "uid-1"}},
					},
				},
			},
			desired: &meridio2v1alpha1.LoadBalancerEndpointSlice{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"key": "value"}},
				Spec: meridio2v1alpha1.LoadBalancerEndpointSliceSpec{
					Endpoints: []meridio2v1alpha1.LoadBalancerEndpoint{
						{Target: meridio2v1alpha1.EndpointTarget{UID: "uid-1"}},
					},
				},
			},
			expected: false,
		},
		{
			name: "endpoints changed",
			existing: &meridio2v1alpha1.LoadBalancerEndpointSlice{
				Spec: meridio2v1alpha1.LoadBalancerEndpointSliceSpec{
					Endpoints: []meridio2v1alpha1.LoadBalancerEndpoint{
						{Target: meridio2v1alpha1.EndpointTarget{UID: "uid-1"}},
					},
				},
			},
			desired: &meridio2v1alpha1.LoadBalancerEndpointSlice{
				Spec: meridio2v1alpha1.LoadBalancerEndpointSliceSpec{
					Endpoints: []meridio2v1alpha1.LoadBalancerEndpoint{
						{Target: meridio2v1alpha1.EndpointTarget{UID: "uid-2"}},
					},
				},
			},
			expected: true,
		},
		{
			name: "labels changed",
			existing: &meridio2v1alpha1.LoadBalancerEndpointSlice{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"key": "old"}},
			},
			desired: &meridio2v1alpha1.LoadBalancerEndpointSlice{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"key": "new"}},
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := sliceNeedsUpdate(tt.existing, tt.desired)
			if result != tt.expected {
				t.Errorf("sliceNeedsUpdate() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestCreateSlices_SliceSplitting(t *testing.T) {
	r := &DistributionGroupReconciler{MaxEndpointsPerSlice: 100}
	dg := &meridio2v1alpha1.DistributionGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "test-dg", Namespace: "default"},
	}

	// 150 Pods should split into 2 slices (100 + 50)
	scrapedPods := make([]podWithAddresses, 150)
	for i := range 150 {
		scrapedPods[i] = newTestPod(
			"uid-"+strconv.Itoa(i), "pod-"+strconv.Itoa(i),
			"10.0.0."+strconv.Itoa(i),
		)
	}

	slices := r.createSlices(dg, testGWCtx(), scrapedPods, nil, nil)

	if len(slices) != 2 {
		t.Errorf("Expected 2 slices (100+50), got %d", len(slices))
	}

	totalEndpoints := 0
	for _, s := range slices {
		totalEndpoints += len(s.Spec.Endpoints)
		if len(s.Spec.Endpoints) > 100 {
			t.Errorf("Slice has %d endpoints, max should be 100", len(s.Spec.Endpoints))
		}
	}
	if totalEndpoints != 150 {
		t.Errorf("Expected 150 total endpoints, got %d", totalEndpoints)
	}
}

func TestCreateSlices_LabelsAndMetadata(t *testing.T) {
	r := newTestReconciler()
	dg := &meridio2v1alpha1.DistributionGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "test-dg", Namespace: "default"},
	}

	scrapedPods := []podWithAddresses{
		newTestPod("pod-1", "pod-1", "10.0.0.1"),
	}

	testGatewayContext := testGWCtx()
	testGWName := testGatewayContext.gateway.Name
	testGWNamespace := testGatewayContext.gateway.Namespace
	slices := r.createSlices(dg, testGatewayContext, scrapedPods, nil, nil)

	if len(slices) != 1 {
		t.Fatalf("Expected 1 slice, got %d", len(slices))
	}

	slice := slices[0]
	if slice.Labels[labelManagedBy] != managedByValue {
		t.Errorf("Expected managed-by label %q, got %q", managedByValue, slice.Labels[labelManagedBy])
	}
	if slice.Spec.DistributionGroupName != "test-dg" {
		t.Errorf("Expected distributionGroupName 'test-dg', got %q", slice.Spec.DistributionGroupName)
	}
	if slice.Spec.GatewayRef.Name != testGWName || slice.Spec.GatewayRef.Namespace != testGWNamespace {
		t.Errorf("Expected gatewayRef %s/%s, got %s/%s", testGWName, testGWNamespace,
			slice.Spec.GatewayRef.Name, slice.Spec.GatewayRef.Namespace)
	}
	if slice.Namespace != "default" {
		t.Errorf("Expected namespace 'default', got %q", slice.Namespace)
	}
}

func TestCreateSlices_StructurePreservation(t *testing.T) {
	r := newTestReconciler()
	dg := &meridio2v1alpha1.DistributionGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "test-dg", Namespace: "default"},
	}

	// Existing slice with 2 endpoints
	existingSlice := meridio2v1alpha1.LoadBalancerEndpointSlice{
		ObjectMeta: metav1.ObjectMeta{Name: "existing-slice"},
		Spec: meridio2v1alpha1.LoadBalancerEndpointSliceSpec{
			Endpoints: []meridio2v1alpha1.LoadBalancerEndpoint{
				{Target: meridio2v1alpha1.EndpointTarget{UID: "pod-1"}},
				{Target: meridio2v1alpha1.EndpointTarget{UID: "pod-2"}},
			},
		},
	}

	// Existing slice with 2 endpoints
	scrapedPods := []podWithAddresses{
		newTestPod("pod-1", "pod-1", "10.0.0.1"),
		newTestPod("pod-2", "pod-2", "10.0.0.2"),
		newTestPod("pod-3", "pod-3", "10.0.0.3"),
	}

	slices := r.createSlices(dg, testGWCtx(), scrapedPods, nil, []meridio2v1alpha1.LoadBalancerEndpointSlice{existingSlice})

	if len(slices) != 1 {
		t.Errorf("Expected 1 slice (reused), got %d", len(slices))
	}
	if slices[0].Name != "existing-slice" {
		t.Errorf("Expected to reuse 'existing-slice', got %q", slices[0].Name)
	}
	if len(slices[0].Spec.Endpoints) != 3 {
		t.Errorf("Expected 3 endpoints in reused slice, got %d", len(slices[0].Spec.Endpoints))
	}
}

func TestCreateSlices_Compaction(t *testing.T) {
	r := newTestReconciler()
	dg := &meridio2v1alpha1.DistributionGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "test-dg", Namespace: "default"},
	}

	// Two existing slices: slice-0 had [pod-1, pod-2, pod-3], slice-1 had [pod-4, pod-5]
	existingSlices := []meridio2v1alpha1.LoadBalancerEndpointSlice{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "slice-0"},
			Spec: meridio2v1alpha1.LoadBalancerEndpointSliceSpec{
				Endpoints: []meridio2v1alpha1.LoadBalancerEndpoint{
					{Target: meridio2v1alpha1.EndpointTarget{UID: "pod-1"}},
					{Target: meridio2v1alpha1.EndpointTarget{UID: "pod-2"}},
					{Target: meridio2v1alpha1.EndpointTarget{UID: "pod-3"}},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "slice-1"},
			Spec: meridio2v1alpha1.LoadBalancerEndpointSliceSpec{
				Endpoints: []meridio2v1alpha1.LoadBalancerEndpoint{
					{Target: meridio2v1alpha1.EndpointTarget{UID: "pod-4"}},
					{Target: meridio2v1alpha1.EndpointTarget{UID: "pod-5"}},
				},
			},
		},
	}

	// After scale-in: only pod-1 and pod-4 remain
	scrapedPods := []podWithAddresses{
		newTestPod("pod-1", "pod-1", "10.0.0.1"),
		newTestPod("pod-4", "pod-4", "10.0.0.4"),
	}

	slices := r.createSlices(dg, testGWCtx(), scrapedPods, nil, existingSlices)

	// Compaction should move pod-4 from slice-1 into slice-0 (which has capacity)
	// and eliminate slice-1
	if len(slices) != 1 {
		t.Errorf("Expected 1 slice after compaction, got %d", len(slices))
	}
	if slices[0].Name != "slice-0" {
		t.Errorf("Expected surviving slice to be 'slice-0', got %q", slices[0].Name)
	}
	if len(slices[0].Spec.Endpoints) != 2 {
		t.Errorf("Expected 2 endpoints in compacted slice, got %d", len(slices[0].Spec.Endpoints))
	}
}

func TestCreateSlices_CompactionPreservesFullSlices(t *testing.T) {
	r := &DistributionGroupReconciler{MaxEndpointsPerSlice: 10}
	dg := &meridio2v1alpha1.DistributionGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "test-dg", Namespace: "default"},
	}

	// Realistic scenario: 23 pods previously filled 3 slices (10+10+3).
	// After scale-in: all 10 pods in slice-0 survive, 1 in slice-1 survives, 1 in slice-2 survives.
	// Expected: slice-0 untouched (full), slice-2's survivor compacts into slice-1, slice-2 eliminated.
	existingSlice0Endpoints := make([]meridio2v1alpha1.LoadBalancerEndpoint, 10)
	existingSlice1Endpoints := make([]meridio2v1alpha1.LoadBalancerEndpoint, 10)
	existingSlice2Endpoints := make([]meridio2v1alpha1.LoadBalancerEndpoint, 3)

	// slice-0: pods 0-9 (all survive)
	scrapedPods := make([]podWithAddresses, 0, 12)
	for i := range 10 {
		uid := "s0-pod-" + strconv.Itoa(i)
		existingSlice0Endpoints[i] = meridio2v1alpha1.LoadBalancerEndpoint{
			Target: meridio2v1alpha1.EndpointTarget{UID: uid, Name: uid},
		}
		scrapedPods = append(scrapedPods, newTestPod(uid, uid, "10.0.0."+strconv.Itoa(i)))
	}
	// slice-1: pods 10-19 (only pod-10 survives)
	for i := range 10 {
		uid := "s1-pod-" + strconv.Itoa(10+i)
		existingSlice1Endpoints[i] = meridio2v1alpha1.LoadBalancerEndpoint{
			Target: meridio2v1alpha1.EndpointTarget{UID: uid, Name: uid},
		}
	}
	scrapedPods = append(scrapedPods, newTestPod("s1-pod-10", "s1-pod-10", "10.0.0.10"))
	// slice-2: pods 20-22 (only pod-20 survives)
	for i := range 3 {
		uid := "s2-pod-" + strconv.Itoa(20+i)
		existingSlice2Endpoints[i] = meridio2v1alpha1.LoadBalancerEndpoint{
			Target: meridio2v1alpha1.EndpointTarget{UID: uid, Name: uid},
		}
	}
	scrapedPods = append(scrapedPods, newTestPod("s2-pod-20", "s2-pod-20", "10.0.0.20"))

	existingSlices := []meridio2v1alpha1.LoadBalancerEndpointSlice{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "slice-0"},
			Spec:       meridio2v1alpha1.LoadBalancerEndpointSliceSpec{Endpoints: existingSlice0Endpoints},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "slice-1"},
			Spec:       meridio2v1alpha1.LoadBalancerEndpointSliceSpec{Endpoints: existingSlice1Endpoints},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "slice-2"},
			Spec:       meridio2v1alpha1.LoadBalancerEndpointSliceSpec{Endpoints: existingSlice2Endpoints},
		},
	}

	slices := r.createSlices(dg, testGWCtx(), scrapedPods, nil, existingSlices)
	t.Logf("slices: %v", slices)

	// slice-2's survivor should compact into slice-1, eliminating slice-2
	if len(slices) != 2 {
		t.Fatalf("Expected 2 slices after compaction, got %d", len(slices))
	}
	if slices[0].Name != "slice-0" {
		t.Errorf("Expected first slice to be 'slice-0', got %q", slices[0].Name)
	}
	if slices[1].Name != "slice-1" {
		t.Errorf("Expected second slice to be 'slice-1', got %q", slices[1].Name)
	}

	// slice-0: full, untouched — same UIDs in original order
	if len(slices[0].Spec.Endpoints) != 10 {
		t.Fatalf("Expected slice-0 to remain full (10), got %d", len(slices[0].Spec.Endpoints))
	}
	for i, ep := range slices[0].Spec.Endpoints {
		expectedUID := "s0-pod-" + strconv.Itoa(i)
		if ep.Target.UID != expectedUID {
			t.Errorf("slice-0[%d]: expected UID %q, got %q", i, expectedUID, ep.Target.UID)
		}
	}

	// slice-1: should have 2 endpoints (its own survivor + slice-2's survivor)
	if len(slices[1].Spec.Endpoints) != 2 {
		t.Errorf("Expected slice-1 to have 2 endpoints after compaction, got %d", len(slices[1].Spec.Endpoints))
	}
	for i, ep := range slices[1].Spec.Endpoints {
		switch ep.Target.UID {
		case "s1-pod-10", "s2-pod-20":
		default:
			{
				t.Errorf("slice-1[%d]: unexpected UID %q", i, ep.Target.UID)
			}
		}
	}
}

func TestCreateSlices_CompactionPreservesMaglevIDs(t *testing.T) {
	r := newTestReconciler()
	dg := &meridio2v1alpha1.DistributionGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "test-dg", Namespace: "default"},
		Spec: meridio2v1alpha1.DistributionGroupSpec{
			Type:   meridio2v1alpha1.DistributionGroupTypeMaglev,
			Maglev: &meridio2v1alpha1.MaglevConfig{MaxEndpoints: 32},
		},
	}

	gwCtx := testGWCtx()

	// Existing slices with Maglev IDs
	existingSlices := []meridio2v1alpha1.LoadBalancerEndpointSlice{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "slice-0"},
			Spec: meridio2v1alpha1.LoadBalancerEndpointSliceSpec{
				Endpoints: []meridio2v1alpha1.LoadBalancerEndpoint{
					{Target: meridio2v1alpha1.EndpointTarget{UID: "pod-0", Name: "pod-0"}, Identifier: int32Ptr(0)},
					{Target: meridio2v1alpha1.EndpointTarget{UID: "pod-1", Name: "pod-1"}, Identifier: int32Ptr(1)},
					{Target: meridio2v1alpha1.EndpointTarget{UID: "pod-2", Name: "pod-2"}, Identifier: int32Ptr(2)},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "slice-1"},
			Spec: meridio2v1alpha1.LoadBalancerEndpointSliceSpec{
				Endpoints: []meridio2v1alpha1.LoadBalancerEndpoint{
					{Target: meridio2v1alpha1.EndpointTarget{UID: "pod-3", Name: "pod-3"}, Identifier: int32Ptr(3)},
				},
			},
		},
	}

	// Only pod-0 and pod-3 remain
	scrapedPods := []podWithAddresses{
		newTestPod("pod-0", "pod-0", "10.0.0.1"),
		newTestPod("pod-3", "pod-3", "10.0.0.4"),
	}

	slices, _ := r.calculateMaglevSlices(dg, gwCtx, scrapedPods, existingSlices)

	if len(slices) != 1 {
		t.Fatalf("Expected compaction to 1 slice, got %d", len(slices))
	}
	if len(slices[0].Spec.Endpoints) != 2 {
		t.Fatalf("Expected 2 endpoints, got %d", len(slices[0].Spec.Endpoints))
	}

	ids := getIDMap(slices)
	if ids["pod-0"] != 0 {
		t.Errorf("pod-0 ID should be 0, got %d", ids["pod-0"])
	}
	if ids["pod-3"] != 3 {
		t.Errorf("pod-3 ID should be 3, got %d", ids["pod-3"])
	}
}
