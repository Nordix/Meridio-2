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
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	meridio2v1alpha1 "github.com/nordix/meridio-2/api/v1alpha1"
)

const (
	testControllerName = "test-controller"
	testSubnetV4       = "169.111.100.0/24"
	testSubnetV6       = "fd00::100:0/120"
	testNextHopV4      = "169.111.100.3"
	testNextHopV6      = "fd00::100:3"
	testIPFamilyV4     = "IPv4"
)

func acceptedGateway(name, controllerName string, addresses ...string) *gatewayv1.Gateway {
	gw := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace},
		Spec: gatewayv1.GatewaySpec{
			GatewayClassName: "test-class",
			Infrastructure: &gatewayv1.GatewayInfrastructure{
				ParametersRef: &gatewayv1.LocalParametersReference{
					Group: gatewayv1.Group(meridio2v1alpha1.GroupVersion.Group),
					Kind:  "GatewayConfiguration",
					Name:  name + "-config",
				},
			},
		},
		Status: gatewayv1.GatewayStatus{
			Conditions: []metav1.Condition{
				{
					Type:    string(gatewayv1.GatewayConditionAccepted),
					Status:  metav1.ConditionTrue,
					Message: "Accepted by " + controllerName,
				},
			},
		},
	}
	for _, addr := range addresses {
		gw.Status.Addresses = append(gw.Status.Addresses, gatewayv1.GatewayStatusAddress{
			Type:  ptr(gatewayv1.IPAddressType),
			Value: addr,
		})
	}
	return gw
}

func newDG(name string, selector map[string]string, parentGateway string) *meridio2v1alpha1.DistributionGroup {
	dg := &meridio2v1alpha1.DistributionGroup{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace},
		Spec: meridio2v1alpha1.DistributionGroupSpec{
			Selector: &metav1.LabelSelector{MatchLabels: selector},
		},
	}
	if parentGateway != "" {
		dg.Spec.ParentRefs = []meridio2v1alpha1.ParentReference{
			{Name: parentGateway},
		}
	}
	return dg
}

func newGatewayConfig(cidrs []string) *meridio2v1alpha1.GatewayConfiguration {
	subnets := make([]meridio2v1alpha1.InternalSubnet, len(cidrs))
	for i, cidr := range cidrs {
		subnets[i] = meridio2v1alpha1.InternalSubnet{AttachmentType: "NAD", CIDR: cidr}
	}
	gc := &meridio2v1alpha1.GatewayConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: "sllb-a-config", Namespace: testNamespace},
		Spec: meridio2v1alpha1.GatewayConfigurationSpec{
			NetworkAttachments: []meridio2v1alpha1.NetworkAttachment{
				{Type: "NAD", NAD: &meridio2v1alpha1.NAD{Interface: "net1", Name: "nad-1", Namespace: testNamespace}},
			},
			InternalSubnets:   subnets,
			HorizontalScaling: meridio2v1alpha1.HorizontalScaling{Replicas: 1},
		},
	}
	return gc
}

func newL34Route(name, ns, gatewayName, dgName string) *meridio2v1alpha1.L34Route {
	return &meridio2v1alpha1.L34Route{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: meridio2v1alpha1.L34RouteSpec{
			ParentRefs: []gatewayv1.ParentReference{
				{Name: gatewayv1.ObjectName(gatewayName)},
			},
			BackendRefs: []gatewayv1.BackendRef{
				{
					BackendObjectReference: gatewayv1.BackendObjectReference{
						Group: ptr(gatewayv1.Group(meridio2v1alpha1.GroupVersion.Group)),
						Kind:  ptr(gatewayv1.Kind("DistributionGroup")),
						Name:  gatewayv1.ObjectName(dgName),
					},
				},
			},
			DestinationCIDRs: []string{"20.0.0.1/32"},
			Protocols:        []meridio2v1alpha1.TransportProtocol{meridio2v1alpha1.TCP},
			Priority:         1,
		},
	}
}

func ptr[T any](v T) *T { return &v }

// --- listMatchingDGs tests ---

