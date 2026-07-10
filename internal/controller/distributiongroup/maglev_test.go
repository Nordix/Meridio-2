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
	"strconv"
	"testing"

	meridio2v1alpha1 "github.com/nordix/meridio-2/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func int32Ptr(v int32) *int32 { return &v }

func TestAssignMaglevIDs_NewPods(t *testing.T) {
	pods := []corev1.Pod{
		{ObjectMeta: metav1.ObjectMeta{UID: "pod-1"}},
		{ObjectMeta: metav1.ObjectMeta{UID: "pod-2"}},
		{ObjectMeta: metav1.ObjectMeta{UID: "pod-3"}},
	}

	result := assignMaglevIDs(pods, nil, 32)

	if len(result) != 3 {
		t.Errorf("Expected 3 assignments, got %d", len(result))
	}
	if result["pod-1"] != 0 || result["pod-2"] != 1 || result["pod-3"] != 2 {
		t.Errorf("Expected sequential IDs 0,1,2, got %v", result)
	}
}

func TestAssignMaglevIDs_PreserveExisting(t *testing.T) {
	existing := map[string]int32{
		"pod-1": 5,
		"pod-2": 10,
	}

	pods := []corev1.Pod{
		{ObjectMeta: metav1.ObjectMeta{UID: "pod-1"}},
		{ObjectMeta: metav1.ObjectMeta{UID: "pod-2"}},
		{ObjectMeta: metav1.ObjectMeta{UID: "pod-3"}},
	}

	result := assignMaglevIDs(pods, existing, 32)

	if result["pod-1"] != 5 || result["pod-2"] != 10 {
		t.Errorf("Existing assignments not preserved: %v", result)
	}
	if result["pod-3"] != 0 {
		t.Errorf("New pod should get first available ID (0), got %d", result["pod-3"])
	}
}

func TestAssignMaglevIDs_CapacityLimit(t *testing.T) {
	pods := []corev1.Pod{
		{ObjectMeta: metav1.ObjectMeta{UID: "pod-1"}},
		{ObjectMeta: metav1.ObjectMeta{UID: "pod-2"}},
		{ObjectMeta: metav1.ObjectMeta{UID: "pod-3"}},
	}

	result := assignMaglevIDs(pods, nil, 2)

	if len(result) != 2 {
		t.Errorf("Expected 2 assignments (capacity limit), got %d", len(result))
	}
}

func TestExtractMaglevAssignments(t *testing.T) {
	slices := []meridio2v1alpha1.LoadBalancerEndpointSlice{
		{
			Spec: meridio2v1alpha1.LoadBalancerEndpointSliceSpec{
				Endpoints: []meridio2v1alpha1.LoadBalancerEndpoint{
					{
						Target:     meridio2v1alpha1.EndpointTarget{Name: "pod-1", UID: "uid-1"},
						Identifier: int32Ptr(5),
					},
					{
						Target:     meridio2v1alpha1.EndpointTarget{Name: "pod-2", UID: "uid-2"},
						Identifier: int32Ptr(10),
					},
				},
			},
		},
	}

	result := extractMaglevAssignments(slices)

	if len(result) != 2 {
		t.Errorf("Expected 2 assignments, got %d", len(result))
	}
	if result["uid-1"] != 5 || result["uid-2"] != 10 {
		t.Errorf("Incorrect assignments: %v", result)
	}
}

func TestExtractMaglevAssignments_SkipNilIdentifier(t *testing.T) {
	slices := []meridio2v1alpha1.LoadBalancerEndpointSlice{
		{
			Spec: meridio2v1alpha1.LoadBalancerEndpointSliceSpec{
				Endpoints: []meridio2v1alpha1.LoadBalancerEndpoint{
					{
						Target:     meridio2v1alpha1.EndpointTarget{Name: "pod-1", UID: "uid-1"},
						Identifier: nil, // no ID assigned
					},
					{
						Target:     meridio2v1alpha1.EndpointTarget{Name: "pod-2", UID: "uid-2"},
						Identifier: int32Ptr(15),
					},
				},
			},
		},
	}

	result := extractMaglevAssignments(slices)

	if len(result) != 1 {
		t.Errorf("Expected 1 valid assignment, got %d: %v", len(result), result)
	}
	if result["uid-2"] != 15 {
		t.Errorf("Expected uid-2=15, got %v", result)
	}
}

func TestAssignMaglevIDs_CapacityEnforcement(t *testing.T) {
	// Simulate 32 Pods with IDs, 1 new Pod
	existing := make(map[string]int32)
	for i := range int32(32) {
		existing["pod-"+strconv.Itoa(int(i))] = i
	}

	// 33 total Pods (32 existing + 1 new)
	pods := make([]corev1.Pod, 33)
	for i := range 32 {
		pods[i] = corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{UID: types.UID("pod-" + strconv.Itoa(i))},
		}
	}
	pods[32] = corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{UID: "pod-new"},
	}

	result := assignMaglevIDs(pods, existing, 32)

	// Should only have 32 assignments (capacity limit)
	if len(result) != 32 {
		t.Errorf("Expected 32 assignments (capacity limit), got %d", len(result))
	}

	// New Pod should NOT get an ID
	if _, exists := result["pod-new"]; exists {
		t.Error("New Pod should not get ID when capacity is full")
	}
}
