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
	"testing"

	meridio2v1alpha1 "github.com/nordix/meridio-2/api/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func TestExtractDGsFromBackendRefs_DistributionGroup(t *testing.T) {
	dgGroup := meridio2v1alpha1.GroupVersion.Group
	dgKind := kindDistributionGroup
	ns := "test-ns"

	backendRefs := []gatewayv1.BackendRef{
		{
			BackendObjectReference: gatewayv1.BackendObjectReference{
				Group:     (*gatewayv1.Group)(&dgGroup),
				Kind:      (*gatewayv1.Kind)(&dgKind),
				Name:      "dg-1",
				Namespace: (*gatewayv1.Namespace)(&ns),
			},
		},
	}

	result := extractDGsFromBackendRefs(backendRefs, "default")

	expected := client.ObjectKey{Namespace: "test-ns", Name: "dg-1"}
	if !result[expected] {
		t.Errorf("Expected to extract %v, got %v", expected, result)
	}
	if len(result) != 1 {
		t.Errorf("Expected 1 DG, got %d", len(result))
	}
}

func TestExtractDGsFromBackendRefs_DefaultNamespace(t *testing.T) {
	dgGroup := meridio2v1alpha1.GroupVersion.Group
	dgKind := kindDistributionGroup

	backendRefs := []gatewayv1.BackendRef{
		{
			BackendObjectReference: gatewayv1.BackendObjectReference{
				Group: (*gatewayv1.Group)(&dgGroup),
				Kind:  (*gatewayv1.Kind)(&dgKind),
				Name:  "dg-1",
			},
		},
	}

	result := extractDGsFromBackendRefs(backendRefs, "local-ns")

	expected := client.ObjectKey{Namespace: "local-ns", Name: "dg-1"}
	if !result[expected] {
		t.Errorf("Expected to use local namespace, got %v", result)
	}
}

func TestExtractDGsFromBackendRefs_SkipService(t *testing.T) {
	backendRefs := []gatewayv1.BackendRef{
		{
			BackendObjectReference: gatewayv1.BackendObjectReference{
				Name: "service-1",
			},
		},
	}

	result := extractDGsFromBackendRefs(backendRefs, "default")

	if len(result) != 0 {
		t.Errorf("Expected to skip Service backend, got %d DGs", len(result))
	}
}

func TestExtractDGsFromBackendRefs_Multiple(t *testing.T) {
	dgGroup := meridio2v1alpha1.GroupVersion.Group
	dgKind := kindDistributionGroup

	backendRefs := []gatewayv1.BackendRef{
		{
			BackendObjectReference: gatewayv1.BackendObjectReference{
				Group: (*gatewayv1.Group)(&dgGroup),
				Kind:  (*gatewayv1.Kind)(&dgKind),
				Name:  "dg-1",
			},
		},
		{
			BackendObjectReference: gatewayv1.BackendObjectReference{
				Group: (*gatewayv1.Group)(&dgGroup),
				Kind:  (*gatewayv1.Kind)(&dgKind),
				Name:  "dg-2",
			},
		},
	}

	result := extractDGsFromBackendRefs(backendRefs, "default")

	if len(result) != 2 {
		t.Errorf("Expected 2 DGs, got %d", len(result))
	}
}

func TestBackendRefMatchesDG_Match(t *testing.T) {
	dgGroup := meridio2v1alpha1.GroupVersion.Group
	dgKind := kindDistributionGroup

	backendRef := gatewayv1.BackendRef{
		BackendObjectReference: gatewayv1.BackendObjectReference{
			Group: (*gatewayv1.Group)(&dgGroup),
			Kind:  (*gatewayv1.Kind)(&dgKind),
			Name:  "dg-1",
		},
	}

	dgKey := client.ObjectKey{Namespace: "default", Name: "dg-1"}

	if !backendRefMatchesDG(backendRef, "default", dgKey) {
		t.Error("Expected match")
	}
}

func TestBackendRefMatchesDG_DifferentName(t *testing.T) {
	dgGroup := meridio2v1alpha1.GroupVersion.Group
	dgKind := kindDistributionGroup

	backendRef := gatewayv1.BackendRef{
		BackendObjectReference: gatewayv1.BackendObjectReference{
			Group: (*gatewayv1.Group)(&dgGroup),
			Kind:  (*gatewayv1.Kind)(&dgKind),
			Name:  "dg-1",
		},
	}

	dgKey := client.ObjectKey{Namespace: "default", Name: "dg-2"}

	if backendRefMatchesDG(backendRef, "default", dgKey) {
		t.Error("Expected no match (different name)")
	}
}

func TestBackendRefMatchesDG_DifferentNamespace(t *testing.T) {
	dgGroup := meridio2v1alpha1.GroupVersion.Group
	dgKind := kindDistributionGroup
	ns := "other-ns"

	backendRef := gatewayv1.BackendRef{
		BackendObjectReference: gatewayv1.BackendObjectReference{
			Group:     (*gatewayv1.Group)(&dgGroup),
			Kind:      (*gatewayv1.Kind)(&dgKind),
			Name:      "dg-1",
			Namespace: (*gatewayv1.Namespace)(&ns),
		},
	}

	dgKey := client.ObjectKey{Namespace: "default", Name: "dg-1"}

	if backendRefMatchesDG(backendRef, "default", dgKey) {
		t.Error("Expected no match (different namespace)")
	}
}

