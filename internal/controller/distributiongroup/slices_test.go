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
	"context"
	"strconv"
	"testing"

	meridio2v1alpha1 "github.com/nordix/meridio-2/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestCalculateMaglevSlices_SharedIDsAcrossNetworks(t *testing.T) {
	r := &DistributionGroupReconciler{}
	dg := &meridio2v1alpha1.DistributionGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "test-dg", Namespace: "default"},
		Spec: meridio2v1alpha1.DistributionGroupSpec{
			Type:   meridio2v1alpha1.DistributionGroupTypeMaglev,
			Maglev: &meridio2v1alpha1.MaglevConfig{MaxEndpoints: 32},
		},
	}

	// 32 Pods appear in both IPv4 and IPv6 networks (dual-stack)
	ipv4Pods := make([]podWithNetworkIP, 32)
	ipv6Pods := make([]podWithNetworkIP, 32)
	for i := range 32 {
		uid := types.UID("uid-" + strconv.Itoa(i))
		name := "pod-" + strconv.Itoa(i)
		pod := corev1.Pod{ObjectMeta: metav1.ObjectMeta{UID: uid, Name: name, Namespace: "default"}}
		ipv4Pods[i] = podWithNetworkIP{pod: pod, ip: "192.168.100." + strconv.Itoa(10+i)}
		ipv6Pods[i] = podWithNetworkIP{pod: pod, ip: "2001:db8:100::" + strconv.Itoa(10+i)}
	}

	podsByNetwork := map[string][]podWithNetworkIP{
		"192.168.100.0/24":  ipv4Pods,
		"2001:db8:100::/64": ipv6Pods,
	}

	slices, capacityInfo := r.calculateMaglevSlices(dg, podsByNetwork, nil)

	// Should produce 2 slices (one per network)
	if len(slices) != 2 {
		t.Fatalf("Expected 2 slices (one per network), got %d", len(slices))
	}

	// Extract Pod UID → Maglev zone from each network's slice
	idsByNetwork := make(map[string]map[string]string) // decoded CIDR → uid → zone
	for _, slice := range slices {
		network := decodeCIDRFromLabel(slice.Labels[labelNetworkSubnet])
		if idsByNetwork[network] == nil {
			idsByNetwork[network] = make(map[string]string)
		}
		for _, ep := range slice.Endpoints {
			if ep.TargetRef != nil && ep.Zone != nil {
				idsByNetwork[network][string(ep.TargetRef.UID)] = *ep.Zone
			}
		}
	}

	ipv4IDs := idsByNetwork["192.168.100.0/24"]
	ipv6IDs := idsByNetwork["2001:db8:100::/64"]

	if len(ipv4IDs) != 32 {
		t.Fatalf("Expected 32 endpoints in IPv4 slice, got %d", len(ipv4IDs))
	}
	if len(ipv6IDs) != 32 {
		t.Fatalf("Expected 32 endpoints in IPv6 slice, got %d", len(ipv6IDs))
	}

	// Core assertion: every Pod must have the same Maglev ID in both networks
	for uid, v4Zone := range ipv4IDs {
		v6Zone, exists := ipv6IDs[uid]
		if !exists {
			t.Errorf("Pod %s exists in IPv4 slice but not IPv6", uid)
			continue
		}
		if v4Zone != v6Zone {
			t.Errorf("Pod %s has different Maglev IDs across networks: IPv4=%s, IPv6=%s", uid, v4Zone, v6Zone)
		}
	}

	// No capacity issues expected (32 pods, 32 max)
	if capacityInfo != nil && len(capacityInfo.networkIssues) > 0 {
		t.Errorf("Unexpected capacity issues: %v", capacityInfo.networkIssues)
	}
}

