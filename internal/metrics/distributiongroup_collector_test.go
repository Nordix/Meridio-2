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
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	meridio2v1alpha1 "github.com/nordix/meridio-2/api/v1alpha1"
	"github.com/nordix/meridio-2/internal/controller/distributiongroup"
)

func dcScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	_ = meridio2v1alpha1.AddToScheme(scheme)
	_ = gatewayv1.Install(scheme)
	return scheme
}

// dcNewFakeClient builds a fake client seeded with objs, with the same
// spec.distributionGroupName field index the real manager registers (see
// DistributionGroupReconciler's SetupWithManager), which countOwnedEndpointsByGateway's
// client.MatchingFields List depends on.
func dcNewFakeClient(objs ...client.Object) client.Client {
	return fake.NewClientBuilder().
		WithScheme(dcScheme()).
		WithObjects(objs...).
		WithIndex(&meridio2v1alpha1.LoadBalancerEndpointSlice{},
			"spec.distributionGroupName",
			func(obj client.Object) []string {
				slice := obj.(*meridio2v1alpha1.LoadBalancerEndpointSlice)
				return []string{slice.Spec.DistributionGroupName}
			},
		).
		Build()
}

// dcReadyCondition builds a Ready condition using the same Type/Reason literals the
// distributiongroup package itself writes (ConditionTypeReady, ReasonEndpointsAvailable).
func dcReadyCondition(status metav1.ConditionStatus) metav1.Condition {
	return metav1.Condition{
		Type:               distributiongroup.ConditionTypeReady,
		Status:             status,
		Reason:             distributiongroup.ReasonEndpointsAvailable,
		Message:            "ready",
		LastTransitionTime: metav1.Now(),
	}
}

// dcNewOwnedSlice builds a LoadBalancerEndpointSlice owned by dg (real ownerReference, via
// controllerutil.SetControllerReference — mirrors production, not a hand-rolled OwnerReferences
// literal) with endpointCount endpoints, scoped to the given Gateway.
func dcNewOwnedSlice(
	t *testing.T, dg *meridio2v1alpha1.DistributionGroup, name, gwName, gwNamespace string, endpointCount int,
) *meridio2v1alpha1.LoadBalancerEndpointSlice {
	t.Helper()
	endpoints := make([]meridio2v1alpha1.LoadBalancerEndpoint, endpointCount)
	for i := range endpoints {
		podName := fmt.Sprintf("%s-pod-%d", name, i)
		endpoints[i] = meridio2v1alpha1.LoadBalancerEndpoint{
			Target:    meridio2v1alpha1.EndpointTarget{Name: podName, UID: podName},
			Addresses: []meridio2v1alpha1.EndpointAddress{{IP: fmt.Sprintf("10.0.0.%d", i+1), Family: meridio2v1alpha1.IPv4}},
			Ready:     true,
		}
	}
	slice := &meridio2v1alpha1.LoadBalancerEndpointSlice{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: dg.Namespace},
		Spec: meridio2v1alpha1.LoadBalancerEndpointSliceSpec{
			DistributionGroupName: dg.Name,
			GatewayRef:            meridio2v1alpha1.SliceGatewayRef{Name: gwName, Namespace: gwNamespace},
			Endpoints:             endpoints,
		},
	}
	require.NoError(t, controllerutil.SetControllerReference(dg, slice, dcScheme()))
	return slice
}

// --- resolveMaxEndpoints branches ---

func TestResolveMaxEndpoints_MaglevWithConfig(t *testing.T) {
	dg := &meridio2v1alpha1.DistributionGroup{
		Spec: meridio2v1alpha1.DistributionGroupSpec{
			Type:   meridio2v1alpha1.DistributionGroupTypeMaglev,
			Maglev: &meridio2v1alpha1.MaglevConfig{MaxEndpoints: 64},
		},
	}
	require.Equal(t, float64(64), resolveMaxEndpoints(dg))
}

func TestResolveMaxEndpoints_MaglevWithoutConfig_UsesDefault(t *testing.T) {
	dg := &meridio2v1alpha1.DistributionGroup{
		Spec: meridio2v1alpha1.DistributionGroupSpec{
			Type: meridio2v1alpha1.DistributionGroupTypeMaglev,
		},
	}
	require.Equal(t, float64(meridio2v1alpha1.DefaultMaglevMaxEndpoints), resolveMaxEndpoints(dg))
}

func TestResolveMaxEndpoints_UnknownType_ReturnsPositiveInf(t *testing.T) {
	dg := &meridio2v1alpha1.DistributionGroup{
		Spec: meridio2v1alpha1.DistributionGroupSpec{
			Type: "SomeFutureStrategy",
		},
	}
	got := resolveMaxEndpoints(dg)
	require.True(t, math.IsInf(got, 1), "expected +Inf for an unrecognized DistributionGroupType, got %v", got)
}

