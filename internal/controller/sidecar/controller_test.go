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

package sidecar

import (
	"context"
	"fmt"
	"net"
	"syscall"
	"testing"

	meridio2v1alpha1 "github.com/nordix/meridio-2/api/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/vishvananda/netlink"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const testPodName = "test-pod"
const testNamespace = "default"
const testPodUID = "test-uid-12345"

func newScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	_ = meridio2v1alpha1.AddToScheme(scheme)
	return scheme
}

func newENC(gateways ...meridio2v1alpha1.GatewayConnection) *meridio2v1alpha1.EndpointNetworkConfiguration {
	return &meridio2v1alpha1.EndpointNetworkConfiguration{
		ObjectMeta: metav1.ObjectMeta{
			Name:       testPodName,
			Namespace:  testNamespace,
			Generation: 1,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "v1",
				Kind:       "Pod",
				Name:       testPodName,
				UID:        testPodUID,
			}},
		},
		Spec: meridio2v1alpha1.EndpointNetworkConfigurationSpec{
			Gateways: gateways,
		},
	}
}

func setupController(nl *mockNetlink, objects ...client.Object) (*Controller, client.Client) {
	fakeClient := fake.NewClientBuilder().
		WithScheme(newScheme()).
		WithObjects(objects...).
		WithStatusSubresource(&meridio2v1alpha1.EndpointNetworkConfiguration{}).
		Build()

	return &Controller{
		Client:     fakeClient,
		Scheme:     newScheme(),
		PodName:    testPodName,
		PodUID:     testPodUID,
		MinTableID: 50000,
		MaxTableID: 55000,
		nl:         nl,
	}, fakeClient
}

func reconcileRequest() ctrl.Request {
	return ctrl.Request{NamespacedName: types.NamespacedName{
		Name:      testPodName,
		Namespace: testNamespace,
	}}
}

func getENCStatus(t *testing.T, fakeClient client.Client) *meridio2v1alpha1.EndpointNetworkConfiguration {
	t.Helper()
	var enc meridio2v1alpha1.EndpointNetworkConfiguration
	err := fakeClient.Get(context.Background(), reconcileRequest().NamespacedName, &enc)
	assert.NoError(t, err)
	return &enc
}

// --- Reconcile tests ---

func TestReconcile_OwnerRefRejection(t *testing.T) {
	nl := newMockNetlink()
	nl.addLink("net1", 10, "192.168.1.5/24")

	tests := []struct {
		name   string
		owners []metav1.OwnerReference
	}{
		{"no ownerRef", nil},
		{"wrong UID", []metav1.OwnerReference{{APIVersion: "v1", Kind: "Pod", Name: testPodName, UID: "wrong-uid"}}},
		{"wrong Kind", []metav1.OwnerReference{{APIVersion: "v1", Kind: "ReplicaSet", Name: testPodName, UID: testPodUID}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			enc := newENC(meridio2v1alpha1.GatewayConnection{
				Name: "gw-a",
				Domains: []meridio2v1alpha1.NetworkDomain{{
					Name:    "v4",
					Network: meridio2v1alpha1.NetworkIdentity{Subnet: "192.168.1.0/24", InterfaceHint: "net1"},
					VIPs:    []string{"20.0.0.1"},
				}},
			})
			enc.OwnerReferences = tt.owners
			c, _ := setupController(nl, enc)

			result, err := c.Reconcile(context.Background(), reconcileRequest())

			assert.NoError(t, err)
			assert.Equal(t, ctrl.Result{}, result)
			// No state should be created
			assert.Empty(t, c.tableIDs.activeGateways())
		})
	}
}

func TestReconcile_ENCNotFound(t *testing.T) {
	nl := newMockNetlink()
	c, _ := setupController(nl)

	result, err := c.Reconcile(context.Background(), reconcileRequest())

	assert.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)
}