func TestCalculateMaglevSlices_StableAcrossReconciles(t *testing.T) {
	r := &DistributionGroupReconciler{}
	dg := &meridio2v1alpha1.DistributionGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "test-dg", Namespace: "default"},
		Spec: meridio2v1alpha1.DistributionGroupSpec{
			Type:   meridio2v1alpha1.DistributionGroupTypeMaglev,
			Maglev: &meridio2v1alpha1.MaglevConfig{MaxEndpoints: 32},
		},
	}

	// 32 dual-stack Pods
	ipv4Pods := make([]podWithNetworkIP, 32)
	ipv6Pods := make([]podWithNetworkIP, 32)
	for i := range 32 {
		uid := types.UID("uid-" + strconv.Itoa(i))
		name := "pod-" + strconv.Itoa(i)
		pod := corev1.Pod{ObjectMeta: metav1.ObjectMeta{UID: uid, Name: name, Namespace: "default"}}
		ipv4Pods[i] = podWithNetworkIP{pod: pod, ip: "192.168.100." + strconv.Itoa(10+i)}
		ipv6Pods[i] = podWithNetworkIP{pod: pod, ip: "2001:db8:100::" + strconv.Itoa(10+i)}
	}

	podsByNetwork := map[string][]podWithNetworkIP{
		"192.168.100.0/24":  ipv4Pods,
		"2001:db8:100::/64": ipv6Pods,
	}

	// First reconcile: no existing slices
	slices1, _ := r.calculateMaglevSlices(dg, podsByNetwork, nil)

	// Build slicesByNetwork from first reconcile output (simulates what listOwnedSlices returns)
	slicesByNetwork := make(map[string][]discoveryv1.EndpointSlice)
	for _, s := range slices1 {
		network := decodeCIDRFromLabel(s.Labels[labelNetworkSubnet])
		slicesByNetwork[network] = append(slicesByNetwork[network], s)
	}

	// Second reconcile: pass first reconcile's output as existing slices
	slices2, _ := r.calculateMaglevSlices(dg, podsByNetwork, slicesByNetwork)

	// Extract all UID→zone mappings from each reconcile, detecting cross-network inconsistencies
	getIDs := func(slices []discoveryv1.EndpointSlice) map[string]string {
		ids := make(map[string]string)
		for _, s := range slices {
			for _, ep := range s.Endpoints {
				if ep.TargetRef != nil && ep.Zone != nil {
					uid := string(ep.TargetRef.UID)
					if existing, seen := ids[uid]; seen && existing != *ep.Zone {
						t.Errorf("Pod %s has inconsistent IDs across network slices: %s vs %s", uid, existing, *ep.Zone)
					}
					ids[uid] = *ep.Zone
				}
			}
		}
		return ids
	}

	ids1 := getIDs(slices1)
	ids2 := getIDs(slices2)

	if len(ids1) != 32 { // 32 unique pods (each appears in 2 slices with same ID)
		t.Fatalf("First reconcile: expected 32 unique pod entries, got %d", len(ids1))
	}
	if len(ids2) != 32 {
		t.Fatalf("Second reconcile: expected 32 unique pod entries, got %d", len(ids2))
	}

	// Every Pod must have the same ID in both reconciles
	for uid, zone1 := range ids1 {
		zone2, exists := ids2[uid]
		if !exists {
			t.Errorf("Pod %s missing in second reconcile", uid)
			continue
		}
		if zone1 != zone2 {
			t.Errorf("Pod %s ID changed between reconciles: %s → %s", uid, zone1, zone2)
		}
	}
}

func TestCalculateMaglevSlices_AsymmetricNetworkPresence(t *testing.T) {
	r := &DistributionGroupReconciler{}
	dg := &meridio2v1alpha1.DistributionGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "test-dg", Namespace: "default"},
		Spec: meridio2v1alpha1.DistributionGroupSpec{
			Type:   meridio2v1alpha1.DistributionGroupTypeMaglev,
			Maglev: &meridio2v1alpha1.MaglevConfig{MaxEndpoints: 32},
		},
	}

	// Pod-1 and Pod-2 are dual-stack, Pod-3 is IPv4 only, Pod-4 is IPv6 only
	podsByNetwork := map[string][]podWithNetworkIP{
		"192.168.100.0/24": {
			{pod: corev1.Pod{ObjectMeta: metav1.ObjectMeta{UID: "uid-1", Name: "pod-1"}}, ip: "192.168.100.10"},
			{pod: corev1.Pod{ObjectMeta: metav1.ObjectMeta{UID: "uid-2", Name: "pod-2"}}, ip: "192.168.100.11"},
			{pod: corev1.Pod{ObjectMeta: metav1.ObjectMeta{UID: "uid-3", Name: "pod-3"}}, ip: "192.168.100.12"},
		},
		"2001:db8:100::/64": {
			{pod: corev1.Pod{ObjectMeta: metav1.ObjectMeta{UID: "uid-1", Name: "pod-1"}}, ip: "2001:db8:100::10"},
			{pod: corev1.Pod{ObjectMeta: metav1.ObjectMeta{UID: "uid-2", Name: "pod-2"}}, ip: "2001:db8:100::11"},
			{pod: corev1.Pod{ObjectMeta: metav1.ObjectMeta{UID: "uid-4", Name: "pod-4"}}, ip: "2001:db8:100::14"},
		},
	}

	slices, _ := r.calculateMaglevSlices(dg, podsByNetwork, nil)

	// Should produce 2 slices (one per network)
	if len(slices) != 2 {
		t.Fatalf("Expected 2 slices, got %d", len(slices))
	}

	// Extract per-network endpoints
	endpointsByNetwork := make(map[string]map[string]string) // CIDR → uid → zone
	for _, slice := range slices {
		network := decodeCIDRFromLabel(slice.Labels[labelNetworkSubnet])
		if endpointsByNetwork[network] == nil {
			endpointsByNetwork[network] = make(map[string]string)
		}
		for _, ep := range slice.Endpoints {
			if ep.TargetRef != nil && ep.Zone != nil {
				endpointsByNetwork[network][string(ep.TargetRef.UID)] = *ep.Zone
			}
		}
	}

	ipv4, ok := endpointsByNetwork["192.168.100.0/24"]
	if !ok {
		t.Fatal("Expected IPv4 slice for 192.168.100.0/24")
	}
	ipv6, ok := endpointsByNetwork["2001:db8:100::/64"]
	if !ok {
		t.Fatal("Expected IPv6 slice for 2001:db8:100::/64")
	}

	// Pod-3 (IPv4 only): present in IPv4, absent from IPv6
	if _, exists := ipv4["uid-3"]; !exists {
		t.Error("Pod-3 should be in IPv4 slice")
	}
	if _, exists := ipv6["uid-3"]; exists {
		t.Error("Pod-3 should NOT be in IPv6 slice")
	}

	// Pod-4 (IPv6 only): absent from IPv4, present in IPv6
	if _, exists := ipv4["uid-4"]; exists {
		t.Error("Pod-4 should NOT be in IPv4 slice")
	}
	if _, exists := ipv6["uid-4"]; !exists {
		t.Error("Pod-4 should be in IPv6 slice")
	}

	// Dual-stack Pods: same ID across both slices
	for _, uid := range []string{"uid-1", "uid-2"} {
		if ipv4[uid] != ipv6[uid] {
			t.Errorf("Dual-stack pod %s has different IDs: IPv4=%s, IPv6=%s", uid, ipv4[uid], ipv6[uid])
		}
	}

	// All 4 Pods get unique IDs (no collision)
	allIDs := make(map[string]string) // zone → uid (first seen)
	for _, endpoints := range endpointsByNetwork {
		for uid, zone := range endpoints {
			if prev, exists := allIDs[zone]; exists && prev != uid {
				t.Errorf("ID collision: %s used by both %s and %s", zone, prev, uid)
			}
			allIDs[zone] = uid
		}
	}
	if len(allIDs) != 4 {
		t.Errorf("Expected 4 unique IDs, got %d", len(allIDs))
	}
}