func TestListMatchingDGs_SelectorMatch(t *testing.T) {
	pod := newPod("app-1", corev1.PodRunning, map[string]string{"app": "web"})
	dg := newDG("dg-1", map[string]string{"app": "web"}, "")
	dgNoMatch := newDG("dg-2", map[string]string{"app": "api"}, "")
	r, _ := setupReconciler(pod, dg, dgNoMatch)

	result, err := r.listMatchingDGs(context.Background(), pod)
	require.NoError(t, err)
	assert.Len(t, result, 1)
	assert.Equal(t, "dg-1", result[0].Name)
}

func TestListMatchingDGs_NoMatch(t *testing.T) {
	pod := newPod("app-1", corev1.PodRunning, map[string]string{"app": "web"})
	dg := newDG("dg-1", map[string]string{"app": "api"}, "")
	r, _ := setupReconciler(pod, dg)

	result, err := r.listMatchingDGs(context.Background(), pod)
	require.NoError(t, err)
	assert.Empty(t, result)
}

func TestListMatchingDGs_NilSelector(t *testing.T) {
	pod := newPod("app-1", corev1.PodRunning, map[string]string{"app": "web"})
	dg := &meridio2v1alpha1.DistributionGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "dg-all", Namespace: testNamespace},
		Spec:       meridio2v1alpha1.DistributionGroupSpec{},
	}
	r, _ := setupReconciler(pod, dg)

	result, err := r.listMatchingDGs(context.Background(), pod)
	require.NoError(t, err)
	assert.Empty(t, result, "nil selector should match no Pods (DG controller convention)")
}

// --- resolveGatewaysForDG tests ---

func TestResolveGatewaysForDG_DirectParentRef(t *testing.T) {
	gw := acceptedGateway("sllb-a", testControllerName)
	dg := newDG("dg-1", map[string]string{"app": "web"}, "sllb-a")
	r, _ := setupReconciler(gw, dg)

	result, err := r.resolveGatewaysForDG(context.Background(), dg)
	require.NoError(t, err)
	assert.Len(t, result, 1)
	assert.Equal(t, "sllb-a", result[0].Name)
}

func TestResolveGatewaysForDG_IndirectViaL34Route(t *testing.T) {
	gw := acceptedGateway("sllb-a", testControllerName)
	dg := newDG("dg-1", map[string]string{"app": "web"}, "")
	route := newL34Route("route-1", testNamespace, "sllb-a", "dg-1")
	r, _ := setupReconciler(gw, dg, route)

	result, err := r.resolveGatewaysForDG(context.Background(), dg)
	require.NoError(t, err)
	assert.Len(t, result, 1)
	assert.Equal(t, "sllb-a", result[0].Name)
}

func TestResolveGatewaysForDG_Deduplication(t *testing.T) {
	gw := acceptedGateway("sllb-a", testControllerName)
	dg := newDG("dg-1", map[string]string{"app": "web"}, "sllb-a")
	route := newL34Route("route-1", testNamespace, "sllb-a", "dg-1")
	r, _ := setupReconciler(gw, dg, route)

	result, err := r.resolveGatewaysForDG(context.Background(), dg)
	require.NoError(t, err)
	assert.Len(t, result, 1, "same Gateway found via both paths should appear once")
}

func TestResolveGatewaysForDG_NotAccepted(t *testing.T) {
	gw := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "sllb-a", Namespace: testNamespace},
		Spec:       gatewayv1.GatewaySpec{GatewayClassName: "test-class"},
		// No Accepted condition
	}
	dg := newDG("dg-1", map[string]string{"app": "web"}, "sllb-a")
	r, _ := setupReconciler(gw, dg)

	result, err := r.resolveGatewaysForDG(context.Background(), dg)
	require.NoError(t, err)
	assert.Empty(t, result, "Gateway without Accepted=True should be filtered out")
}

// --- extractVIPs tests ---

func TestExtractVIPs_DualStack(t *testing.T) {
	gw := acceptedGateway("sllb-a", testControllerName, "20.0.0.1", "2001:db8::1")
	ipv4, ipv6 := extractVIPs(gw)
	assert.Equal(t, []string{"20.0.0.1"}, ipv4)
	assert.Equal(t, []string{"2001:db8::1"}, ipv6)
}