func TestReconcile_ENCNotFound_CleansUpState(t *testing.T) {
	nl := newMockNetlink()
	nl.addLink("net1", 10, "192.168.1.5/24")
	c, _ := setupController(nl)

	// Pre-populate state
	c.tableIDs = newTableIDAllocator(50000, 55000)
	_, _ = c.tableIDs.allocate("gw-a")
	c.managedVIPs = map[string]map[string]bool{
		"net1": {"20.0.0.1": true},
	}

	_, err := c.Reconcile(context.Background(), reconcileRequest())
	assert.NoError(t, err)

	assert.Empty(t, c.tableIDs.activeGateways())
	assert.NotNil(t, c.managedVIPs)
	assert.Empty(t, c.managedVIPs)
}

func TestReconcile_EmptyGateways_SetsStatusReady(t *testing.T) {
	nl := newMockNetlink()
	enc := newENC()
	c, fakeClient := setupController(nl, enc)

	result, err := c.Reconcile(context.Background(), reconcileRequest())

	assert.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)

	fetched := getENCStatus(t, fakeClient)
	assert.Len(t, fetched.Status.Conditions, 1)
	assert.Equal(t, metav1.ConditionTrue, fetched.Status.Conditions[0].Status)
	assert.Equal(t, "Configured", fetched.Status.Conditions[0].Reason)
}

func TestReconcile_SingleGateway_DualStack(t *testing.T) {
	nl := newMockNetlink()
	nl.addLink("net1", 10, "192.168.1.5/24")
	nl.addLink("net1v6", 11, "fd00::5/64")

	enc := newENC(meridio2v1alpha1.GatewayConnection{
		Name: "gw-a",
		Domains: []meridio2v1alpha1.NetworkDomain{
			{
				Name:     "v4",
				IPFamily: "IPv4",
				Network:  meridio2v1alpha1.NetworkIdentity{Subnet: "192.168.1.0/24", InterfaceHint: "net1"},
				VIPs:     []string{"20.0.0.1"},
				NextHops: []string{"192.168.1.1"},
			},
			{
				Name:     "v6",
				IPFamily: "IPv6",
				Network:  meridio2v1alpha1.NetworkIdentity{Subnet: "fd00::/64", InterfaceHint: "net1v6"},
				VIPs:     []string{"2001:db8::1"},
				NextHops: []string{"fd00::1"},
			},
		},
	})
	c, fakeClient := setupController(nl, enc)

	result, err := c.Reconcile(context.Background(), reconcileRequest())

	assert.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)

	// Status should be Ready
	fetched := getENCStatus(t, fakeClient)
	assert.Equal(t, metav1.ConditionTrue, fetched.Status.Conditions[0].Status)

	// VIPs should be managed
	assert.Contains(t, c.managedVIPs["net1"], "20.0.0.1")
	assert.Contains(t, c.managedVIPs["net1v6"], "2001:db8::1")

	// Table ID allocated
	tableID, ok := c.tableIDs.lookup("gw-a")
	assert.True(t, ok)
	assert.Equal(t, 50000, tableID)

	// Rules should exist
	rules := nl.rulesForTable(50000)
	assert.Len(t, rules, 2) // one IPv4, one IPv6

	// Routes should exist for both families
	assert.Contains(t, nl.routes, routeKey{table: 50000, family: netlink.FAMILY_V4})
	assert.Contains(t, nl.routes, routeKey{table: 50000, family: netlink.FAMILY_V6})
}