func TestCalculateMaglevSlices_PartialPodReplacement(t *testing.T) {
	r := &DistributionGroupReconciler{}
	dg := &meridio2v1alpha1.DistributionGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "test-dg", Namespace: "default"},
		Spec: meridio2v1alpha1.DistributionGroupSpec{
			Type:   meridio2v1alpha1.DistributionGroupTypeMaglev,
			Maglev: &meridio2v1alpha1.MaglevConfig{MaxEndpoints: 32},
		},
	}

	makePods := func(prefix string, count, ipOffset int) ([]podWithNetworkIP, []podWithNetworkIP) {
		v4 := make([]podWithNetworkIP, count)
		v6 := make([]podWithNetworkIP, count)
		for i := range count {
			uid := types.UID(prefix + "-" + strconv.Itoa(i))
			name := prefix + "-" + strconv.Itoa(i)
			pod := corev1.Pod{ObjectMeta: metav1.ObjectMeta{UID: uid, Name: name, Namespace: "default"}}
			v4[i] = podWithNetworkIP{pod: pod, ip: "192.168.100." + strconv.Itoa(ipOffset+i)}
			v6[i] = podWithNetworkIP{pod: pod, ip: "2001:db8:100::" + strconv.Itoa(ipOffset+i)}
		}
		return v4, v6
	}

	// Initial: 32 dual-stack Pods
	origV4, origV6 := makePods("orig", 32, 10)
	podsByNetwork := map[string][]podWithNetworkIP{
		"192.168.100.0/24":  origV4,
		"2001:db8:100::/64": origV6,
	}

	// First reconcile
	slices1, _ := r.calculateMaglevSlices(dg, podsByNetwork, nil)

	// Build existing slices for second reconcile
	slicesByNetwork := make(map[string][]discoveryv1.EndpointSlice)
	for _, s := range slices1 {
		network := decodeCIDRFromLabel(s.Labels[labelNetworkSubnet])
		slicesByNetwork[network] = append(slicesByNetwork[network], s)
	}

	// Replace half the Pods (indices 16-31) with new ones
	newV4, newV6 := makePods("new", 16, 50)
	podsByNetwork2 := map[string][]podWithNetworkIP{
		"192.168.100.0/24":  append(origV4[:16], newV4...),
		"2001:db8:100::/64": append(origV6[:16], newV6...),
	}

	// Second reconcile with replaced Pods
	slices2, _ := r.calculateMaglevSlices(dg, podsByNetwork2, slicesByNetwork)

	// Extract IDs, detecting cross-network inconsistencies
	getIDs := func(slices []discoveryv1.EndpointSlice) map[string]string {
		ids := make(map[string]string)
		for _, s := range slices {
			for _, ep := range s.Endpoints {
				if ep.TargetRef != nil && ep.Zone != nil {
					uid := string(ep.TargetRef.UID)
					if existing, seen := ids[uid]; seen && existing != *ep.Zone {
						t.Errorf("Pod %s has inconsistent IDs across networks: %s vs %s", uid, existing, *ep.Zone)
					}
					ids[uid] = *ep.Zone
				}
			}
		}
		return ids
	}

	ids1 := getIDs(slices1)
	ids2 := getIDs(slices2)

	if len(ids1) != 32 {
		t.Fatalf("First reconcile: expected 32 unique pod entries, got %d", len(ids1))
	}
	if len(ids2) != 32 {
		t.Fatalf("Second reconcile: expected 32 unique pod entries, got %d", len(ids2))
	}

	// Surviving Pods (orig-0 through orig-15) must keep their original IDs
	for i := range 16 {
		uid := "orig-" + strconv.Itoa(i)
		zone1, ok1 := ids1[uid]
		zone2, ok2 := ids2[uid]
		if !ok1 || !ok2 {
			t.Errorf("Surviving pod %s missing from reconcile results", uid)
			continue
		}
		if zone1 != zone2 {
			t.Errorf("Surviving pod %s ID changed: %s → %s", uid, zone1, zone2)
		}
	}

	// New Pods (new-0 through new-15) must be present in the allocation
	for i := range 16 {
		uid := "new-" + strconv.Itoa(i)
		if _, exists := ids2[uid]; !exists {
			t.Errorf("New pod %s should have an ID assigned", uid)
		}
	}
}

