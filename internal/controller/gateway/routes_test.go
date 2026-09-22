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

package gateway

import (
	"context"
	"testing"

	meridio2v1alpha1 "github.com/nordix/meridio-2/api/v1alpha1"
	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/nordix/meridio-2/internal/common/gatewayutil"
)

func TestRouteReferencesGateway(t *testing.T) {
	gw := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "test-gw", Namespace: "default"},
	}

	t.Run("MatchingReference", func(t *testing.T) {
		route := &meridio2v1alpha1.L34Route{
			ObjectMeta: metav1.ObjectMeta{Namespace: "default"},
			Spec: meridio2v1alpha1.L34RouteSpec{
				ParentRefs: []gatewayv1.ParentReference{
					{Name: "test-gw"},
				},
			},
		}
		assert.True(t, routeReferencesGateway(route, gw))
	})

	t.Run("DifferentName", func(t *testing.T) {
		route := &meridio2v1alpha1.L34Route{
			ObjectMeta: metav1.ObjectMeta{Namespace: "default"},
			Spec: meridio2v1alpha1.L34RouteSpec{
				ParentRefs: []gatewayv1.ParentReference{
					{Name: "other-gw"},
				},
			},
		}
		assert.False(t, routeReferencesGateway(route, gw))
	})

	t.Run("DifferentNamespace", func(t *testing.T) {
		ns := gatewayv1.Namespace("other-ns")
		route := &meridio2v1alpha1.L34Route{
			ObjectMeta: metav1.ObjectMeta{Namespace: "default"},
			Spec: meridio2v1alpha1.L34RouteSpec{
				ParentRefs: []gatewayv1.ParentReference{
					{Name: "test-gw", Namespace: &ns},
				},
			},
		}
		assert.False(t, routeReferencesGateway(route, gw))
	})

	t.Run("NonGatewayKind", func(t *testing.T) {
		kind := gatewayv1.Kind("HTTPRoute")
		route := &meridio2v1alpha1.L34Route{
			ObjectMeta: metav1.ObjectMeta{Namespace: "default"},
			Spec: meridio2v1alpha1.L34RouteSpec{
				ParentRefs: []gatewayv1.ParentReference{
					{Name: "test-gw", Kind: &kind},
				},
			},
		}
		assert.False(t, routeReferencesGateway(route, gw))
	})

	t.Run("NonGatewayGroup", func(t *testing.T) {
		group := gatewayv1.Group("example.com")
		route := &meridio2v1alpha1.L34Route{
			ObjectMeta: metav1.ObjectMeta{Namespace: "default"},
			Spec: meridio2v1alpha1.L34RouteSpec{
				ParentRefs: []gatewayv1.ParentReference{
					{Name: "test-gw", Group: &group},
				},
			},
		}
		assert.False(t, routeReferencesGateway(route, gw))
	})
}

