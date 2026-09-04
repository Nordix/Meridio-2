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
	"net"
	"sort"

	meridio2v1alpha1 "github.com/nordix/meridio-2/api/v1alpha1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// mapL34RouteToGateway maps L34Route changes to Gateway reconciliation requests
func (r *GatewayReconciler) mapL34RouteToGateway(ctx context.Context, obj client.Object) []ctrl.Request {
	route, ok := obj.(*meridio2v1alpha1.L34Route)
	if !ok {
		return nil
	}

	requests := make([]ctrl.Request, 0, len(route.Spec.ParentRefs))
	for _, parentRef := range route.Spec.ParentRefs {
		// Check if parentRef is a Gateway
		group := gatewayv1.GroupName
		if parentRef.Group != nil {
			group = string(*parentRef.Group)
		}
		kind := kindGateway
		if parentRef.Kind != nil {
			kind = string(*parentRef.Kind)
		}
		if group != gatewayv1.GroupName || kind != kindGateway {
			continue
		}

		// Resolve namespace
		ns := route.Namespace
		if parentRef.Namespace != nil {
			ns = string(*parentRef.Namespace)
		}

		requests = append(requests, ctrl.Request{
			NamespacedName: client.ObjectKey{
				Namespace: ns,
				Name:      string(parentRef.Name),
			},
		})
	}
	return requests
}

// updateAddressesFromRoutes updates Gateway status.addresses from L34Routes
func (r *GatewayReconciler) updateAddressesFromRoutes(ctx context.Context, gw *gatewayv1.Gateway) error {
	// List L34Routes respecting namespace configuration
	var routeList meridio2v1alpha1.L34RouteList
	listOpts := []client.ListOption{}
	if r.Namespace != "" {
		listOpts = append(listOpts, client.InNamespace(r.Namespace))
	}
	if err := r.List(ctx, &routeList, listOpts...); err != nil {
		return err
	}

	// Collect unique destinationCIDRs
	cidrs := make(map[string]bool)
	for _, route := range routeList.Items {
		if !routeReferencesGateway(&route, gw) {
			continue
		}
		for _, cidr := range route.Spec.DestinationCIDRs {
			cidrs[cidr] = true
		}
	}

	// Convert to sorted addresses
	addresses := make([]gatewayv1.GatewayStatusAddress, 0, len(cidrs))
	for cidr := range cidrs {
		ip, _, err := net.ParseCIDR(cidr)
		if err != nil {
			continue
		}
		addressType := gatewayv1.IPAddressType
		addresses = append(addresses, gatewayv1.GatewayStatusAddress{
			Type:  &addressType,
			Value: ip.String(),
		})
	}

	// Sort for deterministic output
	sort.Slice(addresses, func(i, j int) bool {
		return addresses[i].Value < addresses[j].Value
	})

	// Update if changed
	if !addressesEqual(gw.Status.Addresses, addresses) {
		gw.Status.Addresses = addresses
		return r.Status().Update(ctx, gw)
	}

	return nil
}

// routeReferencesGateway checks if L34Route references the Gateway
// TODO: Move to internal/common/gatewayutil package - similar logic exists in DistributionGroup controller
func routeReferencesGateway(route *meridio2v1alpha1.L34Route, gw *gatewayv1.Gateway) bool {
	for _, parentRef := range route.Spec.ParentRefs {
		// Check if parentRef is a Gateway
		group := gatewayv1.GroupName
		if parentRef.Group != nil {
			group = string(*parentRef.Group)
		}
		kind := kindGateway
		if parentRef.Kind != nil {
			kind = string(*parentRef.Kind)
		}
		if group != gatewayv1.GroupName || kind != kindGateway {
			continue
		}

		// Check namespace and name
		ns := route.Namespace
		if parentRef.Namespace != nil {
			ns = string(*parentRef.Namespace)
		}
		if ns == gw.Namespace && string(parentRef.Name) == gw.Name {
			return true
		}
	}
	return false
}

// addressesEqual checks if two address slices are equal
func addressesEqual(a, b []gatewayv1.GatewayStatusAddress) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Value != b[i].Value {
			return false
		}
	}
	return true
}