func TestCreateSlicesForNetwork_MaglevCapacityEnforcement(t *testing.T) {
	dg := &meridio2v1alpha1.DistributionGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "test-dg", Namespace: "default"},
		Spec: meridio2v1alpha1.DistributionGroupSpec{
			Type: meridio2v1alpha1.DistributionGroupTypeMaglev,
			Maglev: &meridio2v1alpha1.MaglevConfig{
				MaxEndpoints: 2,
			},
		},
	}

	// 3 Pods with IPs, but only 2 get Maglev IDs (capacity limit)
	podsWithIP := []podWithNetworkIP{
		{pod: corev1.Pod{ObjectMeta: metav1.ObjectMeta{UID: "pod-1", Name: "pod-1", Namespace: "default"}}, ip: "10.0.0.1"},
		{pod: corev1.Pod{ObjectMeta: metav1.ObjectMeta{UID: "pod-2", Name: "pod-2", Namespace: "default"}}, ip: "10.0.0.2"},
		{pod: corev1.Pod{ObjectMeta: metav1.ObjectMeta{UID: "pod-3", Name: "pod-3", Namespace: "default"}}, ip: "10.0.0.3"},
	}

	// Only 2 Pods get IDs (capacity exceeded for pod-3)
	podToID := map[string]int32{
		"pod-1": 0,
		"pod-2": 1,
		// pod-3 intentionally missing (capacity exceeded)
	}

	slices := createSlicesForNetwork(dg, podsWithIP, podToID, "192.168.1.0/24", nil)

	// Verify only 2 endpoints in result (pod-3 excluded)
	totalEndpoints := 0
	for _, slice := range slices {
		totalEndpoints += len(slice.Endpoints)
	}
	if totalEndpoints != 2 {
		t.Errorf("Expected 2 endpoints (capacity limit), got %d", totalEndpoints)
	}

	// Verify all endpoints have Maglev zones
	for _, slice := range slices {
		for _, ep := range slice.Endpoints {
			if ep.Zone == nil {
				t.Errorf("Endpoint %s has no zone (should be excluded)", ep.TargetRef.UID)
			}
			expectedZone1 := maglevIDPrefix + "0"
			expectedZone2 := maglevIDPrefix + "1"
			if ep.Zone != nil && (*ep.Zone != expectedZone1 && *ep.Zone != expectedZone2) {
				t.Errorf("Endpoint has invalid zone: %s", *ep.Zone)
			}
		}
	}

	// Verify pod-3 is NOT in any slice
	for _, slice := range slices {
		for _, ep := range slice.Endpoints {
			if ep.TargetRef.UID == types.UID("pod-3") {
				t.Error("pod-3 should be excluded (capacity exceeded)")
			}
		}
	}
}

func TestCalculateMaglevSlices_CapacityExceededDualStack(t *testing.T) {
	r := &DistributionGroupReconciler{}
	dg := &meridio2v1alpha1.DistributionGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "test-dg", Namespace: "default"},
		Spec: meridio2v1alpha1.DistributionGroupSpec{
			Type:   meridio2v1alpha1.DistributionGroupTypeMaglev,
			Maglev: &meridio2v1alpha1.MaglevConfig{MaxEndpoints: 4},
		},
	}

	// 6 dual-stack Pods, capacity 4 — 2 should be excluded from BOTH slices
	ipv4Pods := make([]podWithNetworkIP, 6)
	ipv6Pods := make([]podWithNetworkIP, 6)
	for i := range 6 {
		uid := types.UID("uid-" + strconv.Itoa(i))
		name := "pod-" + strconv.Itoa(i)
		pod := corev1.Pod{ObjectMeta: metav1.ObjectMeta{UID: uid, Name: name, Namespace: "default"}}
		ipv4Pods[i] = podWithNetworkIP{pod: pod, ip: "192.168.100." + strconv.Itoa(10+i)}
		ipv6Pods[i] = podWithNetworkIP{pod: pod, ip: "2001:db8:100::" + strconv.Itoa(10+i)}
	}

	podsByNetwork := map[string][]podWithNetworkIP{
		"192.168.100.0/24":  ipv4Pods,
		"2001:db8:100::/64": ipv6Pods,
	}

	slices, capacityInfo := r.calculateMaglevSlices(dg, podsByNetwork, nil)

	if capacityInfo == nil || len(capacityInfo.networkIssues) == 0 {
		t.Fatal("Expected capacity exceeded info")
	}

	// Extract UIDs per network
	uidsByNetwork := make(map[string]map[string]string)
	for _, slice := range slices {
		network := decodeCIDRFromLabel(slice.Labels[labelNetworkSubnet])
		if uidsByNetwork[network] == nil {
			uidsByNetwork[network] = make(map[string]string)
		}
		for _, ep := range slice.Endpoints {
			if ep.TargetRef != nil && ep.Zone != nil {
				uidsByNetwork[network][string(ep.TargetRef.UID)] = *ep.Zone
			}
		}
	}

	ipv4, ok := uidsByNetwork["192.168.100.0/24"]
	if !ok {
		t.Fatal("Expected IPv4 slice")
	}
	ipv6, ok := uidsByNetwork["2001:db8:100::/64"]
	if !ok {
		t.Fatal("Expected IPv6 slice")
	}

	// Exactly 4 Pods included in each slice
	if len(ipv4) != 4 {
		t.Errorf("Expected 4 endpoints in IPv4 slice, got %d", len(ipv4))
	}
	if len(ipv6) != 4 {
		t.Errorf("Expected 4 endpoints in IPv6 slice, got %d", len(ipv6))
	}

	// Same Pods included in both slices (same set excluded from both)
	for uid := range ipv4 {
		if _, exists := ipv6[uid]; !exists {
			t.Errorf("Pod %s included in IPv4 but excluded from IPv6", uid)
		}
	}

	// Included Pods have consistent IDs across slices
	for uid, v4Zone := range ipv4 {
		if v6Zone := ipv6[uid]; v4Zone != v6Zone {
			t.Errorf("Pod %s has different IDs: IPv4=%s, IPv6=%s", uid, v4Zone, v6Zone)
		}
	}

	// All assigned IDs are unique
	usedIDs := make(map[string]bool)
	for _, zone := range ipv4 {
		if usedIDs[zone] {
			t.Errorf("Duplicate ID: %s", zone)
		}
		usedIDs[zone] = true
	}
}

