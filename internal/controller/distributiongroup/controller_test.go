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
	"fmt"
	"net"
	"testing"

	meridio2v1alpha1 "github.com/nordix/meridio-2/api/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

const (
	tcControllerName   = "example.com/gateway-controller"
	tcNamespace        = "meridio-2"
	tcDGName           = "test-dg"
	tcGatewayName      = "test-gateway"
	tcGWClassName      = "test-class"
	tcGWConfigName     = "test-gwconfig"
	tcInternalSubnet   = "192.168.100.0/24"
	tcInternalSubnetV6 = "2001:db8:100::/64"
)

func tcScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	_ = meridio2v1alpha1.AddToScheme(scheme)
	_ = gatewayv1.Install(scheme)
	_ = corev1.AddToScheme(scheme)
	return scheme
}

func tcSetupReconciler(objects ...client.Object) (*DistributionGroupReconciler, client.Client) {
	fakeClient := fake.NewClientBuilder().
		WithScheme(tcScheme()).
		WithObjects(objects...).
		WithStatusSubresource(&meridio2v1alpha1.DistributionGroup{}).
		WithIndex(&meridio2v1alpha1.LoadBalancerEndpointSlice{},
			"spec.distributionGroupName",
			func(obj client.Object) []string {
				slice := obj.(*meridio2v1alpha1.LoadBalancerEndpointSlice)
				return []string{slice.Spec.DistributionGroupName}
			},
		).
		Build()

	return &DistributionGroupReconciler{
		Client:               fakeClient,
		Scheme:               tcScheme(),
		ControllerName:       tcControllerName,
		Namespace:            tcNamespace,
		MaxEndpointsPerSlice: 200,
		IPScraper:            tcFakeIPScraper,
	}, fakeClient
}

func tcReconcileRequest() ctrl.Request {
	return ctrl.Request{NamespacedName: types.NamespacedName{
		Name:      tcDGName,
		Namespace: tcNamespace,
	}}
}

// tcFakeIPScraper returns the Pod's PodIP if it falls within the requested CIDR.
// Avoids needing Multus annotations in tests.
func tcFakeIPScraper(pod *corev1.Pod, cidr, _ string) string {
	_, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return ""
	}
	for _, pip := range pod.Status.PodIPs {
		if ipnet.Contains(net.ParseIP(pip.IP)) {
			return pip.IP
		}
	}
	return ""
}

func tcNewDG(opts ...func(*meridio2v1alpha1.DistributionGroup)) *meridio2v1alpha1.DistributionGroup {
	dg := &meridio2v1alpha1.DistributionGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:       tcDGName,
			Namespace:  tcNamespace,
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

func tcNewGateway(accepted bool) *gatewayv1.Gateway {
	gw := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      tcGatewayName,
			Namespace: tcNamespace,
		},
		Spec: gatewayv1.GatewaySpec{
			GatewayClassName: gatewayv1.ObjectName(tcGWClassName),
			Infrastructure: &gatewayv1.GatewayInfrastructure{
				ParametersRef: &gatewayv1.LocalParametersReference{
					Group: gatewayv1.Group(meridio2v1alpha1.GroupVersion.Group),
					Kind:  kindGatewayConfiguration,
					Name:  tcGWConfigName,
				},
			},
		},
	}
	if accepted {
		gw.Status.Conditions = []metav1.Condition{{
			Type:    string(gatewayv1.GatewayConditionAccepted),
			Status:  metav1.ConditionTrue,
			Reason:  "Accepted",
			Message: "Managed by " + tcControllerName,
		}}
	}
	return gw
}

