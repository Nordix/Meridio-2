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
	"net"
	"testing"

	meridio2v1alpha1 "github.com/nordix/meridio-2/api/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

const (
	testControllerName  = "example.com/gateway-controller"
	testNamespace       = "meridio-2"
	testDGName          = "test-dg"
	testGatewayName     = "test-gateway"
	testGWClassName     = "test-class"
	testGWConfigName    = "test-gwconfig"
	testNetworkSubnet   = "192.168.100.0/24"
	testNetworkSubnetV6 = "2001:db8:100::/64"
)

func newScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	_ = meridio2v1alpha1.AddToScheme(scheme)
	_ = gatewayv1.Install(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = discoveryv1.AddToScheme(scheme)
	return scheme
}

func setupReconciler(objects ...client.Object) (*DistributionGroupReconciler, client.Client) {
	fakeClient := fake.NewClientBuilder().
		WithScheme(newScheme()).
		WithObjects(objects...).
		WithStatusSubresource(&meridio2v1alpha1.DistributionGroup{}).
		Build()

	return &DistributionGroupReconciler{
		Client:         fakeClient,
		Scheme:         newScheme(),
		ControllerName: testControllerName,
		Namespace:      testNamespace,
		IPScraper:      fakeIPScraper,
	}, fakeClient
}

func reconcileRequest() ctrl.Request {
	return ctrl.Request{NamespacedName: types.NamespacedName{
		Name:      testDGName,
		Namespace: testNamespace,
	}}
}

// fakeIPScraper returns the Pod's PodIP if it falls within the requested CIDR.
// This avoids needing Multus annotations in tests.
func fakeIPScraper(pod *corev1.Pod, cidr, _ string) string {
	for _, ip := range allPodIPs(pod) {
		if ipInCIDR(ip, cidr) {
			return ip
		}
	}
	return ""
}

func allPodIPs(pod *corev1.Pod) []string {
	ips := make([]string, 0, len(pod.Status.PodIPs))
	for _, pip := range pod.Status.PodIPs {
		ips = append(ips, pip.IP)
	}
	return ips
}

func ipInCIDR(ip, cidr string) bool {
	_, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return false
	}
	return ipnet.Contains(net.ParseIP(ip))
}

func newDG(opts ...func(*meridio2v1alpha1.DistributionGroup)) *meridio2v1alpha1.DistributionGroup {
	dg := &meridio2v1alpha1.DistributionGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:       testDGName,
			Namespace:  testNamespace,
			Generation: 1,
		},
		Spec: meridio2v1alpha1.DistributionGroupSpec{
			Type: meridio2v1alpha1.DistributionGroupTypeMaglev,
			Maglev: &meridio2v1alpha1.MaglevConfig{
				MaxEndpoints: 32,
			},
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "target"},
			},
		},
	}
	for _, o := range opts {
		o(dg)
	}
	return dg
}

func newGateway(accepted bool) *gatewayv1.Gateway {
	gw := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testGatewayName,
			Namespace: testNamespace,
		},
		Spec: gatewayv1.GatewaySpec{
			GatewayClassName: gatewayv1.ObjectName(testGWClassName),
			Infrastructure: &gatewayv1.GatewayInfrastructure{
				ParametersRef: &gatewayv1.LocalParametersReference{
					Group: gatewayv1.Group(meridio2v1alpha1.GroupVersion.Group),
					Kind:  "GatewayConfiguration",
					Name:  testGWConfigName,
				},
			},
		},
	}
	if accepted {
		gw.Status.Conditions = []metav1.Condition{{
			Type:    string(gatewayv1.GatewayConditionAccepted),
			Status:  metav1.ConditionTrue,
			Reason:  "Accepted",
			Message: "Managed by " + testControllerName,
		}}
	}
	return gw
}

