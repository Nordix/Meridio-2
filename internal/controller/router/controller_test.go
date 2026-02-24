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

package router

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	meridio2v1alpha1 "github.com/nordix/meridio-2/api/v1alpha1"
	"github.com/nordix/meridio-2/internal/bird"
)

const testGatewayName = "test-gateway"
const testNamespace = "default"

// mockingBird is a test double for bird.BirdInterface that captures Configure arguments
type mockingBird struct {
	configureVIPs    []string
	configureRouters []*meridio2v1alpha1.GatewayRouter
	configureErr     error
}

func (m *mockingBird) Run(ctx context.Context) error {
	return nil
}

func (m *mockingBird) Configure(ctx context.Context, vips []string, routers []*meridio2v1alpha1.GatewayRouter) error {
	m.configureVIPs = vips
	m.configureRouters = routers
	return m.configureErr
}

func (m *mockingBird) Monitor(ctx context.Context, interval time.Duration) (<-chan bird.MonitorStatus, error) {
	ch := make(chan bird.MonitorStatus)
	close(ch)
	return ch, nil
}

func newScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	_ = gatewayapiv1.Install(scheme)
	_ = meridio2v1alpha1.AddToScheme(scheme)
	return scheme
}

func newGateway(name, namespace string, addresses ...string) *gatewayapiv1.Gateway {
	gw := &gatewayapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: gatewayapiv1.GatewaySpec{
			GatewayClassName: "test-class",
		},
	}

	if len(addresses) > 0 {
		gw.Status.Addresses = make([]gatewayapiv1.GatewayStatusAddress, len(addresses))
		for i, addr := range addresses {
			gw.Status.Addresses[i] = gatewayapiv1.GatewayStatusAddress{
				Type:  ptr(gatewayapiv1.IPAddressType),
				Value: addr,
			}
		}
	}

	return gw
}

func newGatewayRouter(name string, gatewayRef gatewayapiv1.ParentReference) *meridio2v1alpha1.GatewayRouter {
	return &meridio2v1alpha1.GatewayRouter{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: testNamespace,
		},
		Spec: meridio2v1alpha1.GatewayRouterSpec{
			GatewayRef: gatewayRef,
		},
	}
}

func setupReconciler(objects ...client.Object) (*RouterReconciler, client.Client) {
	fakeClient := fake.NewClientBuilder().
		WithScheme(newScheme()).
		WithObjects(objects...).
		WithStatusSubresource(&gatewayapiv1.Gateway{}, &meridio2v1alpha1.GatewayRouter{}).
		Build()

	return &RouterReconciler{
		Client:           fakeClient,
		Scheme:           newScheme(),
		GatewayName:      testGatewayName,
		GatewayNamespace: testNamespace,
		Bird:             &mockingBird{},
	}, fakeClient
}

func ptr[T any](v T) *T {
	return &v
}