func TestReconcile_MultipleGateways(t *testing.T) {
	nl := newMockNetlink()
	nl.addLink("net1", 10, "192.168.1.5/24")
	nl.addLink("net2", 11, "10.0.0.5/24")

	enc := newENC(
		meridio2v1alpha1.GatewayConnection{
			Name: "gw-a",
			Domains: []meridio2v1alpha1.NetworkDomain{
				{
					Name:     "v4",
					IPFamily: "IPv4",
					Network:  meridio2v1alpha1.NetworkIdentity{Subnet: "192.168.1.0/24", InterfaceHint: "net1"},
					VIPs:     []string{"20.0.0.1"},
					NextHops: []string{"192.168.1.1"},
				},
			},
		},
		meridio2v1alpha1.GatewayConnection{
			Name: "gw-b",
			Domains: []meridio2v1alpha1.NetworkDomain{
				{
					Name:     "v4",
					IPFamily: "IPv4",
					Network:  meridio2v1alpha1.NetworkIdentity{Subnet: "10.0.0.0/24", InterfaceHint: "net2"},
					VIPs:     []string{"30.0.0.1"},
					NextHops: []string{"10.0.0.1"},
				},
			},
		},
	)
	c, _ := setupController(nl, enc)

	_, err := c.Reconcile(context.Background(), reconcileRequest())
	assert.NoError(t, err)

	// Each gateway gets its own table ID
	idA, _ := c.tableIDs.lookup("gw-a")
	idB, _ := c.tableIDs.lookup("gw-b")
	assert.NotEqual(t, idA, idB)

	// Each has rules
	assert.Len(t, nl.rulesForTable(idA), 1)
	assert.Len(t, nl.rulesForTable(idB), 1)

	// Each has a v4 route in its own table
	routeA, okA := nl.routes[routeKey{table: idA, family: netlink.FAMILY_V4}]
	routeB, okB := nl.routes[routeKey{table: idB, family: netlink.FAMILY_V4}]
	assert.True(t, okA, "gw-a should have a v4 route")
	assert.True(t, okB, "gw-b should have a v4 route")

	// Verify next-hops point to the correct gateways
	assert.Len(t, routeA.MultiPath, 1)
	assert.Equal(t, "192.168.1.1", routeA.MultiPath[0].Gw.String())
	assert.Len(t, routeB.MultiPath, 1)
	assert.Equal(t, "10.0.0.1", routeB.MultiPath[0].Gw.String())

	// No v6 routes (only v4 domains configured)
	assert.NotContains(t, nl.routes, routeKey{table: idA, family: netlink.FAMILY_V6})
	assert.NotContains(t, nl.routes, routeKey{table: idB, family: netlink.FAMILY_V6})
}

func TestReconcile_InvalidVIP_SetsStatusFailed_NoRequeue(t *testing.T) {
	nl := newMockNetlink()
	nl.addLink("net1", 10, "192.168.1.5/24")

	enc := newENC(meridio2v1alpha1.GatewayConnection{
		Name: "gw-a",
		Domains: []meridio2v1alpha1.NetworkDomain{
			{
				Name:     "v4",
				IPFamily: "IPv4",
				Network:  meridio2v1alpha1.NetworkIdentity{Subnet: "192.168.1.0/24", InterfaceHint: "net1"},
				VIPs:     []string{"not-an-ip"},
			},
		},
	})
	c, fakeClient := setupController(nl, enc)

	result, err := c.Reconcile(context.Background(), reconcileRequest())

	// buildDesiredState error → no requeue
	assert.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)

	fetched := getENCStatus(t, fakeClient)
	assert.Equal(t, metav1.ConditionFalse, fetched.Status.Conditions[0].Status)
	assert.Equal(t, "ConfigurationFailed", fetched.Status.Conditions[0].Reason)
	assert.Contains(t, fetched.Status.Conditions[0].Message, "invalid VIP")
}

func TestReconcile_InvalidNextHop_SetsStatusFailed(t *testing.T) {
	nl := newMockNetlink()
	nl.addLink("net1", 10, "192.168.1.5/24")

	enc := newENC(meridio2v1alpha1.GatewayConnection{
		Name: "gw-a",
		Domains: []meridio2v1alpha1.NetworkDomain{
			{
				Name:     "v4",
				IPFamily: "IPv4",
				Network:  meridio2v1alpha1.NetworkIdentity{Subnet: "192.168.1.0/24", InterfaceHint: "net1"},
				VIPs:     []string{"20.0.0.1"},
				NextHops: []string{"bad-hop"},
			},
		},
	})
	c, fakeClient := setupController(nl, enc)

	result, err := c.Reconcile(context.Background(), reconcileRequest())

	assert.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)

	fetched := getENCStatus(t, fakeClient)
	assert.Equal(t, metav1.ConditionFalse, fetched.Status.Conditions[0].Status)
	assert.Contains(t, fetched.Status.Conditions[0].Message, "invalid next-hop")
}