func newGatewayConfig() *meridio2v1alpha1.GatewayConfiguration {
	return &meridio2v1alpha1.GatewayConfiguration{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testGWConfigName,
			Namespace: testNamespace,
		},
		Spec: meridio2v1alpha1.GatewayConfigurationSpec{
			NetworkSubnets: []meridio2v1alpha1.NetworkSubnet{
				{AttachmentType: "NAD", CIDRs: []string{testNetworkSubnet}},
			},
			NetworkAttachments: []meridio2v1alpha1.NetworkAttachment{
				{Type: "NAD", NAD: &meridio2v1alpha1.NAD{Name: "macvlan", Namespace: testNamespace, Interface: "net1"}},
			},
			HorizontalScaling: meridio2v1alpha1.HorizontalScaling{Replicas: 1},
		},
	}
}

func newL34Route(gatewayName, dgName string) *meridio2v1alpha1.L34Route {
	dgGroup := meridio2v1alpha1.GroupVersion.Group
	dgKind := "DistributionGroup"
	return &meridio2v1alpha1.L34Route{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-route",
			Namespace: testNamespace,
		},
		Spec: meridio2v1alpha1.L34RouteSpec{
			ParentRefs: []gatewayv1.ParentReference{
				{Name: gatewayv1.ObjectName(gatewayName)},
			},
			BackendRefs: []gatewayv1.BackendRef{
				{BackendObjectReference: gatewayv1.BackendObjectReference{
					Group: (*gatewayv1.Group)(&dgGroup),
					Kind:  (*gatewayv1.Kind)(&dgKind),
					Name:  gatewayv1.ObjectName(dgName),
				}},
			},
			DestinationCIDRs: []string{"20.0.0.1/32"},
			Protocols:        []meridio2v1alpha1.TransportProtocol{meridio2v1alpha1.TCP},
			Priority:         1,
		},
	}
}

func newPod(name, ip string, ready bool) *corev1.Pod {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: testNamespace,
			Labels:    map[string]string{"app": "target"},
			UID:       types.UID(name),
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			PodIP: ip,
			PodIPs: []corev1.PodIP{
				{IP: ip},
			},
		},
	}
	if ready {
		pod.Status.Conditions = []corev1.PodCondition{
			{Type: corev1.PodReady, Status: corev1.ConditionTrue},
		}
	}
	return pod
}

func getDGStatus(t *testing.T, c client.Client) *meridio2v1alpha1.DistributionGroup {
	t.Helper()
	var dg meridio2v1alpha1.DistributionGroup
	err := c.Get(context.Background(), reconcileRequest().NamespacedName, &dg)
	require.NoError(t, err)
	return &dg
}

func listEndpointSlices(t *testing.T, c client.Client) []discoveryv1.EndpointSlice {
	t.Helper()
	var list discoveryv1.EndpointSliceList
	err := c.List(context.Background(), &list, client.InNamespace(testNamespace))
	require.NoError(t, err)
	return list.Items
}

func findCondition(conditions []metav1.Condition, condType string) *metav1.Condition {
	for i := range conditions {
		if conditions[i].Type == condType {
			return &conditions[i]
		}
	}
	return nil
}

// --- Reconciler Tests ---

func TestReconcile_DGNotFound(t *testing.T) {
	r, _ := setupReconciler()
	result, err := r.Reconcile(context.Background(), reconcileRequest())
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)
}

func TestReconcile_NoMatchingPods(t *testing.T) {
	dg := newDG()
	gw := newGateway(true)
	gwConfig := newGatewayConfig()
	route := newL34Route(testGatewayName, testDGName)

	r, c := setupReconciler(dg, gw, gwConfig, route)
	result, err := r.Reconcile(context.Background(), reconcileRequest())
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)

	// Status should be Ready=False with "No Pods match selector"
	updated := getDGStatus(t, c)
	cond := findCondition(updated.Status.Conditions, conditionTypeReady)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, reasonNoEndpoints, cond.Reason)
	assert.Equal(t, messageNoMatchingPods, cond.Message)

	// No EndpointSlices should exist
	slices := listEndpointSlices(t, c)
	assert.Empty(t, slices)
}