// --- Collect: ready condition ---

func TestDistributionGroupCollector_Ready(t *testing.T) {
	dg := &meridio2v1alpha1.DistributionGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "dg-a", Namespace: "ns-a"},
		Spec:       meridio2v1alpha1.DistributionGroupSpec{Type: meridio2v1alpha1.DistributionGroupTypeMaglev},
		Status:     meridio2v1alpha1.DistributionGroupStatus{Conditions: []metav1.Condition{dcReadyCondition(metav1.ConditionTrue)}},
	}

	fakeClient := dcNewFakeClient(dg)
	collector := NewDistributionGroupCollector(fakeClient, alwaysSyncedWaiter{}, time.Second, "", testControllerName, "meridio_2")

	expected := `
# HELP meridio_2_distributiongroup_ready Whether the DistributionGroup's Ready status condition is currently True, as 0 or 1. DG-wide (no Gateway dimension).
# TYPE meridio_2_distributiongroup_ready gauge
meridio_2_distributiongroup_ready{dg="dg-a",namespace="ns-a"} 1
`
	require.NoError(t, testutil.CollectAndCompare(collector, strings.NewReader(expected), "meridio_2_distributiongroup_ready"))
}

func TestDistributionGroupCollector_NotReady(t *testing.T) {
	dg := &meridio2v1alpha1.DistributionGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "dg-a", Namespace: "ns-a"},
		Spec:       meridio2v1alpha1.DistributionGroupSpec{Type: meridio2v1alpha1.DistributionGroupTypeMaglev},
		// No Ready condition at all: IsReady must report false, not panic/default-true.
	}

	fakeClient := dcNewFakeClient(dg)
	collector := NewDistributionGroupCollector(fakeClient, alwaysSyncedWaiter{}, time.Second, "", testControllerName, "meridio_2")

	expected := `
# HELP meridio_2_distributiongroup_ready Whether the DistributionGroup's Ready status condition is currently True, as 0 or 1. DG-wide (no Gateway dimension).
# TYPE meridio_2_distributiongroup_ready gauge
meridio_2_distributiongroup_ready{dg="dg-a",namespace="ns-a"} 0
`
	require.NoError(t, testutil.CollectAndCompare(collector, strings.NewReader(expected), "meridio_2_distributiongroup_ready"))
}

// --- Collect: Gateway-union resolution + per-Gateway endpoint counting ---

// TestDistributionGroupCollector_EndpointsPerGateway_OwnedSlicesOnly verifies per-Gateway
// endpoint counting (countOwnedEndpointsByGateway) and, critically, non-owned-slice filtering:
// a LoadBalancerEndpointSlice with a matching spec.distributionGroupName but no ownerReference to
// this DG must not contribute to the count (metav1.IsControlledBy guard in the collector,
// mirroring DistributionGroupReconciler.listOwnedSlices).
func TestDistributionGroupCollector_EndpointsPerGateway_OwnedSlicesOnly(t *testing.T) {
	dg := &meridio2v1alpha1.DistributionGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "dg-a", Namespace: "ns-a"},
		Spec: meridio2v1alpha1.DistributionGroupSpec{
			Type:   meridio2v1alpha1.DistributionGroupTypeMaglev,
			Maglev: &meridio2v1alpha1.MaglevConfig{MaxEndpoints: 32},
		},
	}
	owned := dcNewOwnedSlice(t, dg, "slice-owned", "gw-a", "ns-a", 3)

	// Same distributionGroupName, but NOT owned by dg (no ownerReference) — a manually-created
	// or stale slice that happens to name-match. Must be excluded from the count entirely.
	notOwned := &meridio2v1alpha1.LoadBalancerEndpointSlice{
		ObjectMeta: metav1.ObjectMeta{Name: "slice-not-owned", Namespace: "ns-a"},
		Spec: meridio2v1alpha1.LoadBalancerEndpointSliceSpec{
			DistributionGroupName: dg.Name,
			GatewayRef:            meridio2v1alpha1.SliceGatewayRef{Name: "gw-a", Namespace: "ns-a"},
			Endpoints: []meridio2v1alpha1.LoadBalancerEndpoint{
				{Target: meridio2v1alpha1.EndpointTarget{Name: "pod", UID: "uid"},
					Addresses: []meridio2v1alpha1.EndpointAddress{{IP: "10.0.0.9", Family: meridio2v1alpha1.IPv4}}},
			},
		},
	}

	fakeClient := dcNewFakeClient(dg, owned, notOwned)
	collector := NewDistributionGroupCollector(fakeClient, alwaysSyncedWaiter{}, time.Second, "", testControllerName, "meridio_2")

	expected := `
# HELP meridio_2_distributiongroup_endpoints Current endpoint count for this DistributionGroup under the given Gateway.
# TYPE meridio_2_distributiongroup_endpoints gauge
meridio_2_distributiongroup_endpoints{dg="dg-a",gateway="gw-a",gateway_namespace="ns-a",namespace="ns-a"} 3
`
	require.NoError(t, testutil.CollectAndCompare(collector, strings.NewReader(expected), "meridio_2_distributiongroup_endpoints"))
}

