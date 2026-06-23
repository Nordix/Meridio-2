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

	meridio2v1alpha1 "github.com/nordix/meridio-2/api/v1alpha1"
	"github.com/nordix/meridio-2/internal/common/constants"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// mapDGToPods: DG changed → find Pods matching DG.spec.selector → enqueue those Pods.
func (r *Reconciler) mapDGToPods(ctx context.Context, obj client.Object) []ctrl.Request {
	dg, ok := obj.(*meridio2v1alpha1.DistributionGroup)
	if !ok {
		return nil
	}
	return r.podsForSelector(ctx, dg.Namespace, dg.Spec.Selector)
}

// mapGatewayToPods: Gateway changed → find DGs referencing this Gateway →
// find Pods matching those DGs → enqueue those Pods.
func (r *Reconciler) mapGatewayToPods(ctx context.Context, obj client.Object) []ctrl.Request {
	gw, ok := obj.(*gatewayv1.Gateway)
	if !ok {
		return nil
	}
	return r.podsForGatewayKeys(ctx, []client.ObjectKey{client.ObjectKeyFromObject(gw)})
}

// mapL34RouteToPods: L34Route changed → find Gateways from parentRefs →
// find DGs → find Pods.
func (r *Reconciler) mapL34RouteToPods(ctx context.Context, obj client.Object) []ctrl.Request {
	route, ok := obj.(*meridio2v1alpha1.L34Route)
	if !ok {
		return nil
	}

	gwKeys := make([]client.ObjectKey, 0, len(route.Spec.ParentRefs))
	for _, ref := range route.Spec.ParentRefs {
		group := gatewayv1.GroupName
		if ref.Group != nil {
			group = string(*ref.Group)
		}
		kind := kindGateway
		if ref.Kind != nil {
			kind = string(*ref.Kind)
		}
		if group != gatewayv1.GroupName || kind != kindGateway {
			continue
		}
		ns := route.Namespace
		if ref.Namespace != nil {
			ns = string(*ref.Namespace)
		}
		gwKeys = append(gwKeys, client.ObjectKey{Namespace: ns, Name: string(ref.Name)})
	}
	return r.podsForGatewayKeys(ctx, gwKeys)
}

// mapGatewayConfigToPods: GatewayConfiguration changed → find Gateways referencing it →
// find DGs → find Pods.
func (r *Reconciler) mapGatewayConfigToPods(ctx context.Context, obj client.Object) []ctrl.Request {
	gwConfig, ok := obj.(*meridio2v1alpha1.GatewayConfiguration)
	if !ok {
		return nil
	}

	var gwList gatewayv1.GatewayList
	if err := r.List(ctx, &gwList, client.InNamespace(gwConfig.Namespace)); err != nil {
		return nil
	}

	var gwKeys []client.ObjectKey
	for _, gw := range gwList.Items {
		if gw.Spec.Infrastructure == nil || gw.Spec.Infrastructure.ParametersRef == nil {
			continue
		}
		ref := gw.Spec.Infrastructure.ParametersRef
		if string(ref.Group) == meridio2v1alpha1.GroupVersion.Group &&
			string(ref.Kind) == kindGatewayConfiguration &&
			ref.Name == gwConfig.Name {
			gwKeys = append(gwKeys, client.ObjectKeyFromObject(&gw))
		}
	}
	return r.podsForGatewayKeys(ctx, gwKeys)
}

// mapSLLBRPodToPods: SLLBR Pod changed (IP assigned/removed) → extract Gateway name from label →
// find DGs → find application Pods.
// Only called for Pods with gateway.networking.k8s.io/gateway-name label (filtered by predicate).
func (r *Reconciler) mapSLLBRPodToPods(ctx context.Context, obj client.Object) []ctrl.Request {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return nil
	}
	gwName, exists := pod.Labels[constants.LabelGatewayName]
	if !exists {
		return nil
	}
	return r.podsForGatewayKeys(ctx, []client.ObjectKey{
		{Namespace: pod.Namespace, Name: gwName},
	})
}