func TestCreateSlicesForNetwork_NonMaglevIncludesAll(t *testing.T) {
	dg := &meridio2v1alpha1.DistributionGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "test-dg", Namespace: "default"},
		Spec: meridio2v1alpha1.DistributionGroupSpec{
			Type: meridio2v1alpha1.DistributionGroupTypeMaglev, // Type doesn't matter here
		},
	}

	podsWithIP := []podWithNetworkIP{
		{pod: corev1.Pod{ObjectMeta: metav1.ObjectMeta{UID: "pod-1", Name: "pod-1", Namespace: "default"}}, ip: "10.0.0.1"},
		{pod: corev1.Pod{ObjectMeta: metav1.ObjectMeta{UID: "pod-2", Name: "pod-2", Namespace: "default"}}, ip: "10.0.0.2"},
		{pod: corev1.Pod{ObjectMeta: metav1.ObjectMeta{UID: "pod-3", Name: "pod-3", Namespace: "default"}}, ip: "10.0.0.3"},
	}

	// Non-Maglev: podToID is nil
	slices := createSlicesForNetwork(dg, podsWithIP, nil, "192.168.1.0/24", nil)

	// Verify all 3 Pods are included (no capacity limit)
	totalEndpoints := 0
	for _, slice := range slices {
		totalEndpoints += len(slice.Endpoints)
	}
	if totalEndpoints != 3 {
		t.Errorf("Expected 3 endpoints (no capacity limit), got %d", totalEndpoints)
	}
}

func TestEndpointSliceNeedsUpdate(t *testing.T) {
	tests := []struct {
		name     string
		existing *discoveryv1.EndpointSlice
		desired  *discoveryv1.EndpointSlice
		expected bool
	}{
		{
			name: "no change",
			existing: &discoveryv1.EndpointSlice{
				AddressType: discoveryv1.AddressTypeIPv4,
				Endpoints:   []discoveryv1.Endpoint{{Addresses: []string{"10.0.0.1"}}},
				ObjectMeta:  metav1.ObjectMeta{Labels: map[string]string{"key": "value"}},
			},
			desired: &discoveryv1.EndpointSlice{
				AddressType: discoveryv1.AddressTypeIPv4,
				Endpoints:   []discoveryv1.Endpoint{{Addresses: []string{"10.0.0.1"}}},
				ObjectMeta:  metav1.ObjectMeta{Labels: map[string]string{"key": "value"}},
			},
			expected: false,
		},
		{
			name: "address type changed",
			existing: &discoveryv1.EndpointSlice{
				AddressType: discoveryv1.AddressTypeIPv4,
			},
			desired: &discoveryv1.EndpointSlice{
				AddressType: discoveryv1.AddressTypeIPv6,
			},
			expected: true,
		},
		{
			name: "endpoints changed",
			existing: &discoveryv1.EndpointSlice{
				Endpoints: []discoveryv1.Endpoint{{Addresses: []string{"10.0.0.1"}}},
			},
			desired: &discoveryv1.EndpointSlice{
				Endpoints: []discoveryv1.Endpoint{{Addresses: []string{"10.0.0.2"}}},
			},
			expected: true,
		},
		{
			name: "labels changed",
			existing: &discoveryv1.EndpointSlice{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"key": "old"}},
			},
			desired: &discoveryv1.EndpointSlice{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"key": "new"}},
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := endpointSliceNeedsUpdate(tt.existing, tt.desired)
			if result != tt.expected {
				t.Errorf("endpointSliceNeedsUpdate() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestGroupSlicesByNetwork_NormalizeCIDR(t *testing.T) {
	ctx := context.Background()

	slices := []discoveryv1.EndpointSlice{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "slice-1",
				Labels: map[string]string{labelNetworkSubnet: "192.168.1.0-24"},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "slice-2",
				Labels: map[string]string{labelNetworkSubnet: "192.168.1.5-24"}, // Non-canonical
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "slice-3",
				Labels: map[string]string{labelNetworkSubnet: "10.0.0.0-8"},
			},
		},
	}

	grouped := groupSlicesByNetwork(ctx, slices)

	// Both slice-1 and slice-2 should be grouped under canonical "192.168.1.0/24"
	if len(grouped["192.168.1.0/24"]) != 2 {
		t.Errorf("Expected 2 slices for 192.168.1.0/24, got %d", len(grouped["192.168.1.0/24"]))
	}

	if len(grouped["10.0.0.0/8"]) != 1 {
		t.Errorf("Expected 1 slice for 10.0.0.0/8, got %d", len(grouped["10.0.0.0/8"]))
	}
}

