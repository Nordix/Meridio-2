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
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"slices"
	"strings"

	netdefv1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	meridio2v1alpha1 "github.com/nordix/meridio-2/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

const (
	kindGateway              = "Gateway"
	kindGatewayConfiguration = "GatewayConfiguration"
	kindDistributionGroup    = "DistributionGroup"
	labelGatewayName         = "gateway.networking.k8s.io/gateway-name"
	attachmentTypeNAD        = "NAD"
	// Readiness gate condition types (to be consolidated to shared package)
	readinessGateIPv4 = "meridio-2.nordix.org/ipv4-connectivity"
	readinessGateIPv6 = "meridio-2.nordix.org/ipv6-connectivity"
)

// resolveGatewayConnections determines which Gateways a Pod participates in
// and computes the desired ENC spec for each.
func (r *Reconciler) resolveGatewayConnections(ctx context.Context, pod *corev1.Pod) ([]meridio2v1alpha1.GatewayConnection, error) {
	dgs, err := r.listMatchingDGs(ctx, pod)
	if err != nil {
		return nil, err
	}
	if len(dgs) == 0 {
		return nil, nil
	}

	// Collect unique accepted Gateways across all matching DGs
	gatewayMap := make(map[string]*gatewayv1.Gateway)
	for i := range dgs {
		gateways, err := r.resolveGatewaysForDG(ctx, &dgs[i])
		if err != nil {
			return nil, err
		}
		for j := range gateways {
			key := client.ObjectKeyFromObject(&gateways[j]).String()
			if _, exists := gatewayMap[key]; !exists {
				gatewayMap[key] = &gateways[j]
			}
		}
	}

	// Build GatewayConnection per Gateway
	var connections []meridio2v1alpha1.GatewayConnection
	for _, gw := range gatewayMap {
		conn, err := r.buildGatewayConnection(ctx, pod, gw)
		if err != nil {
			logf.FromContext(ctx).Error(err, "skipping gateway", "gateway", gw.Name)
			continue
		}
		if conn != nil {
			connections = append(connections, *conn)
		}
	}
	// Sort connections by Gateway name: map iteration order is non-deterministic,
	// and ordering differences would trigger unnecessary ENC updates.
	slices.SortFunc(connections, func(a, b meridio2v1alpha1.GatewayConnection) int {
		return cmp.Compare(a.Name, b.Name)
	})

	return connections, nil
}

// buildGatewayConnection builds a GatewayConnection for a single Gateway.
// The pod's Multus network-status annotation is used to resolve interface hints.
func (r *Reconciler) buildGatewayConnection(ctx context.Context, pod *corev1.Pod, gw *gatewayv1.Gateway) (*meridio2v1alpha1.GatewayConnection, error) {
	subnetToType, err := r.getNetworkContexts(ctx, gw)
	if err != nil {
		return nil, err
	}
	if len(subnetToType) == 0 {
		return nil, nil
	}

	ipv4VIPs, ipv6VIPs := extractVIPs(gw)
	ipv4Hops, ipv6Hops, err := r.getSLLBRNextHops(ctx, gw, subnetToType)
	if err != nil {
		return nil, err
	}

	domains := make([]meridio2v1alpha1.NetworkDomain, 0, len(subnetToType))
	for subnet, attachmentType := range subnetToType {
		if attachmentType != attachmentTypeNAD {
			continue // Only NAD for MVP
		}

		ipFamily, err := cidrIPFamily(subnet)
		if err != nil {
			continue
		}

		var vips, nextHops []string
		if ipFamily == "IPv4" {
			vips, nextHops = ipv4VIPs, ipv4Hops
		} else {
			vips, nextHops = ipv6VIPs, ipv6Hops
		}

		if len(vips) == 0 && len(nextHops) == 0 {
			continue
		}

		ifName := scrapeInterfaceForSubnet(pod, subnet)
		if ifName == "" {
			continue // Pod has no interface in this subnet — skip domain
		}

		domains = append(domains, meridio2v1alpha1.NetworkDomain{
			Name:     fmt.Sprintf("%s-%s", gw.Name, ipFamily),
			IPFamily: ipFamily,
			Network: meridio2v1alpha1.NetworkIdentity{
				Subnet:        subnet,
				InterfaceHint: ifName,
			},
			VIPs:     vips,
			NextHops: nextHops,
		})
	}

	if len(domains) == 0 {
		return nil, nil
	}

	// Sort domains by name: subnetToType map iteration order is non-deterministic.
	slices.SortFunc(domains, func(a, b meridio2v1alpha1.NetworkDomain) int {
		return cmp.Compare(a.Name, b.Name)
	})

	return &meridio2v1alpha1.GatewayConnection{
		Name:    gw.Name,
		Domains: domains,
	}, nil
}