func TestExtractVIPs_IPv4Only(t *testing.T) {
	gw := acceptedGateway("sllb-a", testControllerName, "20.0.0.1", "20.0.0.2")
	ipv4, ipv6 := extractVIPs(gw)
	assert.Equal(t, []string{"20.0.0.1", "20.0.0.2"}, ipv4)
	assert.Nil(t, ipv6)
}

func TestExtractVIPs_Empty(t *testing.T) {
	gw := acceptedGateway("sllb-a", testControllerName)
	ipv4, ipv6 := extractVIPs(gw)
	assert.Nil(t, ipv4)
	assert.Nil(t, ipv6)
}

func TestExtractVIPs_PlainIPNotCIDR(t *testing.T) {
	gw := acceptedGateway("sllb-a", testControllerName, "20.0.0.1")
	ipv4, _ := extractVIPs(gw)
	require.Len(t, ipv4, 1)
	assert.Equal(t, "20.0.0.1", ipv4[0], "must be plain IP, not CIDR")
	assert.NotContains(t, ipv4[0], "/")
}

func TestExtractVIPs_Deduplication(t *testing.T) {
	gw := acceptedGateway("sllb-a", testControllerName, "20.0.0.1", "20.0.0.1")
	ipv4, _ := extractVIPs(gw)
	assert.Len(t, ipv4, 1)
}

// --- getNetworkContexts tests ---

func TestGetNetworkContexts(t *testing.T) {
	gw := acceptedGateway("sllb-a", testControllerName)
	gc := newGatewayConfig([]string{testSubnetV4, "fd00:100::/64"})
	r, _ := setupReconciler(gw, gc)

	subnetToType, err := r.getNetworkContexts(context.Background(), gw)
	require.NoError(t, err)
	assert.Equal(t, "NAD", subnetToType[testSubnetV4])
	assert.Equal(t, "NAD", subnetToType["fd00:100::/64"])
}

func TestGetNetworkContexts_NoParametersRef(t *testing.T) {
	gw := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "sllb-a", Namespace: testNamespace},
		Spec:       gatewayv1.GatewaySpec{GatewayClassName: "test-class"},
	}
	r, _ := setupReconciler(gw)

	subnetToType, err := r.getNetworkContexts(context.Background(), gw)
	require.NoError(t, err)
	assert.Empty(t, subnetToType)
}

// --- getSLLBRNextHops tests ---

func TestGetSLLBRNextHops(t *testing.T) {
	gw := acceptedGateway("sllb-a", testControllerName)
	sllbrPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sllbr-sllb-a-abc",
			Namespace: testNamespace,
			Labels:    map[string]string{labelGatewayName: "sllb-a"},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}

	r, _ := setupReconciler(gw, sllbrPod)
	r.IPScraper = func(pod *corev1.Pod, cidr, attachmentType string) string {
		if pod.Name == "sllbr-sllb-a-abc" && cidr == testSubnetV4 {
			return testNextHopV4
		}
		return ""
	}

	ipv4, ipv6, err := r.getSLLBRNextHops(context.Background(), gw, map[string]string{testSubnetV4: "NAD"})
	require.NoError(t, err)
	assert.Equal(t, []string{testNextHopV4}, ipv4)
	assert.Nil(t, ipv6)
}

// --- buildGatewayConnection tests ---

func TestBuildGatewayConnection_SkipsDomainWithNoInterface(t *testing.T) {
	// Gateway has both IPv4 and IPv6 subnets, but pod only has IPv4 interface
	gw := acceptedGateway("sllb-a", testControllerName, "20.0.0.1", "2001:db8::1")
	gc := newGatewayConfig([]string{testSubnetV4, "fd00:100::/64"})
	sllbrPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sllbr-sllb-a-abc",
			Namespace: testNamespace,
			Labels:    map[string]string{labelGatewayName: "sllb-a"},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	// Pod only has IPv4 address on net1 — no IPv6
	targetPod := newPod("app-1", corev1.PodRunning, map[string]string{"app": "web"})
	targetPod.Annotations = map[string]string{
		"k8s.v1.cni.cncf.io/network-status": `[
			{"name":"default","interface":"eth0","ips":["10.244.0.5"],"default":true},
			{"name":"net1","interface":"net1","ips":["169.111.100.10"]}
		]`,
	}

	r, _ := setupReconciler(gw, gc, sllbrPod, targetPod)
	r.IPScraper = func(pod *corev1.Pod, cidr, _ string) string {
		if cidr == testSubnetV4 {
			return testNextHopV4
		}
		if cidr == "fd00:100::/64" {
			return "fd00:100::3"
		}
		return ""
	}

	conn, err := r.buildGatewayConnection(context.Background(), targetPod, gw)
	require.NoError(t, err)
	require.NotNil(t, conn)

	// Only IPv4 domain should be present — IPv6 skipped (no interface)
	assert.Len(t, conn.Domains, 1)
	assert.Equal(t, "IPv4", conn.Domains[0].IPFamily)
	assert.Equal(t, "net1", conn.Domains[0].Network.InterfaceHint)
}