func TestGroupSlicesByNetwork_SkipInvalidLabel(t *testing.T) {
	ctx := context.Background()

	slices := []discoveryv1.EndpointSlice{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "valid",
				Labels: map[string]string{labelNetworkSubnet: "192.168.1.0-24"},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "invalid",
				Labels: map[string]string{labelNetworkSubnet: "not-a-cidr"},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "no-label",
			},
		},
	}

	grouped := groupSlicesByNetwork(ctx, slices)

	// Only valid slice should be grouped
	if len(grouped) != 1 {
		t.Errorf("Expected 1 network group, got %d", len(grouped))
	}
	if len(grouped["192.168.1.0/24"]) != 1 {
		t.Errorf("Expected 1 slice for 192.168.1.0/24, got %d", len(grouped["192.168.1.0/24"]))
	}
}

func TestCreateSlicesForNetwork_SliceSplitting(t *testing.T) {
	dg := &meridio2v1alpha1.DistributionGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "test-dg", Namespace: "default"},
	}

	// Create 150 Pods (should split into 2 slices: 100 + 50)
	podsWithIP := make([]podWithNetworkIP, 150)
	for i := range 150 {
		podsWithIP[i] = podWithNetworkIP{
			pod: corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					UID:       types.UID("pod-" + string(rune(i))),
					Name:      "pod-" + string(rune(i)),
					Namespace: "default",
				},
			},
			ip: "10.0.0." + string(rune(i)),
		}
	}

	slices := createSlicesForNetwork(dg, podsWithIP, nil, "192.168.1.0/24", nil)

	// Should create 2 slices
	if len(slices) != 2 {
		t.Errorf("Expected 2 slices (100+50), got %d", len(slices))
	}

	// Verify total endpoints
	totalEndpoints := 0
	for _, slice := range slices {
		totalEndpoints += len(slice.Endpoints)
		if len(slice.Endpoints) > 100 {
			t.Errorf("Slice has %d endpoints, max should be 100", len(slice.Endpoints))
		}
	}
	if totalEndpoints != 150 {
		t.Errorf("Expected 150 total endpoints, got %d", totalEndpoints)
	}
}

func TestCreateSlicesForNetwork_IPv6AddressType(t *testing.T) {
	dg := &meridio2v1alpha1.DistributionGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "test-dg", Namespace: "default"},
	}

	podsWithIP := []podWithNetworkIP{
		{pod: corev1.Pod{ObjectMeta: metav1.ObjectMeta{UID: "pod-1", Name: "pod-1"}}, ip: "2001:db8::1"},
	}

	slices := createSlicesForNetwork(dg, podsWithIP, nil, "2001:db8::/32", nil)

	if len(slices) != 1 {
		t.Fatalf("Expected 1 slice, got %d", len(slices))
	}
	if slices[0].AddressType != discoveryv1.AddressTypeIPv6 {
		t.Errorf("Expected IPv6 address type, got %v", slices[0].AddressType)
	}
}

func TestCreateSlicesForNetwork_LabelsAndMetadata(t *testing.T) {
	dg := &meridio2v1alpha1.DistributionGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "test-dg", Namespace: "default"},
	}

	podsWithIP := []podWithNetworkIP{
		{pod: corev1.Pod{ObjectMeta: metav1.ObjectMeta{UID: "pod-1", Name: "pod-1"}}, ip: "10.0.0.1"},
	}

	slices := createSlicesForNetwork(dg, podsWithIP, nil, "192.168.1.0/24", nil)

	if len(slices) != 1 {
		t.Fatalf("Expected 1 slice, got %d", len(slices))
	}

	slice := slices[0]
	if slice.Labels[labelManagedBy] != managedByValue {
		t.Errorf("Expected managed-by label %q, got %q", managedByValue, slice.Labels[labelManagedBy])
	}
	if slice.Labels[labelDistributionGroup] != "test-dg" {
		t.Errorf("Expected distribution-group label 'test-dg', got %q", slice.Labels[labelDistributionGroup])
	}
	if slice.Labels[labelNetworkSubnet] != "192.168.1.0-24" {
		t.Errorf("Expected network-subnet label '192.168.1.0-24', got %q", slice.Labels[labelNetworkSubnet])
	}
	if slice.Namespace != "default" {
		t.Errorf("Expected namespace 'default', got %q", slice.Namespace)
	}
}