func tcNewGatewayConfig() *meridio2v1alpha1.GatewayConfiguration {
	return &meridio2v1alpha1.GatewayConfiguration{
		ObjectMeta: metav1.ObjectMeta{
			Name:      tcGWConfigName,
			Namespace: tcNamespace,
		},
		Spec: meridio2v1alpha1.GatewayConfigurationSpec{
			InternalSubnets: []meridio2v1alpha1.InternalSubnet{
				{AttachmentType: "NAD", CIDR: tcInternalSubnet},
			},
			NetworkAttachments: []meridio2v1alpha1.NetworkAttachment{
				{Type: "NAD", NAD: &meridio2v1alpha1.NAD{Name: "macvlan", Namespace: tcNamespace, Interface: "net1"}},
			},
			HorizontalScaling: meridio2v1alpha1.HorizontalScaling{Replicas: 1},
		},
	}
}

func tcNewL34Route(gatewayName, dgName string) *meridio2v1alpha1.L34Route {
	dgGroup := meridio2v1alpha1.GroupVersion.Group
	dgKind := kindDistributionGroup
	return &meridio2v1alpha1.L34Route{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-route",
			Namespace: tcNamespace,
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

func tcNewPod(name string, ready bool, ips ...string) *corev1.Pod {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: tcNamespace,
			Labels:    map[string]string{"app": "target"},
			UID:       types.UID(name),
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
		},
	}
	if len(ips) > 0 {
		pod.Status.PodIP = ips[0]
		for _, ip := range ips {
			pod.Status.PodIPs = append(pod.Status.PodIPs, corev1.PodIP{IP: ip})
		}
	}
	if ready {
		pod.Status.Conditions = []corev1.PodCondition{
			{Type: corev1.PodReady, Status: corev1.ConditionTrue},
		}
	}
	return pod
}

func tcGetDG(t *testing.T, c client.Client) *meridio2v1alpha1.DistributionGroup {
	t.Helper()
	var dg meridio2v1alpha1.DistributionGroup
	err := c.Get(context.Background(), tcReconcileRequest().NamespacedName, &dg)
	require.NoError(t, err)
	return &dg
}

func tcListSlices(t *testing.T, c client.Client) []meridio2v1alpha1.LoadBalancerEndpointSlice {
	t.Helper()
	var list meridio2v1alpha1.LoadBalancerEndpointSliceList
	err := c.List(context.Background(), &list, client.InNamespace(tcNamespace))
	require.NoError(t, err)
	return list.Items
}

func tcFindCondition(conditions []metav1.Condition, condType string) *metav1.Condition {
	for i := range conditions {
		if conditions[i].Type == condType {
			return &conditions[i]
		}
	}
	return nil
}

// --- Status and Gateway resolution tests ---

func TestReconcile_DGNotFound(t *testing.T) {
	r, _ := tcSetupReconciler()
	result, err := r.Reconcile(context.Background(), tcReconcileRequest())
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)
}

func TestReconcile_NoMatchingPods(t *testing.T) {
	dg := tcNewDG()
	gw := tcNewGateway(true)
	gwConfig := tcNewGatewayConfig()
	route := tcNewL34Route(tcGatewayName, tcDGName)

	r, c := tcSetupReconciler(dg, gw, gwConfig, route)
	result, err := r.Reconcile(context.Background(), tcReconcileRequest())
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)

	// Status should be Ready=False with "No Pods match selector"
	updated := tcGetDG(t, c)
	cond := tcFindCondition(updated.Status.Conditions, conditionTypeReady)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, reasonNoEndpoints, cond.Reason)
	assert.Equal(t, messageNoMatchingPods, cond.Message)

	// No slices should exist
	assert.Empty(t, tcListSlices(t, c))
}

func TestReconcile_NoAcceptedGateways(t *testing.T) {
	dg := tcNewDG()
	gw := tcNewGateway(false) // not accepted
	gwConfig := tcNewGatewayConfig()
	route := tcNewL34Route(tcGatewayName, tcDGName)
	pod := tcNewPod("pod-1", true, "192.168.100.10")

	r, c := tcSetupReconciler(dg, gw, gwConfig, route, pod)
	result, err := r.Reconcile(context.Background(), tcReconcileRequest())
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)

	updated := tcGetDG(t, c)
	cond := tcFindCondition(updated.Status.Conditions, conditionTypeReady)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, messageNoAcceptedGateways, cond.Message)

	assert.Empty(t, tcListSlices(t, c))
}