func TestBuildGatewayConnection_NamingConvention(t *testing.T) {
	gw := acceptedGateway("sllb-a", testControllerName, "20.0.0.1", "2001:db8::1")
	gc := newGatewayConfig([]string{testSubnetV4, "fd00:100::/64"})
	sllbrPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sllbr-sllb-a-abc",
			Namespace: testNamespace,
			Labels:    map[string]string{labelGatewayName: "sllb-a"},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	// Target pod with Multus network-status annotation
	targetPod := newPod("app-1", corev1.PodRunning, map[string]string{"app": "web"})
	targetPod.Annotations = map[string]string{
		"k8s.v1.cni.cncf.io/network-status": `[
			{"name":"default","interface":"eth0","ips":["10.244.0.5"],"default":true},
			{"name":"net1","interface":"net1","ips":["169.111.100.10","fd00:100::a"]}
		]`,
	}

	r, _ := setupReconciler(gw, gc, sllbrPod, targetPod)
	r.IPScraper = func(pod *corev1.Pod, cidr, _ string) string {
		switch cidr {
		case testSubnetV4:
			return testNextHopV4
		case "fd00:100::/64":
			return "fd00:100::3"
		}
		return ""
	}

	conn, err := r.buildGatewayConnection(context.Background(), targetPod, gw)
	require.NoError(t, err)
	require.NotNil(t, conn)

	assert.Equal(t, "sllb-a", conn.Name)
	assert.Len(t, conn.Domains, 2)

	// Find domains by IP family
	domainMap := make(map[string]meridio2v1alpha1.NetworkDomain)
	for _, d := range conn.Domains {
		domainMap[d.IPFamily] = d
	}

	v4 := domainMap[testIPFamilyV4]
	assert.Equal(t, "sllb-a-IPv4", v4.Name)
	assert.Equal(t, testSubnetV4, v4.Network.Subnet)
	assert.Equal(t, "net1", v4.Network.InterfaceHint)
	assert.Equal(t, []string{"20.0.0.1"}, v4.VIPs)
	assert.Equal(t, []string{testNextHopV4}, v4.NextHops)

	v6 := domainMap["IPv6"]
	assert.Equal(t, "sllb-a-IPv6", v6.Name)
	assert.Equal(t, "fd00:100::/64", v6.Network.Subnet)
	assert.Equal(t, "net1", v6.Network.InterfaceHint)
	assert.Equal(t, []string{"2001:db8::1"}, v6.VIPs)
	assert.Equal(t, []string{"fd00:100::3"}, v6.NextHops)
}

// --- Full integration: resolveGatewayConnections ---

func TestResolveGatewayConnections_FullChain(t *testing.T) {
	pod := newPod("app-1", corev1.PodRunning, map[string]string{"app": "web"})
	pod.Annotations = map[string]string{
		"k8s.v1.cni.cncf.io/network-status": `[
			{"name":"default","interface":"eth0","ips":["10.244.0.5"],"default":true},
			{"name":"nad-1","interface":"net1","ips":["169.111.100.10"]}
		]`,
	}
	gw := acceptedGateway("sllb-a", testControllerName, "20.0.0.1")
	gc := newGatewayConfig([]string{testSubnetV4})
	dg := newDG("dg-1", map[string]string{"app": "web"}, "sllb-a")
	sllbrPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sllbr-sllb-a-abc",
			Namespace: testNamespace,
			Labels:    map[string]string{labelGatewayName: "sllb-a"},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}

	r, _ := setupReconciler(pod, gw, gc, dg, sllbrPod)
	r.IPScraper = func(pod *corev1.Pod, cidr, _ string) string {
		if cidr == testSubnetV4 {
			return testNextHopV4
		}
		return ""
	}

	connections, err := r.resolveGatewayConnections(context.Background(), pod)
	require.NoError(t, err)
	require.Len(t, connections, 1)

	assert.Equal(t, "sllb-a", connections[0].Name)
	require.Len(t, connections[0].Domains, 1)
	assert.Equal(t, "sllb-a-IPv4", connections[0].Domains[0].Name)
	assert.Equal(t, testIPFamilyV4, connections[0].Domains[0].IPFamily)
	assert.Equal(t, "net1", connections[0].Domains[0].Network.InterfaceHint)
	assert.Equal(t, []string{"20.0.0.1"}, connections[0].Domains[0].VIPs)
	assert.Equal(t, []string{testNextHopV4}, connections[0].Domains[0].NextHops)
}