func TestCreateSlicesForNetwork_StructurePreservation(t *testing.T) {
	dg := &meridio2v1alpha1.DistributionGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "test-dg", Namespace: "default"},
	}

	// Existing slice with 2 endpoints
	existingSlice := discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{Name: "existing-slice"},
		Endpoints: []discoveryv1.Endpoint{
			{TargetRef: &corev1.ObjectReference{UID: "pod-1"}},
			{TargetRef: &corev1.ObjectReference{UID: "pod-2"}},
		},
	}

	// 3 Pods: 2 existing + 1 new
	podsWithIP := []podWithNetworkIP{
		{pod: corev1.Pod{ObjectMeta: metav1.ObjectMeta{UID: "pod-1", Name: "pod-1"}}, ip: "10.0.0.1"},
		{pod: corev1.Pod{ObjectMeta: metav1.ObjectMeta{UID: "pod-2", Name: "pod-2"}}, ip: "10.0.0.2"},
		{pod: corev1.Pod{ObjectMeta: metav1.ObjectMeta{UID: "pod-3", Name: "pod-3"}}, ip: "10.0.0.3"},
	}

	slices := createSlicesForNetwork(dg, podsWithIP, nil, "192.168.1.0/24", []discoveryv1.EndpointSlice{existingSlice})

	// Should reuse existing slice
	if len(slices) != 1 {
		t.Errorf("Expected 1 slice (reused), got %d", len(slices))
	}
	if slices[0].Name != "existing-slice" {
		t.Errorf("Expected to reuse 'existing-slice', got %q", slices[0].Name)
	}
	if len(slices[0].Endpoints) != 3 {
		t.Errorf("Expected 3 endpoints in reused slice, got %d", len(slices[0].Endpoints))
	}
}

func TestCreateSlicesForNetwork_Compaction(t *testing.T) {
	dg := &meridio2v1alpha1.DistributionGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "test-dg", Namespace: "default"},
	}

	// Two existing slices: slice-0 had [pod-1, pod-2, pod-3], slice-1 had [pod-4, pod-5]
	existingSlices := []discoveryv1.EndpointSlice{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "slice-0"},
			Endpoints: []discoveryv1.Endpoint{
				{TargetRef: &corev1.ObjectReference{UID: "pod-1"}},
				{TargetRef: &corev1.ObjectReference{UID: "pod-2"}},
				{TargetRef: &corev1.ObjectReference{UID: "pod-3"}},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "slice-1"},
			Endpoints: []discoveryv1.Endpoint{
				{TargetRef: &corev1.ObjectReference{UID: "pod-4"}},
				{TargetRef: &corev1.ObjectReference{UID: "pod-5"}},
			},
		},
	}

	// After scale-in: only pod-1 and pod-4 remain
	podsWithIP := []podWithNetworkIP{
		{pod: corev1.Pod{ObjectMeta: metav1.ObjectMeta{UID: "pod-1", Name: "pod-1"}}, ip: "10.0.0.1"},
		{pod: corev1.Pod{ObjectMeta: metav1.ObjectMeta{UID: "pod-4", Name: "pod-4"}}, ip: "10.0.0.4"},
	}

	slices := createSlicesForNetwork(dg, podsWithIP, nil, "192.168.1.0/24", existingSlices)

	// Compaction should move pod-4 from slice-1 into slice-0 (which has capacity)
	// and eliminate slice-1
	if len(slices) != 1 {
		t.Errorf("Expected 1 slice after compaction, got %d", len(slices))
	}
	if slices[0].Name != "slice-0" {
		t.Errorf("Expected surviving slice to be 'slice-0', got %q", slices[0].Name)
	}
	if len(slices[0].Endpoints) != 2 {
		t.Errorf("Expected 2 endpoints in compacted slice, got %d", len(slices[0].Endpoints))
	}
}