func TestReconcile_NoAcceptedGateways(t *testing.T) {
	dg := newDG()
	gw := newGateway(false) // not accepted
	gwConfig := newGatewayConfig()
	route := newL34Route(testGatewayName, testDGName)
	pod := newPod("pod-1", "192.168.100.10", true)

	r, c := setupReconciler(dg, gw, gwConfig, route, pod)
	result, err := r.Reconcile(context.Background(), reconcileRequest())
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)

	updated := getDGStatus(t, c)
	cond := findCondition(updated.Status.Conditions, conditionTypeReady)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, messageNoAcceptedGateways, cond.Message)

	slices := listEndpointSlices(t, c)
	assert.Empty(t, slices)
}

func TestReconcile_NoReferencedGateways(t *testing.T) {
	dg := newDG()
	// No L34Route, no parentRefs → no Gateways
	pod := newPod("pod-1", "192.168.100.10", true)

	r, c := setupReconciler(dg, pod)
	result, err := r.Reconcile(context.Background(), reconcileRequest())
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)

	updated := getDGStatus(t, c)
	cond := findCondition(updated.Status.Conditions, conditionTypeReady)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, messageNoReferencedGateways, cond.Message)
}

func TestReconcile_HappyPath_CreatesEndpointSlice(t *testing.T) {
	dg := newDG()
	gw := newGateway(true)
	gwConfig := newGatewayConfig()
	route := newL34Route(testGatewayName, testDGName)
	pod := newPod("pod-1", "192.168.100.10", true)

	r, c := setupReconciler(dg, gw, gwConfig, route, pod)
	result, err := r.Reconcile(context.Background(), reconcileRequest())
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)

	// Should create EndpointSlice
	slices := listEndpointSlices(t, c)
	require.Len(t, slices, 1)
	assert.Len(t, slices[0].Endpoints, 1)
	assert.Equal(t, "192.168.100.10", slices[0].Endpoints[0].Addresses[0])
	assert.Equal(t, discoveryv1.AddressTypeIPv4, slices[0].AddressType)

	// Labels
	assert.Equal(t, managedByValue, slices[0].Labels[labelManagedBy])
	assert.Equal(t, testDGName, slices[0].Labels[labelDistributionGroup])

	// Status should be Ready=True
	updated := getDGStatus(t, c)
	cond := findCondition(updated.Status.Conditions, conditionTypeReady)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionTrue, cond.Status)
	assert.Equal(t, reasonEndpointsAvailable, cond.Reason)
}

func TestReconcile_HappyPath_MaglevIDsAssigned(t *testing.T) {
	dg := newDG()
	gw := newGateway(true)
	gwConfig := newGatewayConfig()
	route := newL34Route(testGatewayName, testDGName)
	pod1 := newPod("pod-1", "192.168.100.10", true)
	pod2 := newPod("pod-2", "192.168.100.11", true)

	r, c := setupReconciler(dg, gw, gwConfig, route, pod1, pod2)
	result, err := r.Reconcile(context.Background(), reconcileRequest())
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)

	slices := listEndpointSlices(t, c)
	require.Len(t, slices, 1)
	assert.Len(t, slices[0].Endpoints, 2)

	// All endpoints should have Maglev zone
	for _, ep := range slices[0].Endpoints {
		require.NotNil(t, ep.Zone, "endpoint should have Maglev zone")
		assert.Contains(t, *ep.Zone, maglevIDPrefix)
	}
}

func TestReconcile_MaglevCapacityExceeded(t *testing.T) {
	dg := newDG(func(dg *meridio2v1alpha1.DistributionGroup) {
		dg.Spec.Maglev.MaxEndpoints = 1 // only 1 slot
	})
	gw := newGateway(true)
	gwConfig := newGatewayConfig()
	route := newL34Route(testGatewayName, testDGName)
	pod1 := newPod("pod-1", "192.168.100.10", true)
	pod2 := newPod("pod-2", "192.168.100.11", true)

	r, c := setupReconciler(dg, gw, gwConfig, route, pod1, pod2)
	result, err := r.Reconcile(context.Background(), reconcileRequest())
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)

	// Only 1 endpoint (capacity limit)
	slices := listEndpointSlices(t, c)
	require.Len(t, slices, 1)
	assert.Len(t, slices[0].Endpoints, 1)

	// CapacityExceeded condition should be set
	updated := getDGStatus(t, c)
	capCond := findCondition(updated.Status.Conditions, conditionTypeCapacityExceeded)
	require.NotNil(t, capCond)
	assert.Equal(t, metav1.ConditionTrue, capCond.Status)
	assert.Equal(t, reasonMaglevCapacityExceeded, capCond.Reason)
}