func TestReconcile_InterfaceNotFound_SetsStatusFailed_Requeues(t *testing.T) {
	nl := newMockNetlink() // no interfaces

	enc := newENC(meridio2v1alpha1.GatewayConnection{
		Name: "gw-a",
		Domains: []meridio2v1alpha1.NetworkDomain{
			{
				Name:     "v4",
				IPFamily: "IPv4",
				Network:  meridio2v1alpha1.NetworkIdentity{Subnet: "192.168.1.0/24"},
			},
		},
	})
	c, fakeClient := setupController(nl, enc)

	result, err := c.Reconcile(context.Background(), reconcileRequest())

	// InterfaceNotFoundError is transient — requeue
	assert.Error(t, err)
	assert.Equal(t, ctrl.Result{}, result)

	fetched := getENCStatus(t, fakeClient)
	assert.Equal(t, metav1.ConditionFalse, fetched.Status.Conditions[0].Status)
	assert.Contains(t, fetched.Status.Conditions[0].Message, "no interface found")
}

func TestReconcile_ApplyStateError_Requeues(t *testing.T) {
	nl := newMockNetlink()
	nl.addLink("net1", 10, "192.168.1.5/24")
	nl.addrAddErr = fmt.Errorf("operation not permitted")

	enc := newENC(meridio2v1alpha1.GatewayConnection{
		Name: "gw-a",
		Domains: []meridio2v1alpha1.NetworkDomain{
			{
				Name:     "v4",
				IPFamily: "IPv4",
				Network:  meridio2v1alpha1.NetworkIdentity{Subnet: "192.168.1.0/24", InterfaceHint: "net1"},
				VIPs:     []string{"20.0.0.1"},
			},
		},
	})
	c, fakeClient := setupController(nl, enc)

	result, err := c.Reconcile(context.Background(), reconcileRequest())

	// applyState error → requeue
	assert.Error(t, err)
	assert.Equal(t, ctrl.Result{}, result)

	fetched := getENCStatus(t, fakeClient)
	assert.Equal(t, metav1.ConditionFalse, fetched.Status.Conditions[0].Status)
	assert.Contains(t, fetched.Status.Conditions[0].Message, "operation not permitted")
}

func TestReconcile_StaleGatewayCleanup(t *testing.T) {
	nl := newMockNetlink()
	nl.addLink("net1", 10, "192.168.1.5/24")

	enc := newENC(
		meridio2v1alpha1.GatewayConnection{
			Name: "gw-a",
			Domains: []meridio2v1alpha1.NetworkDomain{
				{
					Name:    "v4",
					Network: meridio2v1alpha1.NetworkIdentity{Subnet: "192.168.1.0/24", InterfaceHint: "net1"},
					VIPs:    []string{"20.0.0.1"},
				},
			},
		},
		meridio2v1alpha1.GatewayConnection{
			Name: "gw-b",
			Domains: []meridio2v1alpha1.NetworkDomain{
				{
					Name:    "v4",
					Network: meridio2v1alpha1.NetworkIdentity{Subnet: "192.168.1.0/24", InterfaceHint: "net1"},
					VIPs:    []string{"20.0.0.2"},
				},
			},
		},
	)
	c, fakeClient := setupController(nl, enc)

	_, err := c.Reconcile(context.Background(), reconcileRequest())
	assert.NoError(t, err)
	assert.Len(t, c.tableIDs.activeGateways(), 2)

	// Remove gw-b — re-fetch to get current resourceVersion after status update
	var updated meridio2v1alpha1.EndpointNetworkConfiguration
	err = fakeClient.Get(context.Background(), reconcileRequest().NamespacedName, &updated)
	assert.NoError(t, err)
	updated.Spec.Gateways = updated.Spec.Gateways[:1]
	err = fakeClient.Update(context.Background(), &updated)
	assert.NoError(t, err)

	_, err = c.Reconcile(context.Background(), reconcileRequest())
	assert.NoError(t, err)

	assert.Len(t, c.tableIDs.activeGateways(), 1)
	_, ok := c.tableIDs.lookup("gw-a")
	assert.True(t, ok)
	_, ok = c.tableIDs.lookup("gw-b")
	assert.False(t, ok)
}