// TestDistributionGroupCollector_GatewayUnion_ReferencedAndOwnedBothCounted verifies the union
// described on DistributionGroupCollector: a Gateway that is referenced-and-accepted but has no
// owned slices yet still gets a series (endpoints=0), and a Gateway with owned slices but no
// current accepted reference still gets its series (reported as-is), and a Gateway that is both
// referenced and has owned slices contributes exactly one series (dedup via the union, not two).
func TestDistributionGroupCollector_GatewayUnion_ReferencedAndOwnedBothCounted(t *testing.T) {
	dg := &meridio2v1alpha1.DistributionGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "dg-a", Namespace: "ns-a"},
		Spec: meridio2v1alpha1.DistributionGroupSpec{
			Type: meridio2v1alpha1.DistributionGroupTypeMaglev,
			ParentRefs: []meridio2v1alpha1.ParentReference{
				{Name: "gw-referenced-only"}, // direct parentRef, same namespace as dg (ns-a)
			},
		},
	}

	// Referenced-and-accepted, no owned slices yet: must still appear, with endpoints=0.
	gwReferencedOnly := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "gw-referenced-only", Namespace: "ns-a"},
		Status:     gatewayv1.GatewayStatus{Conditions: []metav1.Condition{acceptedCondition(testControllerName)}},
	}

	// Has an owned slice but is not (or no longer) referenced/accepted: still reported as-is.
	sliceOwnedOnly := dcNewOwnedSlice(t, dg, "slice-owned-only", "gw-owned-only", "ns-a", 2)

	fakeClient := dcNewFakeClient(dg, gwReferencedOnly, sliceOwnedOnly)
	collector := NewDistributionGroupCollector(fakeClient, alwaysSyncedWaiter{}, time.Second, "", testControllerName, "meridio_2")

	expected := `
# HELP meridio_2_distributiongroup_endpoints Current endpoint count for this DistributionGroup under the given Gateway.
# TYPE meridio_2_distributiongroup_endpoints gauge
meridio_2_distributiongroup_endpoints{dg="dg-a",gateway="gw-owned-only",gateway_namespace="ns-a",namespace="ns-a"} 2
meridio_2_distributiongroup_endpoints{dg="dg-a",gateway="gw-referenced-only",gateway_namespace="ns-a",namespace="ns-a"} 0
`
	require.NoError(t, testutil.CollectAndCompare(collector, strings.NewReader(expected), "meridio_2_distributiongroup_endpoints"))
}

// TestDistributionGroupCollector_EmptyUnion_FallsBackToEmptyGatewayLabel verifies that a DG with
// no accepted reference and no owned slices (genuinely unbound) still emits a single
// endpoints=0/max_endpoints series under gateway=""/gateway_namespace="", rather than emitting no
// series at all — so an unbound DG stays visible in the metric stream.
func TestDistributionGroupCollector_EmptyUnion_FallsBackToEmptyGatewayLabel(t *testing.T) {
	dg := &meridio2v1alpha1.DistributionGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "dg-unbound", Namespace: "ns-a"},
		Spec: meridio2v1alpha1.DistributionGroupSpec{
			Type:   meridio2v1alpha1.DistributionGroupTypeMaglev,
			Maglev: &meridio2v1alpha1.MaglevConfig{MaxEndpoints: 16},
		},
	}

	fakeClient := dcNewFakeClient(dg)
	collector := NewDistributionGroupCollector(fakeClient, alwaysSyncedWaiter{}, time.Second, "", testControllerName, "meridio_2")

	expected := `
# HELP meridio_2_distributiongroup_endpoints Current endpoint count for this DistributionGroup under the given Gateway.
# TYPE meridio_2_distributiongroup_endpoints gauge
meridio_2_distributiongroup_endpoints{dg="dg-unbound",gateway="",gateway_namespace="",namespace="ns-a"} 0
# HELP meridio_2_distributiongroup_max_endpoints Upper bound on endpoint count for this DistributionGroup under the given Gateway, per its distribution strategy (spec.type). For Maglev this is the hash table capacity (spec.maglev.maxEndpoints); +Inf for any strategy without a bounded capacity concept.
# TYPE meridio_2_distributiongroup_max_endpoints gauge
meridio_2_distributiongroup_max_endpoints{dg="dg-unbound",gateway="",gateway_namespace="",namespace="ns-a"} 16
`
	require.NoError(t, testutil.CollectAndCompare(collector, strings.NewReader(expected),
		"meridio_2_distributiongroup_endpoints", "meridio_2_distributiongroup_max_endpoints"))
}