func TestReconcile_DGWithDirectParentRef(t *testing.T) {
	gwGroup := gatewayv1.GroupName
	gwKind := "Gateway"
	dg := newDG(func(dg *meridio2v1alpha1.DistributionGroup) {
		dg.Spec.ParentRefs = []meridio2v1alpha1.ParentReference{
			{Name: testGatewayName, Group: &gwGroup, Kind: &gwKind},
		}
	})
	gw := newGateway(true)
	gwConfig := newGatewayConfig()
	// No L34Route — DG references Gateway directly
	pod := newPod("pod-1", "192.168.100.10", true)

	r, c := setupReconciler(dg, gw, gwConfig, pod)
	result, err := r.Reconcile(context.Background(), reconcileRequest())
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)

	slices := listEndpointSlices(t, c)
	require.Len(t, slices, 1)
	assert.Len(t, slices[0].Endpoints, 1)
}

func TestReconcile_DualStack(t *testing.T) {
	dg := newDG()
	gw := newGateway(true)
	gwConfig := newGatewayConfig()
	gwConfig.Spec.NetworkSubnets = []meridio2v1alpha1.NetworkSubnet{
		{AttachmentType: "NAD", CIDRs: []string{testNetworkSubnet}},
		{AttachmentType: "NAD", CIDRs: []string{testNetworkSubnetV6}},
	}
	route := newL34Route(testGatewayName, testDGName)
	pod := newPod("pod-1", "192.168.100.10", true)
	// Add IPv6 address
	pod.Status.PodIPs = append(pod.Status.PodIPs, corev1.PodIP{IP: "2001:db8:100::10"})

	r, c := setupReconciler(dg, gw, gwConfig, route, pod)
	result, err := r.Reconcile(context.Background(), reconcileRequest())
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)

	// Should create 2 EndpointSlices (one per network)
	slices := listEndpointSlices(t, c)
	require.Len(t, slices, 2)

	// Verify one IPv4 and one IPv6
	addressTypes := map[discoveryv1.AddressType]bool{}
	for _, s := range slices {
		addressTypes[s.AddressType] = true
		assert.Len(t, s.Endpoints, 1)
	}
	assert.True(t, addressTypes[discoveryv1.AddressTypeIPv4], "should have IPv4 slice")
	assert.True(t, addressTypes[discoveryv1.AddressTypeIPv6], "should have IPv6 slice")
}

func TestReconcile_PodNotReady_EndpointNotReady(t *testing.T) {
	dg := newDG()
	gw := newGateway(true)
	gwConfig := newGatewayConfig()
	route := newL34Route(testGatewayName, testDGName)
	pod := newPod("pod-1", "192.168.100.10", false) // not ready

	r, c := setupReconciler(dg, gw, gwConfig, route, pod)
	result, err := r.Reconcile(context.Background(), reconcileRequest())
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)

	slices := listEndpointSlices(t, c)
	require.Len(t, slices, 1)
	require.Len(t, slices[0].Endpoints, 1)
	assert.False(t, *slices[0].Endpoints[0].Conditions.Ready)
}

func TestReconcile_RouteReferencesWrongGateway_NoEndpoints(t *testing.T) {
	dg := newDG()
	gw := newGateway(true)
	gwConfig := newGatewayConfig()
	route := newL34Route("other-gateway", testDGName) // wrong gateway
	pod := newPod("pod-1", "192.168.100.10", true)

	r, c := setupReconciler(dg, gw, gwConfig, route, pod)
	result, err := r.Reconcile(context.Background(), reconcileRequest())
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)

	slices := listEndpointSlices(t, c)
	assert.Empty(t, slices)
}