func TestReconcile_DomainRemoved_CleansUpRules(t *testing.T) {
	nl := newMockNetlink()
	nl.addLink("net1", 10, "192.168.1.5/24")
	nl.addLink("net1v6", 11, "fd00::5/64")

	enc := newENC(meridio2v1alpha1.GatewayConnection{
		Name: "gw-a",
		Domains: []meridio2v1alpha1.NetworkDomain{
			{
				Name:    "v4",
				Network: meridio2v1alpha1.NetworkIdentity{Subnet: "192.168.1.0/24", InterfaceHint: "net1"},
				VIPs:    []string{"20.0.0.1"},
			},
			{
				Name:    "v6",
				Network: meridio2v1alpha1.NetworkIdentity{Subnet: "fd00::/64", InterfaceHint: "net1v6"},
				VIPs:    []string{"2001:db8::1"},
			},
		},
	})
	c, fakeClient := setupController(nl, enc)

	_, err := c.Reconcile(context.Background(), reconcileRequest())
	assert.NoError(t, err)

	tableID, _ := c.tableIDs.lookup("gw-a")
	assert.Len(t, nl.rulesForTable(tableID), 2)

	// Remove v6 domain — re-fetch to get current resourceVersion after status update
	var updated meridio2v1alpha1.EndpointNetworkConfiguration
	err = fakeClient.Get(context.Background(), reconcileRequest().NamespacedName, &updated)
	assert.NoError(t, err)
	updated.Spec.Gateways[0].Domains = updated.Spec.Gateways[0].Domains[:1]
	err = fakeClient.Update(context.Background(), &updated)
	assert.NoError(t, err)

	_, err = c.Reconcile(context.Background(), reconcileRequest())
	assert.NoError(t, err)

	rules := nl.rulesForTable(tableID)
	assert.Len(t, rules, 1)
	assert.True(t, rules[0].Src.IP.To4() != nil, "remaining rule should be IPv4")
	assert.NotContains(t, c.managedVIPs["net1v6"], "2001:db8::1")
}

func TestReconcile_MultipleVIPs_SameInterface(t *testing.T) {
	nl := newMockNetlink()
	nl.addLink("net1", 10, "192.168.1.5/24")

	enc := newENC(meridio2v1alpha1.GatewayConnection{
		Name: "gw-a",
		Domains: []meridio2v1alpha1.NetworkDomain{{
			Name:     "v4",
			IPFamily: "IPv4",
			Network:  meridio2v1alpha1.NetworkIdentity{Subnet: "192.168.1.0/24", InterfaceHint: "net1"},
			VIPs:     []string{"20.0.0.1", "20.0.0.2", "20.0.0.3"},
			NextHops: []string{"192.168.1.1"},
		}},
	})
	c, _ := setupController(nl, enc)

	_, err := c.Reconcile(context.Background(), reconcileRequest())
	assert.NoError(t, err)

	assert.Len(t, c.managedVIPs["net1"], 3)
	assert.Len(t, nl.rulesForTable(50000), 3)
}