func TestMapL34RouteToGateway(t *testing.T) {
	t.Run("AcceptedGateway", func(t *testing.T) {
		gw := &gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{Name: "test-gw", Namespace: "default"},
			Status: gatewayv1.GatewayStatus{
				Conditions: []metav1.Condition{
					{
						Type:    string(gatewayv1.GatewayConditionAccepted),
						Status:  metav1.ConditionTrue,
						Message: gatewayutil.GatewayAcceptedMessagePrefix + testControllerName,
					},
				},
			},
		}
		route := &meridio2v1alpha1.L34Route{
			ObjectMeta: metav1.ObjectMeta{Name: "test-route", Namespace: "default"},
			Spec: meridio2v1alpha1.L34RouteSpec{
				ParentRefs:       []gatewayv1.ParentReference{{Name: "test-gw"}},
				DestinationCIDRs: []string{"20.0.0.1/32"},
				Protocols:        []meridio2v1alpha1.TransportProtocol{meridio2v1alpha1.TCP},
				Priority:         1,
			},
		}

		reconciler, _ := setupReconciler(gw, route)
		requests := reconciler.mapL34RouteToGateway(context.Background(), route)

		assert.Len(t, requests, 1)
		assert.Equal(t, ctrl.Request{NamespacedName: types.NamespacedName{Name: "test-gw", Namespace: "default"}}, requests[0])
	})

	t.Run("NotAcceptedGateway", func(t *testing.T) {
		gw := &gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{Name: "test-gw", Namespace: "default"},
		}
		route := &meridio2v1alpha1.L34Route{
			ObjectMeta: metav1.ObjectMeta{Name: "test-route", Namespace: "default"},
			Spec: meridio2v1alpha1.L34RouteSpec{
				ParentRefs:       []gatewayv1.ParentReference{{Name: "test-gw"}},
				DestinationCIDRs: []string{"20.0.0.1/32"},
				Protocols:        []meridio2v1alpha1.TransportProtocol{meridio2v1alpha1.TCP},
				Priority:         1,
			},
		}

		reconciler, _ := setupReconciler(gw, route)
		requests := reconciler.mapL34RouteToGateway(context.Background(), route)

		// Mapper enqueues all Gateway parentRefs (no pre-filtering)
		// Reconcile loop decides whether to process based on acceptance
		assert.Len(t, requests, 1)
		assert.Equal(t, ctrl.Request{NamespacedName: types.NamespacedName{Name: "test-gw", Namespace: "default"}}, requests[0])
	})

	t.Run("NonGatewayParentRef", func(t *testing.T) {
		kind := gatewayv1.Kind("HTTPRoute")
		route := &meridio2v1alpha1.L34Route{
			ObjectMeta: metav1.ObjectMeta{Name: "test-route", Namespace: "default"},
			Spec: meridio2v1alpha1.L34RouteSpec{
				ParentRefs:       []gatewayv1.ParentReference{{Name: "test-gw", Kind: &kind}},
				DestinationCIDRs: []string{"20.0.0.1/32"},
				Protocols:        []meridio2v1alpha1.TransportProtocol{meridio2v1alpha1.TCP},
				Priority:         1,
			},
		}

		reconciler, _ := setupReconciler(route)
		requests := reconciler.mapL34RouteToGateway(context.Background(), route)

		assert.Empty(t, requests)
	})
}