// --- helper tests ---

func TestIsGatewayAccepted(t *testing.T) {
	r, _ := setupReconciler()

	accepted := acceptedGateway("gw", testControllerName)
	assert.True(t, r.isGatewayAccepted(accepted))

	notAccepted := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "gw", Namespace: testNamespace},
	}
	assert.False(t, r.isGatewayAccepted(notAccepted))

	wrongController := acceptedGateway("gw", "other-controller")
	assert.False(t, r.isGatewayAccepted(wrongController))
}

func TestBackendRefMatchesDG(t *testing.T) {
	dgKey := client.ObjectKey{Name: "dg-1", Namespace: testNamespace}

	matching := gatewayv1.BackendRef{
		BackendObjectReference: gatewayv1.BackendObjectReference{
			Group: ptr(gatewayv1.Group(meridio2v1alpha1.GroupVersion.Group)),
			Kind:  ptr(gatewayv1.Kind("DistributionGroup")),
			Name:  "dg-1",
		},
	}
	assert.True(t, backendRefMatchesDG(matching, testNamespace, dgKey))

	wrongKind := gatewayv1.BackendRef{
		BackendObjectReference: gatewayv1.BackendObjectReference{
			Name: "dg-1",
		},
	}
	assert.False(t, backendRefMatchesDG(wrongKind, testNamespace, dgKey))
}

// --- Sidecar contract verification ---
// Validates that ENC output from resolveGatewayConnections is consumable by the
// sidecar controller (internal/controller/sidecar/controller.go).
//
// Sidecar assumptions verified:
//   - GatewayConnection.Name: used as tableID allocation key (must be stable, equals Gateway name)
//   - VIPs: net.ParseIP(vip) must succeed (plain IPs, not CIDRs)
//   - NextHops: net.ParseIP(hop) must succeed (plain IPs, not CIDRs)
//   - Network.Subnet: net.ParseCIDR(subnet) must succeed
//   - Network.InterfaceHint: passed to findInterfaceBySubnet as hint (non-empty for NAD)
//   - Domain.Name: "<gateway>-<ipfamily>" naming convention
//   - Domain.IPFamily: "IPv4" or "IPv6"