func TestReconcile_NoReferencedGateways(t *testing.T) {
	dg := tcNewDG()
	// No L34Route, no parentRefs → no Gateways
	pod := tcNewPod("pod-1", true, "192.168.100.10")

	r, c := tcSetupReconciler(dg, pod)
	result, err := r.Reconcile(context.Background(), tcReconcileRequest())
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)

	updated := tcGetDG(t, c)
	cond := tcFindCondition(updated.Status.Conditions, conditionTypeReady)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, messageNoReferencedGateways, cond.Message)
}

func TestReconcile_MultipleGateways_SkipsReconciliation(t *testing.T) {
	dg := tcNewDG()
	gw1 := tcNewGateway(true)
	gw2 := tcNewGateway(true)
	gw2.Name = "second-gateway"
	gwConfig := tcNewGatewayConfig()
	route1 := tcNewL34Route(tcGatewayName, tcDGName)
	route2 := tcNewL34Route("second-gateway", tcDGName)
	route2.Name = "route-2"
	pod := tcNewPod("pod-1", true, "192.168.100.10")

	r, c := tcSetupReconciler(dg, gw1, gw2, gwConfig, route1, route2, pod)
	result, err := r.Reconcile(context.Background(), tcReconcileRequest())
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)

	updated := tcGetDG(t, c)
	cond := tcFindCondition(updated.Status.Conditions, conditionTypeReady)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, reasonMultipleGateways, cond.Reason)
	assert.Equal(t, messageMultipleGateways, cond.Message)

	// No slices created
	assert.Empty(t, tcListSlices(t, c))
}

func TestReconcile_MultipleGateways_RecoveryAfterConflictResolved(t *testing.T) {
	dg := tcNewDG()
	gw1 := tcNewGateway(true)
	gw2 := tcNewGateway(true)
	gw2.Name = "second-gateway"
	gwConfig := tcNewGatewayConfig()
	route1 := tcNewL34Route(tcGatewayName, tcDGName)
	route2 := tcNewL34Route("second-gateway", tcDGName)
	route2.Name = "route-2"
	pod := tcNewPod("pod-1", true, "192.168.100.10")

	r, c := tcSetupReconciler(dg, gw1, gw2, gwConfig, route1, route2, pod)

	// First reconcile: multiple Gateways → Ready=False
	_, err := r.Reconcile(context.Background(), tcReconcileRequest())
	require.NoError(t, err)
	updated := tcGetDG(t, c)
	cond := tcFindCondition(updated.Status.Conditions, conditionTypeReady)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, reasonMultipleGateways, cond.Reason)

	// Resolve conflict: delete the second route
	require.NoError(t, c.Delete(context.Background(), route2))

	// Second reconcile: single Gateway → Ready=True
	_, err = r.Reconcile(context.Background(), tcReconcileRequest())
	require.NoError(t, err)
	updated = tcGetDG(t, c)
	cond = tcFindCondition(updated.Status.Conditions, conditionTypeReady)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionTrue, cond.Status)
	assert.Equal(t, reasonEndpointsAvailable, cond.Reason)

	// Slices created
	assert.NotEmpty(t, tcListSlices(t, c))
}

func TestReconcile_NoNetworkContext(t *testing.T) {
	dg := tcNewDG()
	gw := tcNewGateway(true)
	gwConfig := tcNewGatewayConfig()
	gwConfig.Spec.InternalSubnets = nil // no internal subnets
	route := tcNewL34Route(tcGatewayName, tcDGName)
	pod := tcNewPod("pod-1", true, "192.168.100.10")

	r, c := tcSetupReconciler(dg, gw, gwConfig, route, pod)
	_, err := r.Reconcile(context.Background(), tcReconcileRequest())
	require.NoError(t, err)

	updated := tcGetDG(t, c)
	cond := tcFindCondition(updated.Status.Conditions, conditionTypeReady)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, messageNoNetworkContext, cond.Message)
}

