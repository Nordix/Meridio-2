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
	"strings"

	meridio2v1alpha1 "github.com/nordix/meridio-2/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// listReferencedGateways returns all Gateways referenced by the DistributionGroup (direct + indirect via L34Routes)
func (r *DistributionGroupReconciler) listReferencedGateways(ctx context.Context, dg *meridio2v1alpha1.DistributionGroup) ([]gatewayv1.Gateway, error) {
	gatewayMap := make(map[string]*gatewayv1.Gateway)

	// Find Gateways from DG.spec.parentRefs
	for _, parentRef := range dg.Spec.ParentRefs {
		gw, err := r.getGatewayFromParentRef(ctx, parentRef, dg.Namespace)
		if err != nil {
			return nil, err
		}
		if gw != nil {
			gatewayMap[client.ObjectKeyFromObject(gw).String()] = gw
		}
	}

	// Find L34Routes referencing this DG
	routes, err := r.listRoutesReferencingDG(ctx, dg)
	if err != nil {
		return nil, err
	}

	// Find Gateways from L34Route.spec.parentRefs
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

	gateways := make([]gatewayv1.Gateway, 0, len(gatewayMap))
	for _, gw := range gatewayMap {
		gateways = append(gateways, *gw)
	}

	return gateways, nil
}

// getGatewayFromParentRef fetches a Gateway from a ParentReference
func (r *DistributionGroupReconciler) getGatewayFromParentRef(ctx context.Context, ref meridio2v1alpha1.ParentReference, localNs string) (*gatewayv1.Gateway, error) {
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
	if err := r.Get(ctx, client.ObjectKey{Namespace: ns, Name: ref.Name}, &gw); err != nil {
		return nil, client.IgnoreNotFound(err)
	}

	return &gw, nil
}

// getGatewayFromGatewayAPIParentRef fetches a Gateway from Gateway API ParentReference
func (r *DistributionGroupReconciler) getGatewayFromGatewayAPIParentRef(ctx context.Context, ref gatewayv1.ParentReference, localNs string) (*gatewayv1.Gateway, error) {
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

// isGatewayAccepted checks if Gateway has Accepted=True condition set by this controller
// TODO(gateway-controller): Move to internal/common/gateway package when Gateway controller is implemented.
// This logic should be shared between Gateway and DistributionGroup controllers to ensure
// consistent Gateway acceptance checking. The Gateway controller will set the Accepted condition,
// and both controllers need to interpret it the same way.
//
// This check allows the DG controller to filter Gateways without watching GatewayClass objects.
// By checking the Accepted condition (set by the Gateway controller), we avoid the complexity of:
// - Watching GatewayClass resources
// - Resolving Gateway.spec.gatewayClassName references
// - Checking GatewayClass.spec.controllerName matches
// Instead, we rely on the Gateway controller to mark relevant Gateways as Accepted.
func (r *DistributionGroupReconciler) isGatewayAccepted(gw *gatewayv1.Gateway) bool {
	for _, cond := range gw.Status.Conditions {
		if cond.Type == string(gatewayv1.GatewayConditionAccepted) &&
			cond.Status == metav1.ConditionTrue &&
			strings.HasSuffix(cond.Message, r.ControllerName) {
			return true
		}
	}
	return false
}

// getNetworkContexts extracts network context from GatewayConfigurations
// Returns map: subnet CIDR → attachment type (NAD/DRA)
func (r *DistributionGroupReconciler) getNetworkContexts(ctx context.Context, gateways []gatewayv1.Gateway) (map[string]string, error) {
	logger := log.FromContext(ctx)
	networkContexts := make(map[string]string)

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

		// Extract subnet CIDRs with their attachment types
		for _, subnet := range gwConfig.Spec.NetworkSubnets {
			for _, cidr := range subnet.CIDRs {
				normalized, err := normalizeCIDR(cidr)
				if err != nil {
					logger.Info("Skipping invalid CIDR in GatewayConfiguration", "gateway", gw.Name, "gwconfig", gwConfig.Name, "cidr", cidr, "error", err)
					continue
				}
				networkContexts[normalized] = subnet.AttachmentType
			}
		}
	}

	return networkContexts, nil
}