// listMatchingDGs returns DistributionGroups whose selector matches the Pod's labels.
// This is the reverse of the DG controller's Pod listing — O(DGs) per reconcile.
func (r *Reconciler) listMatchingDGs(ctx context.Context, pod *corev1.Pod) ([]meridio2v1alpha1.DistributionGroup, error) {
	var dgList meridio2v1alpha1.DistributionGroupList
	listOpts := []client.ListOption{client.InNamespace(pod.Namespace)}
	if err := r.List(ctx, &dgList, listOpts...); err != nil {
		return nil, err
	}

	var matching []meridio2v1alpha1.DistributionGroup
	for _, dg := range dgList.Items {
		if dg.Spec.Selector == nil {
			continue // nil selector = match nothing (DG controller convention)
		}
		selector, err := metav1.LabelSelectorAsSelector(dg.Spec.Selector)
		if err != nil {
			continue
		}
		if selector.Matches(labels.Set(pod.Labels)) {
			matching = append(matching, dg)
		}
	}
	return matching, nil
}

// resolveGatewaysForDG returns accepted Gateways referenced by a DG (direct + indirect via L34Routes).
func (r *Reconciler) resolveGatewaysForDG(ctx context.Context, dg *meridio2v1alpha1.DistributionGroup) ([]gatewayv1.Gateway, error) {
	gatewayMap := make(map[string]*gatewayv1.Gateway)

	// Direct: DG.spec.parentRefs → Gateway
	for _, parentRef := range dg.Spec.ParentRefs {
		gw, err := r.getGatewayFromParentRef(ctx, parentRef, dg.Namespace)
		if err != nil {
			return nil, err
		}
		if gw != nil {
			gatewayMap[client.ObjectKeyFromObject(gw).String()] = gw
		}
	}

	// Indirect: L34Route.backendRef=DG → L34Route.parentRef → Gateway
	routes, err := r.listRoutesReferencingDG(ctx, dg)
	if err != nil {
		return nil, err
	}
	for _, route := range routes {
		for _, parentRef := range route.Spec.ParentRefs {
			gw, err := r.getGatewayFromGatewayAPIParentRef(ctx, parentRef, route.Namespace)
			if err != nil {
				return nil, err
			}
			if gw != nil {
				gatewayMap[client.ObjectKeyFromObject(gw).String()] = gw
			}
		}
	}

	// Filter by Accepted condition
	var accepted []gatewayv1.Gateway
	for _, gw := range gatewayMap {
		if r.isGatewayAccepted(gw) {
			accepted = append(accepted, *gw)
		}
	}
	return accepted, nil
}

// getSLLBRNextHops returns secondary-network IPs of SLLBR Pods for a Gateway.
// Returns plain IP strings split by IP family.
func (r *Reconciler) getSLLBRNextHops(ctx context.Context, gw *gatewayv1.Gateway, subnetToType map[string]string) (ipv4, ipv6 []string, err error) {
	var podList corev1.PodList
	if err := r.List(ctx, &podList,
		client.InNamespace(gw.Namespace),
		client.MatchingLabels{labelGatewayName: gw.Name},
	); err != nil {
		return nil, nil, err
	}

	scraper := r.IPScraper
	if scraper == nil {
		scraper = defaultIPScraper
	}

	for _, pod := range podList.Items {
		if pod.Status.Phase != corev1.PodRunning {
			continue
		}
		// Level 1: container readiness (all non-init containers must be Ready)
		if !isLBPodReady(&pod) {
			continue
		}
		for cidr, attachmentType := range subnetToType {
			ip := scraper(&pod, cidr, attachmentType)
			if ip == "" {
				continue
			}
			parsed := net.ParseIP(ip)
			if parsed == nil {
				continue
			}
			// Level 2: per-IP-family gate check
			if parsed.To4() != nil {
				if hasConnectivityGate(&pod, readinessGateIPv4) {
					ipv4 = append(ipv4, ip)
				}
			} else {
				if hasConnectivityGate(&pod, readinessGateIPv6) {
					ipv6 = append(ipv6, ip)
				}
			}
		}
	}
	// Sort for deterministic output: Pod list order from cache is not guaranteed,
	// and non-deterministic ordering causes unnecessary ENC updates.
	slices.Sort(ipv4)
	slices.Sort(ipv6)
	return ipv4, ipv6, nil
}