func TestSidecarContract_DualStack(t *testing.T) {
	// Build a dual-stack scenario: Gateway with IPv4+IPv6 VIPs, two subnets
	pod := newPod("app-1", corev1.PodRunning, map[string]string{"app": "web"})
	pod.Annotations = map[string]string{
		"k8s.v1.cni.cncf.io/network-status": `[
			{"name":"default","interface":"eth0","ips":["10.244.0.5"],"default":true},
			{"name":"nad-1","interface":"net1","ips":["169.111.100.10","fd00::100:a"]}
		]`,
	}
	gw := acceptedGateway("sllb-a", testControllerName, "20.0.0.1", "2001:db8::1")

	gc := &meridio2v1alpha1.GatewayConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: "sllb-a-config", Namespace: testNamespace},
		Spec: meridio2v1alpha1.GatewayConfigurationSpec{
			NetworkAttachments: []meridio2v1alpha1.NetworkAttachment{
				{Type: "NAD", NAD: &meridio2v1alpha1.NAD{Interface: "net1", Name: "nad-1", Namespace: testNamespace}},
			},
			InternalSubnets: []meridio2v1alpha1.InternalSubnet{
				{AttachmentType: "NAD", CIDR: testSubnetV4},
				{AttachmentType: "NAD", CIDR: testSubnetV6},
			},
			HorizontalScaling: meridio2v1alpha1.HorizontalScaling{Replicas: 1},
		},
	}
	dg := newDG("dg-1", map[string]string{"app": "web"}, "sllb-a")
	sllbrPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sllbr-sllb-a-abc",
			Namespace: testNamespace,
			Labels:    map[string]string{labelGatewayName: "sllb-a"},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}

	r, _ := setupReconciler(pod, gw, gc, dg, sllbrPod)
	r.IPScraper = func(pod *corev1.Pod, cidr, _ string) string {
		switch cidr {
		case testSubnetV4:
			return testNextHopV4
		case testSubnetV6:
			return testNextHopV6
		}
		return ""
	}

	connections, err := r.resolveGatewayConnections(context.Background(), pod)
	require.NoError(t, err)
	require.Len(t, connections, 1)

	gconn := connections[0]

	// Contract 1: GatewayConnection.Name == Gateway name (stable tableID key)
	assert.Equal(t, "sllb-a", gconn.Name, "GatewayConnection.Name must equal Gateway name for stable tableID allocation")

	// Contract 2-6: validate each domain
	require.Len(t, gconn.Domains, 2, "dual-stack should produce 2 domains")

	for _, domain := range gconn.Domains {
		// Contract 2: Domain.IPFamily must be "IPv4" or "IPv6"
		assert.Contains(t, []string{"IPv4", "IPv6"}, domain.IPFamily,
			"IPFamily must be IPv4 or IPv6")

		// Contract 3: Domain.Name must follow "<gateway>-<ipfamily>" convention
		assert.Equal(t, "sllb-a-"+domain.IPFamily, domain.Name,
			"Domain name must be <gateway>-<ipfamily>")

		// Contract 4: VIPs must be parseable by net.ParseIP (plain IPs, not CIDRs)
		for _, vip := range domain.VIPs {
			ip := net.ParseIP(vip)
			assert.NotNil(t, ip, "sidecar calls net.ParseIP on VIP %q — must not be CIDR", vip)
			assert.NotContains(t, vip, "/", "VIP must be plain IP, not CIDR")
		}

		// Contract 5: NextHops must be parseable by net.ParseIP (plain IPs, not CIDRs)
		for _, hop := range domain.NextHops {
			ip := net.ParseIP(hop)
			assert.NotNil(t, ip, "sidecar calls net.ParseIP on NextHop %q — must not be CIDR", hop)
			assert.NotContains(t, hop, "/", "NextHop must be plain IP, not CIDR")
		}

		// Contract 6: Network.Subnet must be parseable by net.ParseCIDR
		_, _, err := net.ParseCIDR(domain.Network.Subnet)
		assert.NoError(t, err, "sidecar calls net.ParseCIDR on Subnet %q", domain.Network.Subnet)

		// Contract 7: Network.InterfaceHint must be non-empty for NAD attachment type
		assert.NotEmpty(t, domain.Network.InterfaceHint,
			"InterfaceHint must be set for NAD — sidecar passes it to findInterfaceBySubnet")
	}

	// Verify actual values for IPv4 domain
	var ipv4Domain, ipv6Domain meridio2v1alpha1.NetworkDomain
	for _, d := range gconn.Domains {
		if d.IPFamily == testIPFamilyV4 {
			ipv4Domain = d
		} else {
			ipv6Domain = d
		}
	}

	assert.Equal(t, []string{"20.0.0.1"}, ipv4Domain.VIPs)
	assert.Equal(t, []string{testNextHopV4}, ipv4Domain.NextHops)
	assert.Equal(t, testSubnetV4, ipv4Domain.Network.Subnet)
	assert.Equal(t, "net1", ipv4Domain.Network.InterfaceHint)

	assert.Equal(t, []string{"2001:db8::1"}, ipv6Domain.VIPs)
	assert.Equal(t, []string{testNextHopV6}, ipv6Domain.NextHops)
	assert.Equal(t, testSubnetV6, ipv6Domain.Network.Subnet)
	assert.Equal(t, "net1", ipv6Domain.Network.InterfaceHint)
}