func TestReconciler_Reconcile(t *testing.T) {
	t.Run("Reconcile_SkipsNonMatchingGatewayName", func(t *testing.T) {
		gw := newGateway("other-gateway", testNamespace)
		reconciler, _ := setupReconciler(gw)

		request := reconcile.Request{NamespacedName: types.NamespacedName{Name: "other-gateway", Namespace: testNamespace}}
		result, err := reconciler.Reconcile(context.Background(), request)

		assert.NoError(t, err)
		assert.Equal(t, ctrl.Result{}, result)
	})

	t.Run("Reconcile_SkipsNonMatchingGatewayNamespace", func(t *testing.T) {
		gw := newGateway(testGatewayName, "other-namespace")
		reconciler, _ := setupReconciler(gw)

		request := reconcile.Request{NamespacedName: types.NamespacedName{Name: testGatewayName, Namespace: "other-namespace"}}
		result, err := reconciler.Reconcile(context.Background(), request)

		assert.NoError(t, err)
		assert.Equal(t, ctrl.Result{}, result)
	})

	t.Run("Reconcile_HandlesGatewayNotFound", func(t *testing.T) {
		reconciler, _ := setupReconciler()

		request := reconcile.Request{NamespacedName: types.NamespacedName{Name: testGatewayName, Namespace: testNamespace}}
		result, err := reconciler.Reconcile(context.Background(), request)

		assert.NoError(t, err)
		assert.Equal(t, ctrl.Result{}, result)
	})

	t.Run("Reconcile_ProcessesMatchingGateway", func(t *testing.T) {
		gw := newGateway(testGatewayName, testNamespace, "40.0.0.1", "40.0.0.2")
		reconciler, fakeClient := setupReconciler(gw)
		mock := reconciler.Bird.(*mockingBird)

		request := reconcile.Request{NamespacedName: types.NamespacedName{Name: testGatewayName, Namespace: testNamespace}}
		result, err := reconciler.Reconcile(context.Background(), request)

		assert.NoError(t, err)
		assert.Equal(t, ctrl.Result{}, result)
		assert.Equal(t, []string{"40.0.0.1", "40.0.0.2"}, mock.configureVIPs)

		fetched := &gatewayapiv1.Gateway{}
		assert.NoError(t, fakeClient.Get(context.Background(), types.NamespacedName{Name: testGatewayName, Namespace: testNamespace}, fetched))
	})

	t.Run("Reconcile_ConfigureError_ReturnsError", func(t *testing.T) {
		gw := newGateway(testGatewayName, testNamespace, "40.0.0.1")
		reconciler, _ := setupReconciler(gw)
		mock := reconciler.Bird.(*mockingBird)
		mock.configureErr = fmt.Errorf("birdc failed")

		request := reconcile.Request{NamespacedName: types.NamespacedName{Name: testGatewayName, Namespace: testNamespace}}
		_, err := reconciler.Reconcile(context.Background(), request)

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "birdc failed")
	})
}

func TestGetVIPs(t *testing.T) {
	t.Run("ExtractsAddressesFromGatewayStatus", func(t *testing.T) {
		gw := newGateway(testGatewayName, testNamespace, "20.0.0.1", "40.0.0.1")
		vips := getVIPs(gw)

		assert.Equal(t, []string{"20.0.0.1", "40.0.0.1"}, vips)
	})

	t.Run("ReturnsEmptyWhenNoAddresses", func(t *testing.T) {
		gw := newGateway(testGatewayName, testNamespace)
		vips := getVIPs(gw)

		assert.Empty(t, vips)
	})

	t.Run("SkipsNonIPAddressTypes", func(t *testing.T) {
		gw := newGateway(testGatewayName, testNamespace, "20.0.0.1")
		gw.Status.Addresses = append(gw.Status.Addresses,
			gatewayapiv1.GatewayStatusAddress{
				Type:  ptr(gatewayapiv1.HostnameAddressType),
				Value: "example.com",
			},
			gatewayapiv1.GatewayStatusAddress{
				Value: "no-type",
			},
		)
		vips := getVIPs(gw)

		assert.Equal(t, []string{"20.0.0.1"}, vips)
	})
}

func TestMakeNamespacedName(t *testing.T) {
	t.Run("UsesDefaultNamespaceWhenRefNamespaceIsNil", func(t *testing.T) {
		ref := gatewayapiv1.ParentReference{
			Name: "test-gateway",
		}
		result := makeNamespacedName(ref, "default-ns")

		assert.Equal(t, types.NamespacedName{
			Name:      "test-gateway",
			Namespace: "default-ns",
		}, result)
	})

	t.Run("UsesExplicitNamespaceWhenProvided", func(t *testing.T) {
		ns := gatewayapiv1.Namespace("explicit-ns")
		ref := gatewayapiv1.ParentReference{
			Name:      "test-gateway",
			Namespace: &ns,
		}
		result := makeNamespacedName(ref, "default-ns")

		assert.Equal(t, types.NamespacedName{
			Name:      "test-gateway",
			Namespace: "explicit-ns",
		}, result)
	})
}