func TestReconcile_DGBeingDeleted_Skipped(t *testing.T) {
	now := metav1.Now()
	dg := tcNewDG(func(dg *meridio2v1alpha1.DistributionGroup) {
		dg.DeletionTimestamp = &now
		dg.Finalizers = []string{"test-finalizer"} // required for fake client
	})

	r, _ := tcSetupReconciler(dg)
	result, err := r.Reconcile(context.Background(), tcReconcileRequest())
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)
}

// --- Happy path and endpoint tests ---

func TestReconcile_HappyPath_CreatesSlice(t *testing.T) {
	dg := tcNewDG()
	gw := tcNewGateway(true)
	gwConfig := tcNewGatewayConfig()
	route := tcNewL34Route(tcGatewayName, tcDGName)
	pod := tcNewPod("pod-1", true, "192.168.100.10")

	r, c := tcSetupReconciler(dg, gw, gwConfig, route, pod)
	result, err := r.Reconcile(context.Background(), tcReconcileRequest())
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)

	// Should create LoadBalancerEndpointSlice
	slices := tcListSlices(t, c)
	require.Len(t, slices, 1)
	require.Len(t, slices[0].Spec.Endpoints, 1)

	// Verify endpoint content
	ep := slices[0].Spec.Endpoints[0]
	require.Len(t, ep.Addresses, 1)
	assert.Equal(t, "192.168.100.10", ep.Addresses[0].IP)
	assert.Equal(t, meridio2v1alpha1.IPv4, ep.Addresses[0].Family)

	// Verify spec fields
	assert.Equal(t, tcDGName, slices[0].Spec.DistributionGroupName)
	assert.Equal(t, tcGatewayName, slices[0].Spec.GatewayRef.Name)
	assert.Equal(t, tcNamespace, slices[0].Spec.GatewayRef.Namespace)

	// Labels
	assert.Equal(t, managedByValue, slices[0].Labels[labelManagedBy])

	// Status should be Ready=True
	updated := tcGetDG(t, c)
	cond := tcFindCondition(updated.Status.Conditions, conditionTypeReady)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionTrue, cond.Status)
	assert.Equal(t, reasonEndpointsAvailable, cond.Reason)
}

func TestReconcile_HappyPath_MaglevIDsAssigned(t *testing.T) {
	dg := tcNewDG()
	gw := tcNewGateway(true)
	gwConfig := tcNewGatewayConfig()
	route := tcNewL34Route(tcGatewayName, tcDGName)
	pod1 := tcNewPod("pod-1", true, "192.168.100.10")
	pod2 := tcNewPod("pod-2", true, "192.168.100.11")

	r, c := tcSetupReconciler(dg, gw, gwConfig, route, pod1, pod2)
	result, err := r.Reconcile(context.Background(), tcReconcileRequest())
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)

	slices := tcListSlices(t, c)
	require.Len(t, slices, 1)
	require.Len(t, slices[0].Spec.Endpoints, 2)

	// All endpoints should have Maglev identifiers
	for _, ep := range slices[0].Spec.Endpoints {
		require.NotNil(t, ep.Identifier, "endpoint %s should have Maglev identifier", ep.Target.Name)
	}
}

