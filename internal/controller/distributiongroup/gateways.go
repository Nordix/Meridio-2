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

	meridio2v1alpha1 "github.com/nordix/meridio-2/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// listReferencedGateways returns all Gateways referenced by the DistributionGroup (direct + indirect via L34Routes)
func (r *DistributionGroupReconciler) listReferencedGateways(ctx context.Context, dg *meridio2v1alpha1.DistributionGroup) ([]gatewayv1.Gateway, error) {
	return ListReferencedGateways(ctx, r.Client, r.Namespace, dg)
}

// ListReferencedGateways returns all Gateways referenced by the DistributionGroup (direct
// parentRefs + indirect via L34Route backendRefs). namespace scopes the L34Route List to a
// single namespace (mirrors the reconciler's r.Namespace); pass "" to watch all namespaces.
//
// Exported so that other consumers reading the same association (e.g. the controller-manager
// metrics collectors in internal/metrics) can reuse the exact resolution semantics used by
// reconciliation, rather than reimplementing parentRef/L34Route walking and risking the two
// paths diverging on what counts as "referenced".
func ListReferencedGateways(
	ctx context.Context, c client.Client, namespace string, dg *meridio2v1alpha1.DistributionGroup,
) ([]gatewayv1.Gateway, error) {
	gatewayMap := make(map[string]*gatewayv1.Gateway)

	// Find Gateways from DG.spec.parentRefs
	for _, parentRef := range dg.Spec.ParentRefs {
		gw, err := getGatewayFromParentRef(ctx, c, parentRef, dg.Namespace)
		if err != nil {
			return nil, err
		}
		if gw != nil {
			gatewayMap[client.ObjectKeyFromObject(gw).String()] = gw
		}
	}

	// Find L34Routes referencing this DG
	routes, err := listRoutesReferencingDG(ctx, c, namespace, dg)
	if err != nil {
		return nil, err
	}

	// Find Gateways from L34Route.spec.parentRefs
	for _, route := range routes {
		for _, parentRef := range route.Spec.ParentRefs {
			gw, err := getGatewayFromGatewayAPIParentRef(ctx, c, parentRef, route.Namespace)
			if err != nil {
				return nil, err
			}
			if gw != nil {
				gatewayMap[client.ObjectKeyFromObject(gw).String()] = gw
			}
		}
	}

	gateways := make([]gatewayv1.Gateway, 0, len(gatewayMap))
	for _, gw := range gatewayMap {
		gateways = append(gateways, *gw)
	}

	return gateways, nil
}

// getGatewayFromParentRef fetches a Gateway from a ParentReference
func getGatewayFromParentRef(ctx context.Context, c client.Client, ref meridio2v1alpha1.ParentReference, localNs string) (*gatewayv1.Gateway, error) {
	// Verify parentRef is a Gateway (DG API enforces this via CEL, but be defensive)
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
	if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: ref.Name}, &gw); err != nil {
		return nil, client.IgnoreNotFound(err)
	}

	return &gw, nil
}

// getGatewayFromGatewayAPIParentRef fetches a Gateway from Gateway API ParentReference
func getGatewayFromGatewayAPIParentRef(ctx context.Context, c client.Client, ref gatewayv1.ParentReference, localNs string) (*gatewayv1.Gateway, error) {
	ns := localNs
	if ref.Namespace != nil {
		ns = string(*ref.Namespace)
	}

	var gw gatewayv1.Gateway
	if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: string(ref.Name)}, &gw); err != nil {
		return nil, client.IgnoreNotFound(err)
	}

	return &gw, nil
}

// getNetworkContexts extracts network context from GatewayConfigurations
// Returns map: subnet CIDR → attachment type (currently only NAD)
func (r *DistributionGroupReconciler) getNetworkContexts(ctx context.Context, gateways []gatewayv1.Gateway) ([]gatewayNetworkContext, error) {
	logger := log.FromContext(ctx)
	result := make([]gatewayNetworkContext, 0, len(gateways))

	for _, gw := range gateways {
		if gw.Spec.Infrastructure == nil || gw.Spec.Infrastructure.ParametersRef == nil {
			continue
		}

		ref := gw.Spec.Infrastructure.ParametersRef
		if string(ref.Group) != meridio2v1alpha1.GroupVersion.Group || string(ref.Kind) != kindGatewayConfiguration {
			continue
		}

		var gwConfig meridio2v1alpha1.GatewayConfiguration
		if err := r.Get(ctx, client.ObjectKey{Namespace: gw.Namespace, Name: ref.Name}, &gwConfig); err != nil {
			return nil, client.IgnoreNotFound(err)
		}

		networks := make(map[string]string, len(gwConfig.Spec.InternalSubnets))
		for _, subnet := range gwConfig.Spec.InternalSubnets {
			normalized, err := normalizeCIDR(subnet.CIDR)
			if err != nil {
				logger.Info("Skipping invalid CIDR in GatewayConfiguration", "gateway", gw.Name, "gwconfig", gwConfig.Name, "cidr", subnet.CIDR, "error", err)
				continue
			}
			networks[normalized] = subnet.AttachmentType
		}

		if len(networks) > 0 {
			result = append(result, gatewayNetworkContext{
				gateway:  client.ObjectKeyFromObject(&gw),
				networks: networks,
			})
		}
	}

	return result, nil
}
