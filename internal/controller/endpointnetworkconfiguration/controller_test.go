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

package endpointnetworkconfiguration

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	meridio2v1alpha1 "github.com/nordix/meridio-2/api/v1alpha1"
)

const (
	testNamespace = "default"
	testPodName   = "app-pod-1"
)

func newScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = corev1.AddToScheme(s)
	_ = meridio2v1alpha1.AddToScheme(s)
	_ = gatewayv1.Install(s)
	return s
}

func newPod(name string, phase corev1.PodPhase, labels map[string]string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: testNamespace,
			Labels:    labels,
			UID:       types.UID(name + "-uid"),
		},
		Status: corev1.PodStatus{Phase: phase},
	}
}

func setupReconciler(objects ...client.Object) (*Reconciler, client.Client) {
	scheme := newScheme()
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(&meridio2v1alpha1.EndpointNetworkConfiguration{}).
		Build()

	return &Reconciler{
		Client:         fakeClient,
		Scheme:         scheme,
		ControllerName: "test-controller",
		Namespace:      testNamespace,
	}, fakeClient
}

func TestReconcile_PodNotFound(t *testing.T) {
	r, _ := setupReconciler()

	result, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "nonexistent", Namespace: testNamespace},
	})

	assert.NoError(t, err)
	assert.Equal(t, reconcile.Result{}, result)
}

func TestReconcile_PodNotRunning(t *testing.T) {
	pod := newPod(testPodName, corev1.PodPending, nil)
	// Pre-existing ENC should be cleaned up when Pod is not running
	enc := &meridio2v1alpha1.EndpointNetworkConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: testPodName, Namespace: testNamespace},
	}
	r, fakeClient := setupReconciler(pod, enc)

	result, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: testPodName, Namespace: testNamespace},
	})

	assert.NoError(t, err)
	assert.Equal(t, reconcile.Result{}, result)

	// ENC should be deleted
	var deleted meridio2v1alpha1.EndpointNetworkConfiguration
	err = fakeClient.Get(context.Background(), types.NamespacedName{Name: testPodName, Namespace: testNamespace}, &deleted)
	assert.True(t, err != nil, "ENC should be deleted for non-running Pod")
}

func TestReconcile_NoMatchingDGs_NoENCCreated(t *testing.T) {
	pod := newPod(testPodName, corev1.PodRunning, map[string]string{"app": "test"})
	r, fakeClient := setupReconciler(pod)

	result, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: testPodName, Namespace: testNamespace},
	})

	assert.NoError(t, err)
	assert.Equal(t, reconcile.Result{}, result)

	// Verify no ENC was created
	var enc meridio2v1alpha1.EndpointNetworkConfiguration
	err = fakeClient.Get(context.Background(), types.NamespacedName{Name: testPodName, Namespace: testNamespace}, &enc)
	assert.True(t, err != nil, "ENC should not exist")
}

func TestReconcileENC_CreatesNew(t *testing.T) {
	pod := newPod(testPodName, corev1.PodRunning, nil)
	r, fakeClient := setupReconciler(pod)

	connections := []meridio2v1alpha1.GatewayConnection{
		{
			Name: "sllb-a",
			Domains: []meridio2v1alpha1.NetworkDomain{
				{
					Name:     "sllb-a-IPv4",
					IPFamily: "IPv4",
					Network: meridio2v1alpha1.NetworkIdentity{
						Subnet:        "169.111.60.0/24",
						InterfaceHint: "net1",
					},
					VIPs:     []string{"20.0.0.1", "20.0.0.2"},
					NextHops: []string{"169.111.60.3"},
				},
			},
		},
	}

	err := r.reconcileENC(context.Background(), pod, connections)
	require.NoError(t, err)

	var enc meridio2v1alpha1.EndpointNetworkConfiguration
	err = fakeClient.Get(context.Background(), types.NamespacedName{Name: testPodName, Namespace: testNamespace}, &enc)
	require.NoError(t, err)

	assert.Equal(t, testPodName, enc.Name)
	assert.Len(t, enc.Spec.Gateways, 1)
	assert.Equal(t, "sllb-a", enc.Spec.Gateways[0].Name)
	assert.Len(t, enc.Spec.Gateways[0].Domains, 1)
	assert.Equal(t, "sllb-a-IPv4", enc.Spec.Gateways[0].Domains[0].Name)
	assert.Equal(t, []string{"20.0.0.1", "20.0.0.2"}, enc.Spec.Gateways[0].Domains[0].VIPs)
	assert.Equal(t, []string{"169.111.60.3"}, enc.Spec.Gateways[0].Domains[0].NextHops)

	// Verify ownerReference
	require.Len(t, enc.OwnerReferences, 1)
	assert.Equal(t, pod.Name, enc.OwnerReferences[0].Name)
	assert.Equal(t, "Pod", enc.OwnerReferences[0].Kind)
}