func TestReconcile_TwoGateways_SharedInterface(t *testing.T) {
	nl := newMockNetlink()
	nl.addLink("net1", 10, "192.168.1.5/24")

	enc := newENC(
		meridio2v1alpha1.GatewayConnection{
			Name: "gw-a",
			Domains: []meridio2v1alpha1.NetworkDomain{{
				Name:    "v4",
				Network: meridio2v1alpha1.NetworkIdentity{Subnet: "192.168.1.0/24", InterfaceHint: "net1"},
				VIPs:    []string{"20.0.0.1"},
			}},
		},
		meridio2v1alpha1.GatewayConnection{
			Name: "gw-b",
			Domains: []meridio2v1alpha1.NetworkDomain{{
				Name:    "v4",
				Network: meridio2v1alpha1.NetworkIdentity{Subnet: "192.168.1.0/24", InterfaceHint: "net1"},
				VIPs:    []string{"30.0.0.1"},
			}},
		},
	)
	c, _ := setupController(nl, enc)

	_, err := c.Reconcile(context.Background(), reconcileRequest())
	assert.NoError(t, err)

	// Both VIPs on same interface
	assert.Contains(t, c.managedVIPs["net1"], "20.0.0.1")
	assert.Contains(t, c.managedVIPs["net1"], "30.0.0.1")

	// Different tables
	idA, _ := c.tableIDs.lookup("gw-a")
	idB, _ := c.tableIDs.lookup("gw-b")
	assert.NotEqual(t, idA, idB)
	assert.Len(t, nl.rulesForTable(idA), 1)
	assert.Len(t, nl.rulesForTable(idB), 1)
}

func TestReconcile_NextHopsRemoved_RoutesDeleted_RulesKept(t *testing.T) {
	nl := newMockNetlink()
	nl.addLink("net1", 10, "192.168.1.5/24")

	enc := newENC(meridio2v1alpha1.GatewayConnection{
		Name: "gw-a",
		Domains: []meridio2v1alpha1.NetworkDomain{{
			Name:     "v4",
			IPFamily: "IPv4",
			Network:  meridio2v1alpha1.NetworkIdentity{Subnet: "192.168.1.0/24", InterfaceHint: "net1"},
			VIPs:     []string{"20.0.0.1"},
			NextHops: []string{"192.168.1.1"},
		}},
	})
	c, fakeClient := setupController(nl, enc)

	_, err := c.Reconcile(context.Background(), reconcileRequest())
	assert.NoError(t, err)
	assert.Contains(t, nl.routes, routeKey{table: 50000, family: netlink.FAMILY_V4})
	assert.Len(t, nl.rulesForTable(50000), 1)

	// Remove next-hops
	var updated meridio2v1alpha1.EndpointNetworkConfiguration
	_ = fakeClient.Get(context.Background(), reconcileRequest().NamespacedName, &updated)
	updated.Spec.Gateways[0].Domains[0].NextHops = nil
	_ = fakeClient.Update(context.Background(), &updated)

	_, err = c.Reconcile(context.Background(), reconcileRequest())
	assert.NoError(t, err)

	// Routes gone, rules and VIPs remain
	assert.NotContains(t, nl.routes, routeKey{table: 50000, family: netlink.FAMILY_V4})
	assert.Len(t, nl.rulesForTable(50000), 1)
	assert.Contains(t, c.managedVIPs["net1"], "20.0.0.1")
}

func TestReconcile_ECMP_MultipleNextHops(t *testing.T) {
	nl := newMockNetlink()
	nl.addLink("net1", 10, "192.168.1.5/24")

	enc := newENC(meridio2v1alpha1.GatewayConnection{
		Name: "gw-a",
		Domains: []meridio2v1alpha1.NetworkDomain{{
			Name:     "v4",
			IPFamily: "IPv4",
			Network:  meridio2v1alpha1.NetworkIdentity{Subnet: "192.168.1.0/24", InterfaceHint: "net1"},
			VIPs:     []string{"20.0.0.1"},
			NextHops: []string{"192.168.1.1", "192.168.1.2", "192.168.1.3"},
		}},
	})
	c, _ := setupController(nl, enc)

	_, err := c.Reconcile(context.Background(), reconcileRequest())
	assert.NoError(t, err)

	route := nl.routes[routeKey{table: 50000, family: netlink.FAMILY_V4}]
	assert.Len(t, route.MultiPath, 3)
	assert.Equal(t, "192.168.1.1", route.MultiPath[0].Gw.String())
	assert.Equal(t, "192.168.1.2", route.MultiPath[1].Gw.String())
	assert.Equal(t, "192.168.1.3", route.MultiPath[2].Gw.String())
}