func TestReconcile_DualStack(t *testing.T) {
	dg := tcNewDG()
	gw := tcNewGateway(true)
	gwConfig := tcNewGatewayConfig()
	gwConfig.Spec.InternalSubnets = []meridio2v1alpha1.InternalSubnet{
		{AttachmentType: "NAD", CIDR: tcInternalSubnet},
		{AttachmentType: "NAD", CIDR: tcInternalSubnetV6},
	}
	route := tcNewL34Route(tcGatewayName, tcDGName)
	pod := tcNewPod("pod-1", true, "192.168.100.10", "2001:db8:100::10")

	r, c := tcSetupReconciler(dg, gw, gwConfig, route, pod)
	result, err := r.Reconcile(context.Background(), tcReconcileRequest())
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)

	// Should create 1 slice with 1 endpoint containing both addresses
	slices := tcListSlices(t, c)
	require.Len(t, slices, 1)
	require.Len(t, slices[0].Spec.Endpoints, 1)

	ep := slices[0].Spec.Endpoints[0]
	require.Len(t, ep.Addresses, 2, "Dual-stack Pod should have 2 addresses in one endpoint")

	// Verify both families present
	families := map[meridio2v1alpha1.IPFamily]string{}
	for _, addr := range ep.Addresses {
		families[addr.Family] = addr.IP
	}
	assert.Equal(t, "192.168.100.10", families[meridio2v1alpha1.IPv4])
	assert.Equal(t, "2001:db8:100::10", families[meridio2v1alpha1.IPv6])
}

func TestReconcile_DualStack_SharedMaglevIDs(t *testing.T) {
	dg := tcNewDG()
	gw := tcNewGateway(true)
	gwConfig := tcNewGatewayConfig()
	gwConfig.Spec.InternalSubnets = []meridio2v1alpha1.InternalSubnet{
		{AttachmentType: "NAD", CIDR: tcInternalSubnet},
		{AttachmentType: "NAD", CIDR: tcInternalSubnetV6},
	}
	route := tcNewL34Route(tcGatewayName, tcDGName)

	// 16 dual-stack Pods
	pods := make([]client.Object, 16)
	for i := range 16 {
		pods[i] = tcNewPod(
			fmt.Sprintf("pod-%d", i), true,
			fmt.Sprintf("192.168.100.%d", 10+i),
			fmt.Sprintf("2001:db8:100::%d", 10+i),
		)
	}

	objs := append([]client.Object{dg, gw, gwConfig, route}, pods...)
	r, c := tcSetupReconciler(objs...)
	result, err := r.Reconcile(context.Background(), tcReconcileRequest())
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)

	// Single slice with all endpoints (dual-stack in one object)
	slices := tcListSlices(t, c)
	require.Len(t, slices, 1)
	require.Len(t, slices[0].Spec.Endpoints, 16)

	// Each endpoint has both addresses and one unique identifier
	usedIDs := make(map[int32]string)
	for _, ep := range slices[0].Spec.Endpoints {
		assert.Len(t, ep.Addresses, 2, "dual-stack Pod should have 2 addresses")
		require.NotNil(t, ep.Identifier)
		if prev, exists := usedIDs[*ep.Identifier]; exists {
			t.Errorf("ID %d collision: used by %s and %s", *ep.Identifier, prev, ep.Target.UID)
		}
		usedIDs[*ep.Identifier] = ep.Target.UID
	}
	assert.Len(t, usedIDs, 16, "all 16 IDs should be unique")
}

func TestReconcile_DualStack_AsymmetricPresence(t *testing.T) {
	dg := tcNewDG()
	gw := tcNewGateway(true)
	gwConfig := tcNewGatewayConfig()
	gwConfig.Spec.InternalSubnets = []meridio2v1alpha1.InternalSubnet{
		{AttachmentType: "NAD", CIDR: tcInternalSubnet},
		{AttachmentType: "NAD", CIDR: tcInternalSubnetV6},
	}
	route := tcNewL34Route(tcGatewayName, tcDGName)
	// pod-1: dual-stack, pod-2: IPv4 only
	pod1 := tcNewPod("pod-1", true, "192.168.100.10", "2001:db8:100::10")
	pod2 := tcNewPod("pod-2", true, "192.168.100.11")

	r, c := tcSetupReconciler(dg, gw, gwConfig, route, pod1, pod2)
	_, err := r.Reconcile(context.Background(), tcReconcileRequest())
	require.NoError(t, err)

	slices := tcListSlices(t, c)
	require.Len(t, slices, 1)
	require.Len(t, slices[0].Spec.Endpoints, 2)

	// Find endpoints by UID
	for _, ep := range slices[0].Spec.Endpoints {
		switch ep.Target.UID {
		case "pod-1":
			assert.Len(t, ep.Addresses, 2, "dual-stack Pod should have 2 addresses")
		case "pod-2":
			assert.Len(t, ep.Addresses, 1, "IPv4-only Pod should have 1 address")
			assert.Equal(t, meridio2v1alpha1.IPv4, ep.Addresses[0].Family)
		}
		// Both should have identifiers
		require.NotNil(t, ep.Identifier)
	}
}