// getNetworkContexts extracts network context from GatewayConfiguration.
// Returns subnetToType (CIDR→"NAD"/"DRA").
func (r *Reconciler) getNetworkContexts(ctx context.Context, gw *gatewayv1.Gateway) (map[string]string, error) {
	subnetToType := make(map[string]string)

	if gw.Spec.Infrastructure == nil || gw.Spec.Infrastructure.ParametersRef == nil {
		return subnetToType, nil
	}

	ref := gw.Spec.Infrastructure.ParametersRef
	if string(ref.Group) != meridio2v1alpha1.GroupVersion.Group || string(ref.Kind) != kindGatewayConfiguration {
		return subnetToType, nil
	}

	var gwConfig meridio2v1alpha1.GatewayConfiguration
	if err := r.Get(ctx, client.ObjectKey{Namespace: gw.Namespace, Name: ref.Name}, &gwConfig); err != nil {
		return subnetToType, client.IgnoreNotFound(err)
	}

	for _, subnet := range gwConfig.Spec.InternalSubnets {
		normalized, err := normalizeCIDR(subnet.CIDR)
		if err != nil {
			continue
		}
		subnetToType[normalized] = subnet.AttachmentType
	}

	return subnetToType, nil
}

// extractVIPs splits Gateway.status.addresses into IPv4 and IPv6 plain IP strings.
func extractVIPs(gw *gatewayv1.Gateway) (ipv4, ipv6 []string) {
	seen := make(map[string]bool)
	for _, addr := range gw.Status.Addresses {
		ip := net.ParseIP(addr.Value)
		if ip == nil {
			continue
		}
		s := ip.String()
		if seen[s] {
			continue
		}
		seen[s] = true
		if ip.To4() != nil {
			ipv4 = append(ipv4, s)
		} else {
			ipv6 = append(ipv6, s)
		}
	}
	return ipv4, ipv6
}

// --- helpers ---

func (r *Reconciler) getGatewayFromParentRef(ctx context.Context, ref meridio2v1alpha1.ParentReference, localNs string) (*gatewayv1.Gateway, error) {
	group := gatewayv1.GroupName
	if ref.Group != nil {
		group = *ref.Group
	}
	kind := kindGateway
	if ref.Kind != nil {
		kind = *ref.Kind
	}
	if group != gatewayv1.GroupName || kind != kindGateway {
		return nil, nil
	}
	ns := localNs
	if ref.Namespace != nil {
		ns = *ref.Namespace
	}
	var gw gatewayv1.Gateway
	if err := r.Get(ctx, client.ObjectKey{Namespace: ns, Name: ref.Name}, &gw); err != nil {
		return nil, client.IgnoreNotFound(err)
	}
	return &gw, nil
}

func (r *Reconciler) getGatewayFromGatewayAPIParentRef(ctx context.Context, ref gatewayv1.ParentReference, localNs string) (*gatewayv1.Gateway, error) {
	ns := localNs
	if ref.Namespace != nil {
		ns = string(*ref.Namespace)
	}
	var gw gatewayv1.Gateway
	if err := r.Get(ctx, client.ObjectKey{Namespace: ns, Name: string(ref.Name)}, &gw); err != nil {
		return nil, client.IgnoreNotFound(err)
	}
	return &gw, nil
}

func (r *Reconciler) listRoutesReferencingDG(ctx context.Context, dg *meridio2v1alpha1.DistributionGroup) ([]meridio2v1alpha1.L34Route, error) {
	var routeList meridio2v1alpha1.L34RouteList
	listOpts := []client.ListOption{client.InNamespace(dg.Namespace)}
	if err := r.List(ctx, &routeList, listOpts...); err != nil {
		return nil, err
	}

	dgKey := client.ObjectKeyFromObject(dg)
	var matching []meridio2v1alpha1.L34Route
	for _, route := range routeList.Items {
		for _, backendRef := range route.Spec.BackendRefs {
			if backendRefMatchesDG(backendRef, route.Namespace, dgKey) {
				matching = append(matching, route)
				break
			}
		}
	}
	return matching, nil
}