// --- updateStatus tests ---

func TestUpdateStatus_Success(t *testing.T) {
	enc := newENC()
	c, fakeClient := setupController(newMockNetlink(), enc)

	err := c.updateStatus(context.Background(), enc, nil)
	assert.NoError(t, err)

	fetched := getENCStatus(t, fakeClient)
	assert.Equal(t, metav1.ConditionTrue, fetched.Status.Conditions[0].Status)
	assert.Equal(t, "Configured", fetched.Status.Conditions[0].Reason)
}

func TestUpdateStatus_Error(t *testing.T) {
	enc := newENC()
	c, fakeClient := setupController(newMockNetlink(), enc)

	err := c.updateStatus(context.Background(), enc, fmt.Errorf("something broke"))
	assert.NoError(t, err)

	fetched := getENCStatus(t, fakeClient)
	assert.Equal(t, metav1.ConditionFalse, fetched.Status.Conditions[0].Status)
	assert.Equal(t, "ConfigurationFailed", fetched.Status.Conditions[0].Reason)
	assert.Contains(t, fetched.Status.Conditions[0].Message, "something broke")
}

func TestUpdateStatus_ObservedGeneration(t *testing.T) {
	enc := newENC()
	enc.Generation = 42
	c, fakeClient := setupController(newMockNetlink(), enc)

	_ = c.updateStatus(context.Background(), enc, nil)

	fetched := getENCStatus(t, fakeClient)
	assert.Equal(t, int64(42), fetched.Status.Conditions[0].ObservedGeneration)
}

// --- network function tests ---

func TestFindInterfaceBySubnet_Hint(t *testing.T) {
	nl := newMockNetlink()
	nl.addLink("net1", 10, "192.168.1.5/24")

	link, err := findInterfaceBySubnet(nl, "net1", "192.168.1.0/24")
	assert.NoError(t, err)
	assert.Equal(t, "net1", link.Attrs().Name)
}

func TestFindInterfaceBySubnet_Scan(t *testing.T) {
	nl := newMockNetlink()
	nl.addLink("eth0", 1, "10.0.0.5/24")
	nl.addLink("net1", 10, "192.168.1.5/24")

	link, err := findInterfaceBySubnet(nl, "", "192.168.1.0/24")
	assert.NoError(t, err)
	assert.Equal(t, "net1", link.Attrs().Name)
}

func TestFindInterfaceBySubnet_NotFound(t *testing.T) {
	nl := newMockNetlink()
	nl.addLink("eth0", 1, "10.0.0.5/24")

	_, err := findInterfaceBySubnet(nl, "", "192.168.1.0/24")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no interface found")
}

func TestFindInterfaceBySubnet_InvalidSubnet(t *testing.T) {
	nl := newMockNetlink()
	_, err := findInterfaceBySubnet(nl, "", "not-a-cidr")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid subnet")
}

func TestSyncVIPs_AddAndRemove(t *testing.T) {
	nl := newMockNetlink()
	nl.addLink("net1", 10, "192.168.1.5/24")
	link, _ := nl.LinkByName("net1")

	managed, err := syncVIPs(nl, link, []net.IP{net.ParseIP("20.0.0.1"), net.ParseIP("20.0.0.2")}, nil)
	assert.NoError(t, err)
	assert.Len(t, managed, 2)

	managed, err = syncVIPs(nl, link, []net.IP{net.ParseIP("20.0.0.1")}, managed)
	assert.NoError(t, err)
	assert.Len(t, managed, 1)
	assert.Contains(t, managed, "20.0.0.1")
}