// podsForGatewayKeys finds all Pods affected by changes to the given Gateways.
// Gateway → (direct DG parentRef + indirect L34Route→DG) → Pod selector match.
func (r *Reconciler) podsForGatewayKeys(ctx context.Context, gwKeys []client.ObjectKey) []ctrl.Request {
	if len(gwKeys) == 0 {
		return nil
	}

	gwSet := make(map[client.ObjectKey]bool, len(gwKeys))
	for _, k := range gwKeys {
		gwSet[k] = true
	}

	listOpts := []client.ListOption{}
	if r.Namespace != "" {
		listOpts = append(listOpts, client.InNamespace(r.Namespace))
	}

	// Find L34Routes referencing these Gateways → collect DG keys from backendRefs
	var routeList meridio2v1alpha1.L34RouteList
	if err := r.List(ctx, &routeList, listOpts...); err != nil {
		return nil
	}

	dgKeys := make(map[client.ObjectKey]bool)
	for _, route := range routeList.Items {
		if !routeReferencesGateway(route, gwSet) {
			continue
		}
		for _, ref := range route.Spec.BackendRefs {
			if backendRefMatchesDGKind(ref) {
				ns := route.Namespace
				if ref.Namespace != nil {
					ns = string(*ref.Namespace)
				}
				dgKeys[client.ObjectKey{Namespace: ns, Name: string(ref.Name)}] = true
			}
		}
	}

	// Find DGs with direct parentRefs to these Gateways
	var dgList meridio2v1alpha1.DistributionGroupList
	if err := r.List(ctx, &dgList, listOpts...); err != nil {
		return nil
	}

	for _, dg := range dgList.Items {
		for _, ref := range dg.Spec.ParentRefs {
			group := gatewayv1.GroupName
			if ref.Group != nil {
				group = *ref.Group
			}
			kind := kindGateway
			if ref.Kind != nil {
				kind = *ref.Kind
			}
			if group != gatewayv1.GroupName || kind != kindGateway {
				continue
			}
			ns := dg.Namespace
			if ref.Namespace != nil {
				ns = *ref.Namespace
			}
			if gwSet[client.ObjectKey{Namespace: ns, Name: ref.Name}] {
				dgKeys[client.ObjectKeyFromObject(&dg)] = true
				break
			}
		}
	}

	// For each DG, find Pods matching its selector
	podSet := make(map[client.ObjectKey]bool)
	for _, dg := range dgList.Items {
		if !dgKeys[client.ObjectKeyFromObject(&dg)] {
			continue
		}
		for _, req := range r.podsForSelector(ctx, dg.Namespace, dg.Spec.Selector) {
			podSet[req.NamespacedName] = true
		}
	}

	requests := make([]ctrl.Request, 0, len(podSet))
	for key := range podSet {
		requests = append(requests, ctrl.Request{NamespacedName: key})
	}
	return requests
}

// podsForSelector lists Pods matching a label selector in the given namespace.
func (r *Reconciler) podsForSelector(ctx context.Context, ns string, ls *metav1.LabelSelector) []ctrl.Request {
	if ls == nil {
		return nil // nil selector = match nothing (DG controller convention)
	}
	selector, err := metav1.LabelSelectorAsSelector(ls)
	if err != nil {
		return nil
	}

	var podList corev1.PodList
	if err := r.List(ctx, &podList, client.InNamespace(ns), client.MatchingLabelsSelector{Selector: selector}); err != nil {
		return nil
	}

	requests := make([]ctrl.Request, 0, len(podList.Items))
	for _, pod := range podList.Items {
		requests = append(requests, ctrl.Request{
			NamespacedName: client.ObjectKeyFromObject(&pod),
		})
	}
	return requests
}

// routeReferencesGateway checks if an L34Route has a parentRef to any Gateway in the set.
func routeReferencesGateway(route meridio2v1alpha1.L34Route, gwSet map[client.ObjectKey]bool) bool {
	for _, ref := range route.Spec.ParentRefs {
		group := gatewayv1.GroupName
		if ref.Group != nil {
			group = string(*ref.Group)
		}
		kind := kindGateway
		if ref.Kind != nil {
			kind = string(*ref.Kind)
		}
		if group != gatewayv1.GroupName || kind != kindGateway {
			continue
		}
		ns := route.Namespace
		if ref.Namespace != nil {
			ns = string(*ref.Namespace)
		}
		if gwSet[client.ObjectKey{Namespace: ns, Name: string(ref.Name)}] {
			return true
		}
	}
	return false
}

// backendRefMatchesDGKind checks if a BackendRef points to a DistributionGroup.
func backendRefMatchesDGKind(ref gatewayv1.BackendRef) bool {
	if ref.Group == nil || ref.Kind == nil {
		return false
	}
	return string(*ref.Group) == meridio2v1alpha1.GroupVersion.Group &&
		string(*ref.Kind) == kindDistributionGroup
}