func TestReconcile_RouteReferencesWrongDG_NoEndpoints(t *testing.T) {
	dg := newDG()
	gw := newGateway(true)
	gwConfig := newGatewayConfig()
	route := newL34Route(testGatewayName, "other-dg") // wrong DG
	pod := newPod("pod-1", "192.168.100.10", true)

	r, c := setupReconciler(dg, gw, gwConfig, route, pod)
	result, err := r.Reconcile(context.Background(), reconcileRequest())
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)

	slices := listEndpointSlices(t, c)
	assert.Empty(t, slices)
}

func TestReconcile_PodIPOutsideSubnet_Excluded(t *testing.T) {
	dg := newDG()
	gw := newGateway(true)
	gwConfig := newGatewayConfig()
	route := newL34Route(testGatewayName, testDGName)
	// Pod IP not in 192.168.100.0/24
	pod := newPod("pod-1", "10.0.0.5", true)

	r, c := setupReconciler(dg, gw, gwConfig, route, pod)
	result, err := r.Reconcile(context.Background(), reconcileRequest())
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)

	// No endpoints (IP doesn't match subnet)
	slices := listEndpointSlices(t, c)
	assert.Empty(t, slices)

	updated := getDGStatus(t, c)
	cond := findCondition(updated.Status.Conditions, conditionTypeReady)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
}

func TestReconcile_Idempotent(t *testing.T) {
	dg := newDG()
	gw := newGateway(true)
	gwConfig := newGatewayConfig()
	route := newL34Route(testGatewayName, testDGName)
	pod := newPod("pod-1", "192.168.100.10", true)

	r, c := setupReconciler(dg, gw, gwConfig, route, pod)

	// First reconcile
	_, err := r.Reconcile(context.Background(), reconcileRequest())
	require.NoError(t, err)
	slices1 := listEndpointSlices(t, c)
	require.Len(t, slices1, 1)

	// Second reconcile — same result, no extra slices
	_, err = r.Reconcile(context.Background(), reconcileRequest())
	require.NoError(t, err)
	slices2 := listEndpointSlices(t, c)
	require.Len(t, slices2, 1)
	assert.Equal(t, slices1[0].Name, slices2[0].Name)
	assert.Len(t, slices2[0].Endpoints, 1)
}

func TestReconcile_MaglevIDStability(t *testing.T) {
	dg := newDG()
	gw := newGateway(true)
	gwConfig := newGatewayConfig()
	route := newL34Route(testGatewayName, testDGName)
	pod1 := newPod("pod-1", "192.168.100.10", true)
	pod2 := newPod("pod-2", "192.168.100.11", true)

	r, c := setupReconciler(dg, gw, gwConfig, route, pod1, pod2)

	// First reconcile
	_, err := r.Reconcile(context.Background(), reconcileRequest())
	require.NoError(t, err)
	slices := listEndpointSlices(t, c)
	require.Len(t, slices, 1)

	// Capture initial ID assignments
	initialIDs := map[string]string{}
	for _, ep := range slices[0].Endpoints {
		initialIDs[ep.TargetRef.Name] = *ep.Zone
	}
	require.Len(t, initialIDs, 2)

	// Second reconcile — IDs must be preserved
	_, err = r.Reconcile(context.Background(), reconcileRequest())
	require.NoError(t, err)
	slices = listEndpointSlices(t, c)
	require.Len(t, slices, 1)

	for _, ep := range slices[0].Endpoints {
		assert.Equal(t, initialIDs[ep.TargetRef.Name], *ep.Zone,
			"Maglev ID for %s should be stable across reconciles", ep.TargetRef.Name)
	}
}

func TestReconcile_CleanupWhenPodsDisappear(t *testing.T) {
	dg := newDG()
	gw := newGateway(true)
	gwConfig := newGatewayConfig()
	route := newL34Route(testGatewayName, testDGName)
	pod := newPod("pod-1", "192.168.100.10", true)

	r, c := setupReconciler(dg, gw, gwConfig, route, pod)

	// First reconcile — creates EndpointSlice
	_, err := r.Reconcile(context.Background(), reconcileRequest())
	require.NoError(t, err)
	assert.Len(t, listEndpointSlices(t, c), 1)

	// Delete the Pod
	require.NoError(t, c.Delete(context.Background(), pod))

	// Second reconcile — should delete EndpointSlice
	_, err = r.Reconcile(context.Background(), reconcileRequest())
	require.NoError(t, err)
	assert.Empty(t, listEndpointSlices(t, c))

	updated := getDGStatus(t, c)
	cond := findCondition(updated.Status.Conditions, conditionTypeReady)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, messageNoMatchingPods, cond.Message)
}