func TestSyncVIPs_EEXIST_Tolerated(t *testing.T) {
	nl := newMockNetlink()
	nl.addLink("net1", 10, "192.168.1.5/24", "20.0.0.1/32")
	link, _ := nl.LinkByName("net1")

	// Simulate restart: managedVIPs is empty but VIP already exists on interface
	nl.addrAddErr = syscall.EEXIST
	managed, err := syncVIPs(nl, link, []net.IP{net.ParseIP("20.0.0.1")}, nil)
	assert.NoError(t, err)
	assert.Contains(t, managed, "20.0.0.1")
}

func TestSyncVIPs_PartialFailure_ReturnsCurrentState(t *testing.T) {
	nl := newMockNetlink()
	nl.addLink("net1", 10, "192.168.1.5/24")
	link, _ := nl.LinkByName("net1")

	managed, err := syncVIPs(nl, link, []net.IP{net.ParseIP("20.0.0.1")}, nil)
	assert.NoError(t, err)

	nl.addrAddErr = fmt.Errorf("ENOMEM")
	managed, err = syncVIPs(nl, link, []net.IP{net.ParseIP("20.0.0.1"), net.ParseIP("20.0.0.2")}, managed)
	assert.Error(t, err)
	assert.Contains(t, managed, "20.0.0.1")
	assert.NotContains(t, managed, "20.0.0.2")
}

func TestSyncRules_AddsAndRemovesRules(t *testing.T) {
	nl := newMockNetlink()

	vips := []net.IP{net.ParseIP("20.0.0.1"), net.ParseIP("20.0.0.2")}
	err := syncRules(context.Background(), nl, vips, 50000, 50000, 55000)
	assert.NoError(t, err)
	assert.Len(t, nl.rulesForTable(50000), 2)

	err = syncRules(context.Background(), nl, []net.IP{net.ParseIP("20.0.0.1")}, 50000, 50000, 55000)
	assert.NoError(t, err)
	assert.Len(t, nl.rulesForTable(50000), 1)
}

func TestSyncRoutes_AddsRoute(t *testing.T) {
	nl := newMockNetlink()

	err := syncRoutes(context.Background(), nl, []net.IP{net.ParseIP("192.168.1.1")}, 50000)
	assert.NoError(t, err)
	assert.Contains(t, nl.routes, routeKey{table: 50000, family: netlink.FAMILY_V4})
}

func TestSyncRoutes_EmptyHops_DeletesRoutes(t *testing.T) {
	nl := newMockNetlink()

	_ = syncRoutes(context.Background(), nl, []net.IP{net.ParseIP("192.168.1.1")}, 50000)
	assert.Len(t, nl.routes, 1)

	err := syncRoutes(context.Background(), nl, nil, 50000)
	assert.NoError(t, err)
	assert.Empty(t, nl.routes)
}

func TestFlushTable(t *testing.T) {
	nl := newMockNetlink()

	_ = syncRules(context.Background(), nl, []net.IP{net.ParseIP("20.0.0.1")}, 50000, 50000, 55000)
	_ = syncRoutes(context.Background(), nl, []net.IP{net.ParseIP("192.168.1.1")}, 50000)

	assert.NotEmpty(t, nl.rulesForTable(50000))
	assert.NotEmpty(t, nl.routes)

	flushTable(context.Background(), nl, 50000, 50000, 55000)

	assert.Empty(t, nl.rulesForTable(50000))
	assert.Empty(t, nl.routes)
}

func TestFlushTable_OutOfRange_NoOp(t *testing.T) {
	nl := newMockNetlink()
	_ = syncRules(context.Background(), nl, []net.IP{net.ParseIP("20.0.0.1")}, 99999, 99999, 99999)

	flushTable(context.Background(), nl, 99999, 50000, 55000)

	assert.NotEmpty(t, nl.rules)
}

func TestVipToIPNet(t *testing.T) {
	assert.Equal(t, "20.0.0.1/32", vipToIPNet(net.ParseIP("20.0.0.1")).String())
	assert.Equal(t, "2001:db8::1/128", vipToIPNet(net.ParseIP("2001:db8::1")).String())
}