func TestReconcile_PodNotReady_EndpointNotReady(t *testing.T) {
	dg := tcNewDG()
	gw := tcNewGateway(true)
	gwConfig := tcNewGatewayConfig()
	route := tcNewL34Route(tcGatewayName, tcDGName)
	pod := tcNewPod("pod-1", false, "192.168.100.10") // not ready

	r, c := tcSetupReconciler(dg, gw, gwConfig, route, pod)
	result, err := r.Reconcile(context.Background(), tcReconcileRequest())
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)

	slices := tcListSlices(t, c)
	require.Len(t, slices, 1)
	require.Len(t, slices[0].Spec.Endpoints, 1)
	assert.False(t, slices[0].Spec.Endpoints[0].Ready)
}

func TestReconcile_RouteReferencesWrongGateway_NoEndpoints(t *testing.T) {
	dg := tcNewDG()
	gw := tcNewGateway(true)
	gwConfig := tcNewGatewayConfig()
	route := tcNewL34Route("other-gateway", tcDGName) // wrong gateway
	pod := tcNewPod("pod-1", true, "192.168.100.10")

	r, c := tcSetupReconciler(dg, gw, gwConfig, route, pod)
	result, err := r.Reconcile(context.Background(), tcReconcileRequest())
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)

	assert.Empty(t, tcListSlices(t, c))
}

func TestReconcile_RouteReferencesWrongDG_NoEndpoints(t *testing.T) {
	dg := tcNewDG()
	gw := tcNewGateway(true)
	gwConfig := tcNewGatewayConfig()
	route := tcNewL34Route(tcGatewayName, "other-dg") // wrong DG
	pod := tcNewPod("pod-1", true, "192.168.100.10")

	r, c := tcSetupReconciler(dg, gw, gwConfig, route, pod)
	result, err := r.Reconcile(context.Background(), tcReconcileRequest())
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)

	assert.Empty(t, tcListSlices(t, c))
}

func TestReconcile_PodIPOutsideSubnet_Excluded(t *testing.T) {
	dg := tcNewDG()
	gw := tcNewGateway(true)
	gwConfig := tcNewGatewayConfig()
	route := tcNewL34Route(tcGatewayName, tcDGName)
	// Pod IP not in 192.168.100.0/24
	pod := tcNewPod("pod-1", true, "10.0.0.5")

	r, c := tcSetupReconciler(dg, gw, gwConfig, route, pod)
	result, err := r.Reconcile(context.Background(), tcReconcileRequest())
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)

	// No endpoints (IP doesn't match subnet)
	assert.Empty(t, tcListSlices(t, c))

	updated := tcGetDG(t, c)
	cond := tcFindCondition(updated.Status.Conditions, conditionTypeReady)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
}

func TestReconcile_DGWithDirectParentRef(t *testing.T) {
	gwGroup := gatewayv1.GroupName
	gwKind := kindGateway
	dg := tcNewDG(func(dg *meridio2v1alpha1.DistributionGroup) {
		dg.Spec.ParentRefs = []meridio2v1alpha1.ParentReference{
			{Name: tcGatewayName, Group: &gwGroup, Kind: &gwKind},
		}
	})
	gw := tcNewGateway(true)
	gwConfig := tcNewGatewayConfig()
	// No L34Route — DG references Gateway directly
	pod := tcNewPod("pod-1", true, "192.168.100.10")

	r, c := tcSetupReconciler(dg, gw, gwConfig, pod)
	result, err := r.Reconcile(context.Background(), tcReconcileRequest())
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)

	slices := tcListSlices(t, c)
	require.Len(t, slices, 1)
	assert.Len(t, slices[0].Spec.Endpoints, 1)
}