func TestReconcile_CapacityExceededConditionRemovedOnRecovery(t *testing.T) {
	dg := newDG(func(dg *meridio2v1alpha1.DistributionGroup) {
		dg.Spec.Maglev.MaxEndpoints = 1
	})
	gw := newGateway(true)
	gwConfig := newGatewayConfig()
	route := newL34Route(testGatewayName, testDGName)
	pod1 := newPod("pod-1", "192.168.100.10", true)
	pod2 := newPod("pod-2", "192.168.100.11", true)

	r, c := setupReconciler(dg, gw, gwConfig, route, pod1, pod2)

	// First reconcile — capacity exceeded (2 pods, 1 slot)
	_, err := r.Reconcile(context.Background(), reconcileRequest())
	require.NoError(t, err)
	updated := getDGStatus(t, c)
	require.NotNil(t, findCondition(updated.Status.Conditions, conditionTypeCapacityExceeded))

	// Remove one Pod to recover capacity
	require.NoError(t, c.Delete(context.Background(), pod2))

	// Second reconcile — capacity recovered
	_, err = r.Reconcile(context.Background(), reconcileRequest())
	require.NoError(t, err)
	updated = getDGStatus(t, c)
	assert.Nil(t, findCondition(updated.Status.Conditions, conditionTypeCapacityExceeded),
		"CapacityExceeded condition should be removed when capacity recovers")
	cond := findCondition(updated.Status.Conditions, conditionTypeReady)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionTrue, cond.Status)
}

func TestReconcile_NoNetworkContext(t *testing.T) {
	dg := newDG()
	gw := newGateway(true)
	gwConfig := newGatewayConfig()
	gwConfig.Spec.NetworkSubnets = nil // no network subnets
	route := newL34Route(testGatewayName, testDGName)
	pod := newPod("pod-1", "192.168.100.10", true)

	r, c := setupReconciler(dg, gw, gwConfig, route, pod)
	_, err := r.Reconcile(context.Background(), reconcileRequest())
	require.NoError(t, err)

	updated := getDGStatus(t, c)
	cond := findCondition(updated.Status.Conditions, conditionTypeReady)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, messageNoNetworkContext, cond.Message)
}

func TestReconcile_DGBeingDeleted_Skipped(t *testing.T) {
	now := metav1.Now()
	dg := newDG(func(dg *meridio2v1alpha1.DistributionGroup) {
		dg.DeletionTimestamp = &now
		dg.Finalizers = []string{"test-finalizer"} // required for fake client
	})

	r, _ := setupReconciler(dg)
	result, err := r.Reconcile(context.Background(), reconcileRequest())
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)
}

func TestReconcile_EndpointSliceModifiedExternally(t *testing.T) {
	dg := newDG()
	gw := newGateway(true)
	gwConfig := newGatewayConfig()
	route := newL34Route(testGatewayName, testDGName)
	pod := newPod("pod-1", "192.168.100.10", true)

	r, c := setupReconciler(dg, gw, gwConfig, route, pod)

	// First reconcile
	_, err := r.Reconcile(context.Background(), reconcileRequest())
	require.NoError(t, err)
	slices := listEndpointSlices(t, c)
	require.Len(t, slices, 1)

	// Tamper with the EndpointSlice externally
	tampered := slices[0].DeepCopy()
	tampered.Endpoints[0].Addresses = []string{"99.99.99.99"}
	require.NoError(t, c.Update(context.Background(), tampered))

	// Reconcile should overwrite the tampered address
	_, err = r.Reconcile(context.Background(), reconcileRequest())
	require.NoError(t, err)
	slices = listEndpointSlices(t, c)
	require.Len(t, slices, 1)
	assert.Equal(t, "192.168.100.10", slices[0].Endpoints[0].Addresses[0])
}