// TestDistributionGroupCollector_ReferencedGatewayNotAccepted_ExcludedFromUnion verifies that a
// Gateway referenced by the DG (direct parentRef) but with no Accepted condition at all does
// not contribute a series on its own
func TestDistributionGroupCollector_ReferencedGatewayNotAccepted_ExcludedFromUnion(t *testing.T) {
	dg := &meridio2v1alpha1.DistributionGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "dg-a", Namespace: "ns-a"},
		Spec: meridio2v1alpha1.DistributionGroupSpec{
			Type: meridio2v1alpha1.DistributionGroupTypeMaglev,
			ParentRefs: []meridio2v1alpha1.ParentReference{
				{Name: "gw-not-accepted"},
			},
		},
	}
	gwNotAccepted := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "gw-not-accepted", Namespace: "ns-a"},
		// No Accepted condition at all: the referenced Gateway must not be treated
		// as accepted-by-us just because it was referenced.
	}

	fakeClient := dcNewFakeClient(dg, gwNotAccepted)
	collector := NewDistributionGroupCollector(fakeClient, alwaysSyncedWaiter{}, time.Second, "", testControllerName, "meridio_2")

	// Union ends up empty (the only referenced Gateway has no Accepted condition, and there are
	// no owned slices), so this falls back to the empty-gateway-label series.
	expected := `
# HELP meridio_2_distributiongroup_endpoints Current endpoint count for this DistributionGroup under the given Gateway.
# TYPE meridio_2_distributiongroup_endpoints gauge
meridio_2_distributiongroup_endpoints{dg="dg-a",gateway="",gateway_namespace="",namespace="ns-a"} 0
`
	require.NoError(t, testutil.CollectAndCompare(collector, strings.NewReader(expected), "meridio_2_distributiongroup_endpoints"))
}

// TestDistributionGroupCollector_ReferencedGatewayAcceptedByAnotherController_ExcludedFromUnion
// verifies that a Gateway referenced by the DG (direct parentRef) but Accepted=True by a
// different controller does not contribute a series on its own.
func TestDistributionGroupCollector_ReferencedGatewayAcceptedByAnotherController_ExcludedFromUnion(t *testing.T) {
	dg := &meridio2v1alpha1.DistributionGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "dg-a", Namespace: "ns-a"},
		Spec: meridio2v1alpha1.DistributionGroupSpec{
			Type: meridio2v1alpha1.DistributionGroupTypeMaglev,
			ParentRefs: []meridio2v1alpha1.ParentReference{
				{Name: "gw-other-controller"},
			},
		},
	}
	gwOtherController := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "gw-other-controller", Namespace: "ns-a"},
		Status: gatewayv1.GatewayStatus{
			Conditions: []metav1.Condition{acceptedCondition("other.example.com/gateway-controller")},
		},
	}

	fakeClient := dcNewFakeClient(dg, gwOtherController)
	collector := NewDistributionGroupCollector(fakeClient, alwaysSyncedWaiter{}, time.Second, "", testControllerName, "meridio_2")

	// Union ends up empty (the only referenced Gateway is accepted by someone else, and there
	// are no owned slices), so this falls back to the empty-gateway-label series.
	expected := `
# HELP meridio_2_distributiongroup_endpoints Current endpoint count for this DistributionGroup under the given Gateway.
# TYPE meridio_2_distributiongroup_endpoints gauge
meridio_2_distributiongroup_endpoints{dg="dg-a",gateway="",gateway_namespace="",namespace="ns-a"} 0
`
	require.NoError(t, testutil.CollectAndCompare(collector, strings.NewReader(expected), "meridio_2_distributiongroup_endpoints"))
}

func TestDistributionGroupCollector_NoDistributionGroups_NoSeries(t *testing.T) {
	fakeClient := dcNewFakeClient()
	collector := NewDistributionGroupCollector(fakeClient, alwaysSyncedWaiter{}, time.Second, "", testControllerName, "meridio_2")

	require.NoError(t, testutil.CollectAndCompare(collector, strings.NewReader(""),
		"meridio_2_distributiongroup_ready", "meridio_2_distributiongroup_endpoints", "meridio_2_distributiongroup_max_endpoints"))
}
