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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// mapPodToDistributionGroup maps Pod changes to DistributionGroup reconciliation requests
func (r *DistributionGroupReconciler) mapPodToDistributionGroup(ctx context.Context, obj client.Object) []ctrl.Request {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return nil
	}

	// List all DistributionGroups
	var dgList meridio2v1alpha1.DistributionGroupList
	listOpts := []client.ListOption{client.InNamespace(pod.Namespace)}
	if err := r.List(ctx, &dgList, listOpts...); err != nil {
		return nil
	}

	var requests []ctrl.Request
	for _, dg := range dgList.Items {
		if dg.Spec.Selector == nil {
			continue
		}
		selector, err := metav1.LabelSelectorAsSelector(dg.Spec.Selector)
		if err != nil {
			continue
		}
		if selector.Matches(labels.Set(pod.Labels)) {
			requests = append(requests, ctrl.Request{
				NamespacedName: client.ObjectKeyFromObject(&dg),
			})
		}
	}
	return requests
}

// mapGatewayToDistributionGroup maps Gateway changes to DistributionGroup reconciliation requests
// Note: On update events, controller-runtime calls this mapper twice (once with old object, once with new).
// This ensures we catch both Accepted=False→True transitions (new object triggers reconcile) and
// Accepted=True→False transitions (old object triggers reconcile for cleanup).
// On delete events, the mapper receives the deleted object with its final status intact.
func (r *DistributionGroupReconciler) mapGatewayToDistributionGroup(ctx context.Context, obj client.Object) []ctrl.Request {
	gw, ok := obj.(*gatewayv1.Gateway)
	if !ok {
		return nil
	}

	// Filter early: only process Gateways accepted by this controller
	// This avoids unnecessary reconciliation loops for irrelevant Gateways
	if !r.isGatewayAccepted(gw) {
		return nil
	}

	return r.findDGsReferencingGateways(ctx, []client.ObjectKey{client.ObjectKeyFromObject(gw)})
}

// mapL34RouteToDistributionGroup maps L34Route changes to DistributionGroup reconciliation requests
func (r *DistributionGroupReconciler) mapL34RouteToDistributionGroup(ctx context.Context, obj client.Object) []ctrl.Request {
	route, ok := obj.(*meridio2v1alpha1.L34Route)
	if !ok {
		return nil
	}

	// Extract DGs from backendRefs
	dgKeys := extractDGsFromBackendRefs(route.Spec.BackendRefs, route.Namespace)

	requests := make([]ctrl.Request, 0, len(dgKeys))
	for key := range dgKeys {
		requests = append(requests, ctrl.Request{NamespacedName: key})
	}
	return requests
}

// mapGatewayConfigToDistributionGroup maps GatewayConfiguration changes to DistributionGroup reconciliation requests
func (r *DistributionGroupReconciler) mapGatewayConfigToDistributionGroup(ctx context.Context, obj client.Object) []ctrl.Request {
	gwConfig, ok := obj.(*meridio2v1alpha1.GatewayConfiguration)
	if !ok {
		return nil
	}

	// Find Gateways using this GatewayConfiguration
	var gwList gatewayv1.GatewayList
	if err := r.List(ctx, &gwList, client.InNamespace(gwConfig.Namespace)); err != nil {
		return nil
	}

	var gatewayKeys []client.ObjectKey
	for _, gw := range gwList.Items {
		if gw.Spec.Infrastructure != nil && gw.Spec.Infrastructure.ParametersRef != nil {
			ref := gw.Spec.Infrastructure.ParametersRef
			if string(ref.Group) == meridio2v1alpha1.GroupVersion.Group &&
				string(ref.Kind) == kindGatewayConfiguration &&
				ref.Name == gwConfig.Name {
				// Only include Gateways accepted by this controller
				if r.isGatewayAccepted(&gw) {
					gatewayKeys = append(gatewayKeys, client.ObjectKeyFromObject(&gw))
				}
			}
		}
	}

	return r.findDGsReferencingGateways(ctx, gatewayKeys)
}