func backendRefMatchesDG(ref gatewayv1.BackendRef, localNs string, dgKey client.ObjectKey) bool {
	group := gatewayv1.GroupName
	if ref.Group != nil {
		group = string(*ref.Group)
	}
	kind := "Service"
	if ref.Kind != nil {
		kind = string(*ref.Kind)
	}
	ns := localNs
	if ref.Namespace != nil {
		ns = string(*ref.Namespace)
	}
	return group == meridio2v1alpha1.GroupVersion.Group &&
		kind == kindDistributionGroup &&
		ns == dgKey.Namespace &&
		string(ref.Name) == dgKey.Name
}

func (r *Reconciler) isGatewayAccepted(gw *gatewayv1.Gateway) bool {
	for _, cond := range gw.Status.Conditions {
		if cond.Type == string(gatewayv1.GatewayConditionAccepted) &&
			cond.Status == metav1.ConditionTrue &&
			strings.HasSuffix(cond.Message, r.ControllerName) {
			return true
		}
	}
	return false
}

func normalizeCIDR(cidr string) (string, error) {
	_, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return "", err
	}
	return ipnet.String(), nil
}

func cidrIPFamily(cidr string) (string, error) {
	ip, _, err := net.ParseCIDR(cidr)
	if err != nil {
		return "", err
	}
	if ip.To4() != nil {
		return "IPv4", nil
	}
	return "IPv6", nil
}

// defaultIPScraper extracts secondary IP from Multus network-status annotation.
func defaultIPScraper(pod *corev1.Pod, cidr, attachmentType string) string {
	if attachmentType != "NAD" {
		return ""
	}
	annotation, ok := pod.Annotations[netdefv1.NetworkStatusAnnot]
	if !ok {
		return ""
	}
	var networkStatus []netdefv1.NetworkStatus
	if err := json.Unmarshal([]byte(annotation), &networkStatus); err != nil {
		return ""
	}
	_, targetNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return ""
	}
	for _, netif := range networkStatus {
		if netif.Default {
			continue
		}
		for _, ipStr := range netif.IPs {
			ip := net.ParseIP(ipStr)
			if ip != nil && targetNet.Contains(ip) {
				return ipStr
			}
		}
	}
	return ""
}

// scrapeInterfaceForSubnet finds the interface name on the pod that has an IP
// within the given CIDR subnet, using the Multus network-status annotation.
func scrapeInterfaceForSubnet(pod *corev1.Pod, cidr string) string {
	annotation, ok := pod.Annotations[netdefv1.NetworkStatusAnnot]
	if !ok {
		return ""
	}
	var networkStatus []netdefv1.NetworkStatus
	if err := json.Unmarshal([]byte(annotation), &networkStatus); err != nil {
		return ""
	}
	_, targetNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return ""
	}
	for _, netif := range networkStatus {
		if netif.Default {
			continue
		}
		for _, ipStr := range netif.IPs {
			ip := net.ParseIP(ipStr)
			if ip != nil && targetNet.Contains(ip) {
				return netif.Interface
			}
		}
	}
	return ""
}

// isLBPodReady(pod) bool - checks all non-init container statuses are Ready. Returns false if any container is not ready or if Pod is being deleted.
func isLBPodReady(pod *corev1.Pod) bool {
	if pod.DeletionTimestamp != nil {
		return false
	}
	oneReady := false
	for _, cs := range pod.Status.ContainerStatuses {
		if !cs.Ready {
			return false
		} else { // At least one container is ready
			oneReady = true
		}
	}
	return oneReady
}

// hasConnectivityGate - Checks if a specific readiness gate condition is True on the Pod.
func hasConnectivityGate(pod *corev1.Pod, conditionType string) bool {
	// Check if the gate is declared in the Pod spec
	gateDeclared := false
	for _, gate := range pod.Spec.ReadinessGates {
		if string(gate.ConditionType) == conditionType {
			gateDeclared = true
			break
		}
	}
	// gate not applicable for this IP family: if gate not declared, include the Pod (skip filtering)
	if !gateDeclared {
		return true
	}
	// Check if the condition is True in the Pod status
	for _, condition := range pod.Status.Conditions {
		if string(condition.Type) == conditionType {
			return condition.Status == corev1.ConditionTrue
		}
	}
	// Gate declared but condition not found - treat as not ready
	return false
}