// --- Idempotency, stability, and lifecycle tests ---

func TestReconcile_Idempotent(t *testing.T) {
	dg := tcNewDG()
	gw := tcNewGateway(true)
	gwConfig := tcNewGatewayConfig()
	route := tcNewL34Route(tcGatewayName, tcDGName)
	pod := tcNewPod("pod-1", true, "192.168.100.10")

	r, c := tcSetupReconciler(dg, gw, gwConfig, route, pod)

	// First reconcile
	result, err := r.Reconcile(context.Background(), tcReconcileRequest())
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)
	slices1 := tcListSlices(t, c)
	require.Len(t, slices1, 1)

	// Second reconcile — same result, no extra slices
	result, err = r.Reconcile(context.Background(), tcReconcileRequest())
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)
	slices2 := tcListSlices(t, c)
	require.Len(t, slices2, 1)
	assert.Equal(t, slices1[0].Name, slices2[0].Name)
	assert.Len(t, slices2[0].Spec.Endpoints, 1)
}

func TestReconcile_MaglevIDStability(t *testing.T) {
	dg := tcNewDG()
	gw := tcNewGateway(true)
	gwConfig := tcNewGatewayConfig()
	route := tcNewL34Route(tcGatewayName, tcDGName)
	pod1 := tcNewPod("pod-1", true, "192.168.100.10")
	pod2 := tcNewPod("pod-2", true, "192.168.100.11")

	r, c := tcSetupReconciler(dg, gw, gwConfig, route, pod1, pod2)

	// First reconcile
	result, err := r.Reconcile(context.Background(), tcReconcileRequest())
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)
	slices := tcListSlices(t, c)
	require.Len(t, slices, 1)

	// Capture initial ID assignments
	initialIDs := map[string]int32{}
	for _, ep := range slices[0].Spec.Endpoints {
		require.NotNil(t, ep.Identifier)
		initialIDs[ep.Target.UID] = *ep.Identifier
	}
	require.Len(t, initialIDs, 2)

	// Second reconcile — IDs must be preserved
	result, err = r.Reconcile(context.Background(), tcReconcileRequest())
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)
	slices = tcListSlices(t, c)
	require.Len(t, slices, 1)

	for _, ep := range slices[0].Spec.Endpoints {
		require.NotNil(t, ep.Identifier)
		assert.Equal(t, initialIDs[ep.Target.UID], *ep.Identifier,
			"Maglev ID for %s should be stable across reconciles", ep.Target.Name)
	}
}

func TestReconcile_MaglevCapacityExceeded(t *testing.T) {
	dg := tcNewDG(func(dg *meridio2v1alpha1.DistributionGroup) {
		dg.Spec.Maglev.MaxEndpoints = 1 // only 1 slot
	})
	gw := tcNewGateway(true)
	gwConfig := tcNewGatewayConfig()
	route := tcNewL34Route(tcGatewayName, tcDGName)
	pod1 := tcNewPod("pod-1", true, "192.168.100.10")
	pod2 := tcNewPod("pod-2", true, "192.168.100.11")

	r, c := tcSetupReconciler(dg, gw, gwConfig, route, pod1, pod2)
	result, err := r.Reconcile(context.Background(), tcReconcileRequest())
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)

	// Only 1 endpoint (capacity limit)
	slices := tcListSlices(t, c)
	require.Len(t, slices, 1)
	assert.Len(t, slices[0].Spec.Endpoints, 1)

	// CapacityExceeded condition should be set
	updated := tcGetDG(t, c)
	capCond := tcFindCondition(updated.Status.Conditions, conditionTypeCapacityExceeded)
	require.NotNil(t, capCond)
	assert.Equal(t, metav1.ConditionTrue, capCond.Status)
	assert.Equal(t, reasonMaglevCapacityExceeded, capCond.Reason)
}