func TestReconcileENC_UpdatesWhenSpecChanges(t *testing.T) {
	pod := newPod(testPodName, corev1.PodRunning, nil)
	r, fakeClient := setupReconciler(pod)

	// Create initial ENC
	initial := []meridio2v1alpha1.GatewayConnection{
		{Name: "sllb-a", Domains: []meridio2v1alpha1.NetworkDomain{
			{Name: "sllb-a-IPv4", IPFamily: "IPv4", VIPs: []string{"20.0.0.1"}},
		}},
	}
	err := r.reconcileENC(context.Background(), pod, initial)
	require.NoError(t, err)

	// Update with new VIPs
	updated := []meridio2v1alpha1.GatewayConnection{
		{Name: "sllb-a", Domains: []meridio2v1alpha1.NetworkDomain{
			{Name: "sllb-a-IPv4", IPFamily: "IPv4", VIPs: []string{"20.0.0.1", "20.0.0.2"}},
		}},
	}
	err = r.reconcileENC(context.Background(), pod, updated)
	require.NoError(t, err)

	var enc meridio2v1alpha1.EndpointNetworkConfiguration
	err = fakeClient.Get(context.Background(), types.NamespacedName{Name: testPodName, Namespace: testNamespace}, &enc)
	require.NoError(t, err)
	assert.Equal(t, []string{"20.0.0.1", "20.0.0.2"}, enc.Spec.Gateways[0].Domains[0].VIPs)
}

func TestReconcileENC_NoUpdateWhenSpecUnchanged(t *testing.T) {
	pod := newPod(testPodName, corev1.PodRunning, nil)
	r, fakeClient := setupReconciler(pod)

	connections := []meridio2v1alpha1.GatewayConnection{
		{Name: "sllb-a", Domains: []meridio2v1alpha1.NetworkDomain{
			{Name: "sllb-a-IPv4", IPFamily: "IPv4", VIPs: []string{"20.0.0.1"}},
		}},
	}

	// Create
	err := r.reconcileENC(context.Background(), pod, connections)
	require.NoError(t, err)

	// Get resourceVersion after create
	var enc meridio2v1alpha1.EndpointNetworkConfiguration
	err = fakeClient.Get(context.Background(), types.NamespacedName{Name: testPodName, Namespace: testNamespace}, &enc)
	require.NoError(t, err)
	rvAfterCreate := enc.ResourceVersion

	// Reconcile again with same spec
	err = r.reconcileENC(context.Background(), pod, connections)
	require.NoError(t, err)

	// ResourceVersion should not change (no update call)
	err = fakeClient.Get(context.Background(), types.NamespacedName{Name: testPodName, Namespace: testNamespace}, &enc)
	require.NoError(t, err)
	assert.Equal(t, rvAfterCreate, enc.ResourceVersion)
}

func TestDeleteENCIfExists_Exists(t *testing.T) {
	pod := newPod(testPodName, corev1.PodRunning, nil)
	enc := &meridio2v1alpha1.EndpointNetworkConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: testPodName, Namespace: testNamespace},
	}
	r, fakeClient := setupReconciler(pod, enc)

	err := r.deleteENCIfExists(context.Background(), types.NamespacedName{Name: testPodName, Namespace: testNamespace})
	assert.NoError(t, err)

	var deleted meridio2v1alpha1.EndpointNetworkConfiguration
	err = fakeClient.Get(context.Background(), types.NamespacedName{Name: testPodName, Namespace: testNamespace}, &deleted)
	assert.True(t, err != nil, "ENC should be deleted")
}

func TestDeleteENCIfExists_NotExists(t *testing.T) {
	r, _ := setupReconciler()

	err := r.deleteENCIfExists(context.Background(), types.NamespacedName{Name: "nonexistent", Namespace: testNamespace})
	assert.NoError(t, err)
}
