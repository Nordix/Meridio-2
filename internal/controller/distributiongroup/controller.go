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

// Package distributiongroup implements the DistributionGroup controller.
//
// The controller manages EndpointSlices for secondary network endpoints, enabling
// L3/L4 load balancing across multi-network Pods. It supports multiple distribution
// strategies (currently Maglev consistent hashing).
//
// Key responsibilities:
//   - Watch DistributionGroups, Pods, Gateways, L34Routes, and GatewayConfigurations
//   - Extract secondary network IPs from Pods (currently only via Multus annotations)
//   - Create/update EndpointSlices with network-specific endpoints
//   - Assign stable Maglev IDs for consistent hashing (when Type=Maglev)
//   - Enforce capacity limits and report status conditions
//
// Network context is derived from Gateway→GatewayConfiguration references, which
// specify the subnet CIDRs and attachment types (NAD/DRA) for secondary networks.
//
// See docs/controllers/distributiongroup.md for detailed architecture and design decisions.
package distributiongroup

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	meridio2v1alpha1 "github.com/nordix/meridio-2/api/v1alpha1"
)

// DistributionGroupReconciler reconciles a DistributionGroup object
type DistributionGroupReconciler struct {
	client.Client
	Scheme         *runtime.Scheme
	ControllerName string
	Namespace      string // Namespace to watch (empty = all namespaces)

	// IPScraper extracts secondary IP from Pod for a given network context
	// Defaults to defaultIPScraper if nil (for testing injection)
	IPScraper func(pod *corev1.Pod, cidr, attachmentType string) string
}

// TODO(resync): Consider adding configurable resync period.
// Manual EndpointSlice modifications are already handled via .Owns() watch.
// Periodic resync is a safety net for:
// - Missed watch events (network issues, controller restart)
// - Orphaned slices (ownerReference removed manually)
// - Cache inconsistencies
// Kubernetes EndpointSlice controller uses ~30min resync.
// Current: Uses controller-runtime default (10 hours).
// Future: Add --resync-period flag if faster drift recovery needed.

// TODO(RBAC): Split into namespace-scoped Role and cluster-scoped ClusterRole
// - Role: distributiongroups, endpointslices, pods, l34routes, gatewayconfigurations
// - ClusterRole: gateways, nodes
// This allows principle of least privilege for namespace-scoped deployments.
//
// +kubebuilder:rbac:groups=meridio-2.nordix.org,resources=distributiongroups,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=meridio-2.nordix.org,resources=distributiongroups/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=discovery.k8s.io,resources=endpointslices,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=gateways,verbs=get;list;watch
// +kubebuilder:rbac:groups=meridio-2.nordix.org,resources=l34routes,verbs=get;list;watch
// +kubebuilder:rbac:groups=meridio-2.nordix.org,resources=gatewayconfigurations,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch

// Reconcile manages EndpointSlices for a DistributionGroup
func (r *DistributionGroupReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	log.Info("reconciling DistributionGroup", "name", req.Name, "namespace", req.Namespace)

	// 1. Fetch DistributionGroup
	var dg meridio2v1alpha1.DistributionGroup
	if err := r.Get(ctx, req.NamespacedName, &dg); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// 2. Verify no finalizers (design decision: use ownerReferences only)
	if !dg.DeletionTimestamp.IsZero() {
		err := fmt.Errorf("distributionGroup %s/%s has DeletionTimestamp set but controller uses no finalizers (finalizers: %v) - resource may be stuck in Terminating state",
			dg.Namespace, dg.Name, dg.Finalizers)
		log.Error(err, "unexpected state detected - skipping reconciliation to avoid retry loop")
		return ctrl.Result{}, nil
	}

	// 3. List Pods matching DG.spec.selector (early exit optimization)
	pods, err := r.listMatchingPods(ctx, &dg)
	if err != nil {
		log.Error(err, "failed to list matching pods")
		return ctrl.Result{}, err
	}
	log.Info("found matching pods", "count", len(pods))

	// 4. If no Pods, delete all owned EndpointSlices and update status
	if len(pods) == 0 {
		if err := r.deleteAllOwnedSlices(ctx, &dg); err != nil {
			log.Error(err, "failed to delete owned endpointslices")
			return ctrl.Result{}, err
		}
		if err := r.updateStatus(ctx, &dg, false, nil, messageNoMatchingPods); err != nil {
			if apierrors.IsConflict(err) {
				return ctrl.Result{Requeue: true}, nil
			}
			log.Error(err, "failed to update status")
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// 5. List all Gateways referencing this DG (direct + indirect via L34Routes)
	allGateways, err := r.listReferencedGateways(ctx, &dg)
	if err != nil {
		log.Error(err, "failed to list referenced gateways")
		return ctrl.Result{}, err
	}
	log.Info("found referenced gateways", "count", len(allGateways))

	// 6. Filter Gateways by Accepted condition (only process Gateways controlled by us)
	var acceptedGateways []gatewayv1.Gateway
	for _, gw := range allGateways {
		if r.isGatewayAccepted(&gw) {
			acceptedGateways = append(acceptedGateways, gw)
		}
	}
	log.Info("found accepted gateways", "count", len(acceptedGateways))

	// 7. For each accepted Gateway, fetch GatewayConfiguration to determine network context
	networkContexts, err := r.getNetworkContexts(ctx, acceptedGateways)
	if err != nil {
		log.Error(err, "failed to get network contexts")
		return ctrl.Result{}, err
	}
	log.Info("extracted network contexts", "contexts", networkContexts)

	// 8. List existing EndpointSlices owned by this DG
	existingSlices, err := r.listOwnedSlices(ctx, &dg)
	if err != nil {
		log.Error(err, "failed to list owned endpointslices")
		return ctrl.Result{}, err
	}

	// 9. Calculate desired EndpointSlices
	desiredSlices, capacityInfo := r.calculateDesiredSlices(ctx, &dg, pods, networkContexts, existingSlices)

	// 10. Reconcile EndpointSlices (create/update/delete)
	if err := r.reconcileSlices(ctx, &dg, desiredSlices, existingSlices); err != nil {
		if apierrors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		log.Error(err, "failed to reconcile endpointslices")
		return ctrl.Result{}, err
	}

	// 11. Update DG status
	msg := ""
	if len(desiredSlices) == 0 {
		// Determine specific reason for no endpoints
		if len(allGateways) == 0 {
			msg = messageNoReferencedGateways
		} else if len(acceptedGateways) == 0 {
			msg = messageNoAcceptedGateways
		} else if len(networkContexts) == 0 {
			msg = messageNoNetworkContext
		}
		// If msg still empty, Pods probably have no secondary IPs (leave default message)
	}
	if err := r.updateStatus(ctx, &dg, len(desiredSlices) > 0, capacityInfo, msg); err != nil {
		if apierrors.IsConflict(err) {
			// Conflict is expected during concurrent reconciles (e.g., .Owns() watch triggers)
			// Controller-runtime will automatically retry with fresh resourceVersion
			return ctrl.Result{Requeue: true}, nil
		}
		log.Error(err, "failed to update status")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *DistributionGroupReconciler) SetupWithManager(mgr ctrl.Manager, enableTopology bool) error {
	builder := ctrl.NewControllerManagedBy(mgr).
		For(&meridio2v1alpha1.DistributionGroup{}).
		Owns(&discoveryv1.EndpointSlice{}).
		Watches(&corev1.Pod{}, handler.EnqueueRequestsFromMapFunc(r.mapPodToDistributionGroup)).
		Watches(&gatewayv1.Gateway{}, handler.EnqueueRequestsFromMapFunc(r.mapGatewayToDistributionGroup)).
		Watches(&meridio2v1alpha1.L34Route{}, handler.EnqueueRequestsFromMapFunc(r.mapL34RouteToDistributionGroup)).
		Watches(&meridio2v1alpha1.GatewayConfiguration{}, handler.EnqueueRequestsFromMapFunc(r.mapGatewayConfigToDistributionGroup)).
		Named("distributiongroup")

	if enableTopology {
		builder = builder.Watches(&corev1.Node{}, handler.EnqueueRequestsFromMapFunc(r.mapNodeToDistributionGroup))
	}

	return builder.Complete(r)
}