func TestUpdateAddressesFromRoutes(t *testing.T) {
	t.Run("SingleRoute", func(t *testing.T) {
		gw := &gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{Name: "test-gw", Namespace: "default"},
		}
		route := &meridio2v1alpha1.L34Route{
			ObjectMeta: metav1.ObjectMeta{Name: "test-route", Namespace: "default"},
			Spec: meridio2v1alpha1.L34RouteSpec{
				ParentRefs:       []gatewayv1.ParentReference{{Name: "test-gw"}},
				DestinationCIDRs: []string{"20.0.0.1/32"},
				Protocols:        []meridio2v1alpha1.TransportProtocol{meridio2v1alpha1.TCP},
				Priority:         1,
			},
		}

		reconciler, fakeClient := setupReconciler(gw, route)
		err := reconciler.updateAddressesFromRoutes(context.Background(), gw)
		assert.NoError(t, err)

		fetched := &gatewayv1.Gateway{}
		err = fakeClient.Get(context.Background(), types.NamespacedName{Name: "test-gw", Namespace: "default"}, fetched)
		assert.NoError(t, err)
		assert.Len(t, fetched.Status.Addresses, 1)
		assert.Equal(t, "20.0.0.1", fetched.Status.Addresses[0].Value)
	})

	t.Run("MultipleRoutes", func(t *testing.T) {
		gw := &gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{Name: "test-gw", Namespace: "default"},
		}
		route1 := &meridio2v1alpha1.L34Route{
			ObjectMeta: metav1.ObjectMeta{Name: "route1", Namespace: "default"},
			Spec: meridio2v1alpha1.L34RouteSpec{
				ParentRefs:       []gatewayv1.ParentReference{{Name: "test-gw"}},
				DestinationCIDRs: []string{"20.0.0.1/32"},
				Protocols:        []meridio2v1alpha1.TransportProtocol{meridio2v1alpha1.TCP},
				Priority:         1,
			},
		}
		route2 := &meridio2v1alpha1.L34Route{
			ObjectMeta: metav1.ObjectMeta{Name: "route2", Namespace: "default"},
			Spec: meridio2v1alpha1.L34RouteSpec{
				ParentRefs:       []gatewayv1.ParentReference{{Name: "test-gw"}},
				DestinationCIDRs: []string{"20.0.0.2/32"},
				Protocols:        []meridio2v1alpha1.TransportProtocol{meridio2v1alpha1.TCP},
				Priority:         1,
			},
		}

		reconciler, fakeClient := setupReconciler(gw, route1, route2)
		err := reconciler.updateAddressesFromRoutes(context.Background(), gw)
		assert.NoError(t, err)

		fetched := &gatewayv1.Gateway{}
		err = fakeClient.Get(context.Background(), types.NamespacedName{Name: "test-gw", Namespace: "default"}, fetched)
		assert.NoError(t, err)
		assert.Len(t, fetched.Status.Addresses, 2)
	})

	t.Run("DuplicateCIDRs", func(t *testing.T) {
		gw := &gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{Name: "test-gw", Namespace: "default"},
		}
		route1 := &meridio2v1alpha1.L34Route{
			ObjectMeta: metav1.ObjectMeta{Name: "route1", Namespace: "default"},
			Spec: meridio2v1alpha1.L34RouteSpec{
				ParentRefs:       []gatewayv1.ParentReference{{Name: "test-gw"}},
				DestinationCIDRs: []string{"20.0.0.1/32"},
				Protocols:        []meridio2v1alpha1.TransportProtocol{meridio2v1alpha1.TCP},
				Priority:         1,
			},
		}
		route2 := &meridio2v1alpha1.L34Route{
			ObjectMeta: metav1.ObjectMeta{Name: "route2", Namespace: "default"},
			Spec: meridio2v1alpha1.L34RouteSpec{
				ParentRefs:       []gatewayv1.ParentReference{{Name: "test-gw"}},
				DestinationCIDRs: []string{"20.0.0.1/32"},
				Protocols:        []meridio2v1alpha1.TransportProtocol{meridio2v1alpha1.UDP},
				Priority:         1,
			},
		}

		reconciler, fakeClient := setupReconciler(gw, route1, route2)
		err := reconciler.updateAddressesFromRoutes(context.Background(), gw)
		assert.NoError(t, err)

		fetched := &gatewayv1.Gateway{}
		err = fakeClient.Get(context.Background(), types.NamespacedName{Name: "test-gw", Namespace: "default"}, fetched)
		assert.NoError(t, err)
		assert.Len(t, fetched.Status.Addresses, 1)
		assert.Equal(t, "20.0.0.1", fetched.Status.Addresses[0].Value)
	})
}

func TestAddressesEqual(t *testing.T) {
	t.Run("Equal", func(t *testing.T) {
		a := []gatewayv1.GatewayStatusAddress{
			{Value: "20.0.0.1"},
			{Value: "20.0.0.2"},
		}
		b := []gatewayv1.GatewayStatusAddress{
			{Value: "20.0.0.1"},
			{Value: "20.0.0.2"},
		}
		assert.True(t, addressesEqual(a, b))
	})

	t.Run("DifferentLength", func(t *testing.T) {
		a := []gatewayv1.GatewayStatusAddress{{Value: "20.0.0.1"}}
		b := []gatewayv1.GatewayStatusAddress{{Value: "20.0.0.1"}, {Value: "20.0.0.2"}}
		assert.False(t, addressesEqual(a, b))
	})

	t.Run("DifferentValues", func(t *testing.T) {
		a := []gatewayv1.GatewayStatusAddress{{Value: "20.0.0.1"}}
		b := []gatewayv1.GatewayStatusAddress{{Value: "20.0.0.2"}}
		assert.False(t, addressesEqual(a, b))
	})

	t.Run("Empty", func(t *testing.T) {
		assert.True(t, addressesEqual(nil, nil))
		assert.True(t, addressesEqual([]gatewayv1.GatewayStatusAddress{}, []gatewayv1.GatewayStatusAddress{}))
	})
}