func TestGetGatewayRouters(t *testing.T) {
	t.Run("ReturnsEmptyWhenNoRoutersMatch", func(t *testing.T) {
		gw := newGateway(testGatewayName, testNamespace)
		reconciler, _ := setupReconciler(gw)

		gateway := types.NamespacedName{Name: testGatewayName, Namespace: testNamespace}
		routers, err := reconciler.getGatewayRouters(context.Background(), gateway)

		assert.NoError(t, err)
		assert.Empty(t, routers)
	})

	t.Run("ReturnsMatchingRouters", func(t *testing.T) {
		gw := newGateway(testGatewayName, testNamespace)
		gwRef := gatewayapiv1.ParentReference{
			Name:      gatewayapiv1.ObjectName(testGatewayName),
			Namespace: ptr(gatewayapiv1.Namespace(testNamespace)),
		}
		router1 := newGatewayRouter("router1", gwRef)
		router2 := newGatewayRouter("router2", gwRef)

		reconciler, _ := setupReconciler(gw, router1, router2)

		gateway := types.NamespacedName{Name: testGatewayName, Namespace: testNamespace}
		routers, err := reconciler.getGatewayRouters(context.Background(), gateway)

		assert.NoError(t, err)
		assert.Equal(t, 2, len(routers))
	})

	t.Run("FiltersOutNonMatchingRouters", func(t *testing.T) {
		gw := newGateway(testGatewayName, testNamespace)
		matchingRef := gatewayapiv1.ParentReference{
			Name:      gatewayapiv1.ObjectName(testGatewayName),
			Namespace: ptr(gatewayapiv1.Namespace(testNamespace)),
		}
		nonMatchingRef := gatewayapiv1.ParentReference{
			Name:      "other-gateway",
			Namespace: ptr(gatewayapiv1.Namespace(testNamespace)),
		}
		router1 := newGatewayRouter("router1", matchingRef)
		router2 := newGatewayRouter("router2", nonMatchingRef)

		reconciler, _ := setupReconciler(gw, router1, router2)

		gateway := types.NamespacedName{Name: testGatewayName, Namespace: testNamespace}
		routers, err := reconciler.getGatewayRouters(context.Background(), gateway)

		assert.NoError(t, err)
		assert.Equal(t, 1, len(routers))
		assert.Equal(t, "router1", routers[0].Name)
	})
}

func TestGatewayRouterEnqueue(t *testing.T) {
	t.Run("MatchingRouter_Enqueues", func(t *testing.T) {
		gwRef := gatewayapiv1.ParentReference{
			Name:      gatewayapiv1.ObjectName(testGatewayName),
			Namespace: ptr(gatewayapiv1.Namespace(testNamespace)),
		}
		router := newGatewayRouter("router1", gwRef)
		reconciler, _ := setupReconciler()

		requests := reconciler.gatewayRouterEnqueue(context.Background(), router)

		assert.Len(t, requests, 1)
		assert.Equal(t, testGatewayName, requests[0].Name)
		assert.Equal(t, testNamespace, requests[0].Namespace)
	})

	t.Run("DifferentName_ReturnsNil", func(t *testing.T) {
		gwRef := gatewayapiv1.ParentReference{
			Name:      "other-gateway",
			Namespace: ptr(gatewayapiv1.Namespace(testNamespace)),
		}
		router := newGatewayRouter("router1", gwRef)
		reconciler, _ := setupReconciler()

		requests := reconciler.gatewayRouterEnqueue(context.Background(), router)

		assert.Nil(t, requests)
	})

	t.Run("DifferentNamespace_ReturnsNil", func(t *testing.T) {
		gwRef := gatewayapiv1.ParentReference{
			Name:      gatewayapiv1.ObjectName(testGatewayName),
			Namespace: ptr(gatewayapiv1.Namespace("other-namespace")),
		}
		router := newGatewayRouter("router1", gwRef)
		reconciler, _ := setupReconciler()

		requests := reconciler.gatewayRouterEnqueue(context.Background(), router)

		assert.Nil(t, requests)
	})

	t.Run("NilNamespace_DefaultsToObjectNamespace_Matching", func(t *testing.T) {
		gwRef := gatewayapiv1.ParentReference{
			Name: gatewayapiv1.ObjectName(testGatewayName),
		}
		router := newGatewayRouter("router1", gwRef)
		reconciler, _ := setupReconciler()

		requests := reconciler.gatewayRouterEnqueue(context.Background(), router)

		assert.Len(t, requests, 1)
	})

	t.Run("NilNamespace_DefaultsToObjectNamespace_NonMatching", func(t *testing.T) {
		gwRef := gatewayapiv1.ParentReference{
			Name: gatewayapiv1.ObjectName(testGatewayName),
		}
		router := newGatewayRouter("router1", gwRef)
		router.Namespace = "other-namespace"
		reconciler, _ := setupReconciler()

		requests := reconciler.gatewayRouterEnqueue(context.Background(), router)

		assert.Nil(t, requests)
	})
}