func TestCreateSlicesForNetwork_CompactionPreservesFullSlices(t *testing.T) {
	dg := &meridio2v1alpha1.DistributionGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "test-dg", Namespace: "default"},
	}

	// slice-0 is full (maxEndpointsPerSlice=100), slice-1 has 2 endpoints
	existingSlice0Endpoints := make([]discoveryv1.Endpoint, maxEndpointsPerSlice)
	podsWithIP := make([]podWithNetworkIP, maxEndpointsPerSlice+2)
	for i := range maxEndpointsPerSlice {
		uid := types.UID("pod-" + strconv.Itoa(i))
		existingSlice0Endpoints[i] = discoveryv1.Endpoint{
			TargetRef: &corev1.ObjectReference{UID: uid},
		}
		podsWithIP[i] = podWithNetworkIP{
			pod: corev1.Pod{ObjectMeta: metav1.ObjectMeta{UID: uid, Name: "pod-" + strconv.Itoa(i)}},
			ip:  "10.0.0." + strconv.Itoa(i),
		}
	}
	// 2 extra pods in slice-1
	for i := range 2 {
		uid := types.UID("extra-" + strconv.Itoa(i))
		podsWithIP[maxEndpointsPerSlice+i] = podWithNetworkIP{
			pod: corev1.Pod{ObjectMeta: metav1.ObjectMeta{UID: uid, Name: "extra-" + strconv.Itoa(i)}},
			ip:  "10.0.1." + strconv.Itoa(i),
		}
	}

	existingSlices := []discoveryv1.EndpointSlice{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "slice-0"},
			Endpoints:  existingSlice0Endpoints,
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "slice-1"},
			Endpoints: []discoveryv1.Endpoint{
				{TargetRef: &corev1.ObjectReference{UID: "extra-0"}},
				{TargetRef: &corev1.ObjectReference{UID: "extra-1"}},
			},
		},
	}

	slices := createSlicesForNetwork(dg, podsWithIP, nil, "192.168.1.0/24", existingSlices)

	// slice-0 is full, slice-1 cannot be compacted into it — both should remain
	if len(slices) != 2 {
		t.Errorf("Expected 2 slices (no compaction possible), got %d", len(slices))
	}
	if len(slices[0].Endpoints) != maxEndpointsPerSlice {
		t.Errorf("Expected slice-0 to remain full (%d), got %d", maxEndpointsPerSlice, len(slices[0].Endpoints))
	}
	if len(slices[1].Endpoints) != 2 {
		t.Errorf("Expected slice-1 to keep 2 endpoints, got %d", len(slices[1].Endpoints))
	}
}

func TestCreateSlicesForNetwork_CompactionPreservesMaglevIDs(t *testing.T) {
	r := &DistributionGroupReconciler{}
	dg := &meridio2v1alpha1.DistributionGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "test-dg", Namespace: "default"},
		Spec: meridio2v1alpha1.DistributionGroupSpec{
			Type:   meridio2v1alpha1.DistributionGroupTypeMaglev,
			Maglev: &meridio2v1alpha1.MaglevConfig{MaxEndpoints: 32},
		},
	}

	zone0 := maglevIDPrefix + "0"
	zone1 := maglevIDPrefix + "1"
	zone2 := maglevIDPrefix + "2"
	zone3 := maglevIDPrefix + "3"

	// Two existing slices: slice-0 had [pod-0, pod-1, pod-2], slice-1 had [pod-3]
	// After scale-in: pod-1 and pod-2 removed → slice-1's pod-3 should compact into slice-0
	existingSlices := []discoveryv1.EndpointSlice{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "slice-0"},
			Endpoints: []discoveryv1.Endpoint{
				{TargetRef: &corev1.ObjectReference{Kind: kindPod, UID: "pod-0"}, Zone: &zone0},
				{TargetRef: &corev1.ObjectReference{Kind: kindPod, UID: "pod-1"}, Zone: &zone1},
				{TargetRef: &corev1.ObjectReference{Kind: kindPod, UID: "pod-2"}, Zone: &zone2},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "slice-1"},
			Endpoints: []discoveryv1.Endpoint{
				{TargetRef: &corev1.ObjectReference{Kind: kindPod, UID: "pod-3"}, Zone: &zone3},
			},
		},
	}

	// Only pod-0 and pod-3 remain
	remainingPods := []podWithNetworkIP{
		{pod: corev1.Pod{ObjectMeta: metav1.ObjectMeta{UID: "pod-0", Name: "pod-0"}}, ip: "10.0.0.1"},
		{pod: corev1.Pod{ObjectMeta: metav1.ObjectMeta{UID: "pod-3", Name: "pod-3"}}, ip: "10.0.0.4"},
	}

	podsByNetwork := map[string][]podWithNetworkIP{"192.168.1.0/24": remainingPods}
	slicesByNetwork := map[string][]discoveryv1.EndpointSlice{"192.168.1.0/24": existingSlices}

	// Reconcile: compaction should merge into 1 slice
	slices, _ := r.calculateMaglevSlices(dg, podsByNetwork, slicesByNetwork)

	// Verify compaction occurred: 2 slices → 1 slice
	if len(slices) != 1 {
		t.Fatalf("Expected compaction to 1 slice, got %d", len(slices))
	}
	if len(slices[0].Endpoints) != 2 {
		t.Fatalf("Expected 2 endpoints after compaction, got %d", len(slices[0].Endpoints))
	}

	// Verify IDs are preserved for surviving Pods
	for _, ep := range slices[0].Endpoints {
		if ep.TargetRef == nil || ep.Zone == nil {
			t.Error("Endpoint missing targetRef or zone after compaction")
			continue
		}
		switch string(ep.TargetRef.UID) {
		case "pod-0":
			if *ep.Zone != zone0 {
				t.Errorf("pod-0 ID changed after compaction: expected %s, got %s", zone0, *ep.Zone)
			}
		case "pod-3":
			if *ep.Zone != zone3 {
				t.Errorf("pod-3 ID changed after compaction: expected %s, got %s", zone3, *ep.Zone)
			}
		default:
			t.Errorf("Unexpected pod in compacted slice: %s", ep.TargetRef.UID)
		}
	}
}