func TestBackendRefMatchesDG_WrongKind(t *testing.T) {
	dgGroup := meridio2v1alpha1.GroupVersion.Group
	wrongKind := "Service"

	backendRef := gatewayv1.BackendRef{
		BackendObjectReference: gatewayv1.BackendObjectReference{
			Group: (*gatewayv1.Group)(&dgGroup),
			Kind:  (*gatewayv1.Kind)(&wrongKind),
			Name:  "dg-1",
		},
	}

	dgKey := client.ObjectKey{Namespace: "default", Name: "dg-1"}

	if backendRefMatchesDG(backendRef, "default", dgKey) {
		t.Error("Expected no match (wrong kind)")
	}
}

func TestBackendRefMatchesDG_WrongGroup(t *testing.T) {
	wrongGroup := "core"
	dgKind := kindDistributionGroup

	backendRef := gatewayv1.BackendRef{
		BackendObjectReference: gatewayv1.BackendObjectReference{
			Group: (*gatewayv1.Group)(&wrongGroup),
			Kind:  (*gatewayv1.Kind)(&dgKind),
			Name:  "dg-1",
		},
	}

	dgKey := client.ObjectKey{Namespace: "default", Name: "dg-1"}

	if backendRefMatchesDG(backendRef, "default", dgKey) {
		t.Error("Expected no match (wrong group)")
	}
}

func TestBackendRefMatchesDG_DefaultGroupKind(t *testing.T) {
	// BackendRef with no Group/Kind defaults to Service
	backendRef := gatewayv1.BackendRef{
		BackendObjectReference: gatewayv1.BackendObjectReference{
			Name: "dg-1",
		},
	}

	dgKey := client.ObjectKey{Namespace: "default", Name: "dg-1"}

	if backendRefMatchesDG(backendRef, "default", dgKey) {
		t.Error("Expected no match (defaults to Service, not DistributionGroup)")
	}
}

func TestFindDGsReferencingGateways_DirectAndIndirect(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, meridio2v1alpha1.AddToScheme(scheme))
	require.NoError(t, gatewayv1.Install(scheme))

	gwGroup := gatewayv1.GroupName
	gwKind := kindGateway
	dgGroup := meridio2v1alpha1.GroupVersion.Group
	dgKind := kindDistributionGroup

	// DG with direct parentRef to gw-1
	dgDirect := &meridio2v1alpha1.DistributionGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "dg-direct", Namespace: "default"},
		Spec: meridio2v1alpha1.DistributionGroupSpec{
			ParentRefs: []meridio2v1alpha1.ParentReference{
				{Group: &gwGroup, Kind: &gwKind, Name: "gw-1"},
			},
		},
	}

	// DG with no parentRef (indirect only via L34Route)
	dgIndirect := &meridio2v1alpha1.DistributionGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "dg-indirect", Namespace: "default"},
		Spec:       meridio2v1alpha1.DistributionGroupSpec{},
	}

	// DG referencing a different Gateway (should NOT be returned)
	dgOther := &meridio2v1alpha1.DistributionGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "dg-other", Namespace: "default"},
		Spec: meridio2v1alpha1.DistributionGroupSpec{
			ParentRefs: []meridio2v1alpha1.ParentReference{
				{Group: &gwGroup, Kind: &gwKind, Name: "gw-other"},
			},
		},
	}

	// L34Route linking gw-1 → dg-indirect
	route := &meridio2v1alpha1.L34Route{
		ObjectMeta: metav1.ObjectMeta{Name: "route-1", Namespace: "default"},
		Spec: meridio2v1alpha1.L34RouteSpec{
			ParentRefs: []gatewayv1.ParentReference{
				{Name: "gw-1"},
			},
			BackendRefs: []gatewayv1.BackendRef{
				{BackendObjectReference: gatewayv1.BackendObjectReference{
					Group: (*gatewayv1.Group)(&dgGroup),
					Kind:  (*gatewayv1.Kind)(&dgKind),
					Name:  "dg-indirect",
				}},
			},
			DestinationCIDRs: []string{"20.0.0.1/32"},
			Protocols:        []meridio2v1alpha1.TransportProtocol{meridio2v1alpha1.TCP},
			Priority:         1,
		},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(dgDirect, dgIndirect, dgOther, route).Build()

	r := &DistributionGroupReconciler{
		Client:    c,
		Namespace: "default",
	}

	requests := r.findDGsReferencingGateways(context.Background(), []client.ObjectKey{
		{Namespace: "default", Name: "gw-1"},
	})

	// Should find both dg-direct (parentRef) and dg-indirect (via L34Route)
	names := make(map[string]bool)
	for _, req := range requests {
		names[req.Name] = true
	}

	assert.True(t, names["dg-direct"], "dg-direct should be found (direct parentRef)")
	assert.True(t, names["dg-indirect"], "dg-indirect should be found (indirect via L34Route)")
	assert.False(t, names["dg-other"], "dg-other should NOT be found (references different Gateway)")
	assert.Len(t, requests, 2)
}

func TestFindDGsReferencingGateways_EmptyInput(t *testing.T) {
	r := &DistributionGroupReconciler{}
	requests := r.findDGsReferencingGateways(context.Background(), nil)
	assert.Nil(t, requests)
}