func TestReconcile_CapacityExceededConditionRemovedOnRecovery(t *testing.T) {
	dg := tcNewDG(func(dg *meridio2v1alpha1.DistributionGroup) {
		dg.Spec.Maglev.MaxEndpoints = 1
	})
	gw := tcNewGateway(true)
	gwConfig := tcNewGatewayConfig()
	route := tcNewL34Route(tcGatewayName, tcDGName)
	pod1 := tcNewPod("pod-1", true, "192.168.100.10")
	pod2 := tcNewPod("pod-2", true, "192.168.100.11")

	r, c := tcSetupReconciler(dg, gw, gwConfig, route, pod1, pod2)

	// First reconcile — capacity exceeded (2 pods, 1 slot)
	_, err := r.Reconcile(context.Background(), tcReconcileRequest())
	require.NoError(t, err)
	updated := tcGetDG(t, c)
	require.NotNil(t, tcFindCondition(updated.Status.Conditions, conditionTypeCapacityExceeded))

	// Remove one Pod to recover capacity
	require.NoError(t, c.Delete(context.Background(), pod2))

	// Second reconcile — capacity recovered
	_, err = r.Reconcile(context.Background(), tcReconcileRequest())
	require.NoError(t, err)
	updated = tcGetDG(t, c)
	assert.Nil(t, tcFindCondition(updated.Status.Conditions, conditionTypeCapacityExceeded),
		"CapacityExceeded condition should be removed when capacity recovers")
	cond := tcFindCondition(updated.Status.Conditions, conditionTypeReady)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionTrue, cond.Status)
}

func TestReconcile_CleanupWhenPodsDisappear(t *testing.T) {
	dg := tcNewDG()
	gw := tcNewGateway(true)
	gwConfig := tcNewGatewayConfig()
	route := tcNewL34Route(tcGatewayName, tcDGName)
	pod := tcNewPod("pod-1", true, "192.168.100.10")

	r, c := tcSetupReconciler(dg, gw, gwConfig, route, pod)

	// First reconcile — creates slice
	result, err := r.Reconcile(context.Background(), tcReconcileRequest())
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)
	assert.Len(t, tcListSlices(t, c), 1)

	// Delete the Pod
	require.NoError(t, c.Delete(context.Background(), pod))

	// Second reconcile — should delete slice
	result, err = r.Reconcile(context.Background(), tcReconcileRequest())
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)
	assert.Empty(t, tcListSlices(t, c))

	updated := tcGetDG(t, c)
	cond := tcFindCondition(updated.Status.Conditions, conditionTypeReady)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, messageNoMatchingPods, cond.Message)
}

func TestReconcile_SliceModifiedExternally(t *testing.T) {
	dg := tcNewDG()
	gw := tcNewGateway(true)
	gwConfig := tcNewGatewayConfig()
	route := tcNewL34Route(tcGatewayName, tcDGName)
	pod := tcNewPod("pod-1", true, "192.168.100.10")

	r, c := tcSetupReconciler(dg, gw, gwConfig, route, pod)

	// First reconcile
	result, err := r.Reconcile(context.Background(), tcReconcileRequest())
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)
	slices := tcListSlices(t, c)
	require.Len(t, slices, 1)

	// Tamper with the slice externally
	tampered := slices[0].DeepCopy()
	tampered.Spec.Endpoints[0].Addresses[0].IP = "99.99.99.99"
	require.NoError(t, c.Update(context.Background(), tampered))

	// Reconcile should overwrite the tampered address
	result, err = r.Reconcile(context.Background(), tcReconcileRequest())
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)
	slices = tcListSlices(t, c)
	require.Len(t, slices, 1)
	assert.Equal(t, "192.168.100.10", slices[0].Spec.Endpoints[0].Addresses[0].IP)
}
