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

package loadbalancer

import (
	"context"
	"errors"
	"fmt"
	"sync"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	meridio2v1alpha1 "github.com/nordix/meridio-2/api/v1alpha1"
	"github.com/nordix/meridio-2/internal/common/readiness"
	nftablesmanager "github.com/nordix/meridio-2/internal/nftables"
)

// Controller reconciles DistributionGroup resources to manage NFQLB instances.
//
// Architectural Pattern: Mirrors Kubernetes Service/kube-proxy model
// ┌─────────────────────────────────┬──────────────────────────────────────────────┐
// │ Kubernetes                      │ Meridio-2                                    │
// ├─────────────────────────────────┼──────────────────────────────────────────────┤
// │ Service (abstract LB)           │ DistributionGroup (abstract LB)              │
// │ EndpointSlice (backends)        │ LoadBalancerEndpointSlice (backends)         │
// │ kube-proxy (per-node agent)     │ LB controller (per-Gateway agent)            │
// │ Watches: Service (primary)      │ Watches: DistributionGroup (primary)         │
// │ Implements: iptables/ipvs       │ Implements: NFQLB (Maglev)                   │
// └─────────────────────────────────┴──────────────────────────────────────────────┘
//
// Design Decision: DistributionGroup as Primary Resource
// - Direct mapping: DistributionGroup → NFQLB instance (1:1)
// - Clear lifecycle: NFQLB instance lifecycle tied to DistributionGroup
// - Architectural consistency: Matches Service/kube-proxy pattern
// - Gateway filtering: Only reconciles DistributionGroups for this Gateway
// - Shared nftables: Single table for all DGs prevents packet re-injection
// - VIPs from Gateway: Gateway.status.addresses provides dynamic VIP set
type Controller struct {
	client.Client
	Scheme            *runtime.Scheme
	GatewayName       string
	GatewayNamespace  string
	NFQLB             nfqlbManager
	Readiness         *readiness.Manager
	NftManagerFactory func(queueNum, queueTotal uint16, nolbFwmark, notargetsFwmark uint32) (nftablesManager, error)

	mu          sync.Mutex
	instances   map[string]nfqlbInstance                         // key: DistributionGroup name
	nftManager  nftablesManager                                  // Shared nftables manager for all DGs
	targets     map[string]map[int]struct{}                      // key: DistributionGroup name -> active identifiers
	flows       map[string]map[string]*meridio2v1alpha1.L34Route // key: DistributionGroup name -> L34Route name
	currentVIPs []string                                         // Currently configured VIPs (to avoid redundant updates)
}

// nftablesManager interface for nftables operations
type nftablesManager interface {
	Setup() error
	SetVIPs(cidrs []string) error
	Cleanup() error
}

const (
	kindDistributionGroup = "DistributionGroup"
	groupGatewayAPI       = gatewayv1.GroupName
	kindGateway           = "Gateway"
	defaultBackendKind    = "Service"
)

// resolveBackendRefDG checks if a BackendRef points to a DistributionGroup.
// Returns the resolved ObjectKey and true if it is a DG reference, or an empty key and false otherwise.
// Applies Gateway API defaulting: Group defaults to "" (core), Kind defaults to "Service",
// Namespace defaults to routeNamespace.
func resolveBackendRefDG(backendRef gatewayv1.BackendRef, routeNamespace string) (client.ObjectKey, bool) {
	group := ""
	if backendRef.Group != nil {
		group = string(*backendRef.Group)
	}
	kind := defaultBackendKind
	if backendRef.Kind != nil {
		kind = string(*backendRef.Kind)
	}
	if group != meridio2v1alpha1.GroupVersion.Group || kind != kindDistributionGroup {
		return client.ObjectKey{}, false
	}
	namespace := routeNamespace
	if backendRef.Namespace != nil {
		namespace = string(*backendRef.Namespace)
	}
	return client.ObjectKey{Name: string(backendRef.Name), Namespace: namespace}, true
}

func (c *Controller) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logr := log.FromContext(ctx)

	// Defense-in-depth: this controller's cache and all its internal state
	// (c.instances/c.targets/c.flows, keyed by name only) are scoped to a single
	// namespace. A request for any other namespace cannot be a legitimate
	// DistributionGroup for this Gateway (namespaces are immutable in Kubernetes,
	// so this can only indicate a mis-constructed request, e.g. from an unvalidated
	// cross-namespace backendRef). Reject it before the Get call below, since a
	// NotFound here is treated as "deleted" and triggers destructive cleanup keyed
	// by name only, which could tear down an unrelated same-named DG in our namespace.
	if req.Namespace != c.GatewayNamespace {
		logr.V(1).Info("Ignoring reconcile request outside Gateway namespace",
			"requestNamespace", req.Namespace, "gatewayNamespace", c.GatewayNamespace)
		return ctrl.Result{}, nil
	}

	// Get DistributionGroup
	distGroup := &meridio2v1alpha1.DistributionGroup{}
	if err := c.Get(ctx, req.NamespacedName, distGroup); err != nil {
		if apierrors.IsNotFound(err) {
			// DistributionGroup deleted - cleanup NFQLB instance and nftables
			logr.Info("DistributionGroup deleted, cleaning up resources", "distGroup", req.Name)
			return c.cleanupDistributionGroup(ctx, req.Name)
		}
		return ctrl.Result{}, err
	}

	// Filter: Only reconcile DistributionGroups for this Gateway
	belongs, err := c.belongsToGateway(ctx, distGroup)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to check Gateway membership: %w", err)
	}
	if !belongs {
		// Check if we previously managed this DistributionGroup
		c.mu.Lock()
		_, wasManaged := c.instances[distGroup.Name]
		c.mu.Unlock()

		if wasManaged {
			// DistributionGroup moved to another Gateway - cleanup local resources
			logr.Info("DistributionGroup moved to another Gateway, cleaning up local resources",
				"distGroup", distGroup.Name,
				"gateway", c.GatewayName)
			return c.cleanupDistributionGroup(ctx, distGroup.Name)
		}

		logr.V(1).Info("DistributionGroup does not belong to this Gateway, skipping",
			"distGroup", distGroup.Name,
			"gateway", c.GatewayName)
		return ctrl.Result{}, nil
	}

	logr.Info("Reconciling DistributionGroup", "distGroup", distGroup.Name)

	// Reconcile NFQLB instance
	if err := c.reconcileNFQLBInstance(ctx, distGroup); err != nil {
		logr.Error(err, "Failed to reconcile NFQLB instance")
		return ctrl.Result{}, err
	}

	// Reconcile targets from LoadBalancerEndpointSlices
	if err := c.reconcileTargets(ctx, distGroup); err != nil {
		logr.Error(err, "Failed to reconcile targets")
		return ctrl.Result{}, err
	}

	// Reconcile flows from L34Routes
	if err := c.reconcileFlows(ctx, distGroup); err != nil {
		logr.Error(err, "Failed to reconcile flows")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// cleanupDistributionGroup removes all local resources for a DistributionGroup.
// Used when DG is deleted or moved to another Gateway.
func (c *Controller) cleanupDistributionGroup(ctx context.Context, distGroupName string) (ctrl.Result, error) {
	logr := log.FromContext(ctx)

	c.mu.Lock()
	defer c.mu.Unlock()

	var errs []error

	// Cleanup NFQLB service. Only drop our own tracking (c.instances/targets/flows)
	// once DeleteInstance actually succeeds — if it fails, the NFQLB layer keeps the
	// instance (and its fwmark offset) reserved for retry, so forgetting it here would
	// mean nothing ever calls DeleteInstance again for this DG (reconcileNFQLBInstance
	// only acts when the DG is absent from c.instances, and a deleted DG never
	// reappears to re-trigger creation).
	if _, exists := c.instances[distGroupName]; exists {
		logr.Info("Deleting NFQLB service", "distGroup", distGroupName)
		if err := c.NFQLB.DeleteInstance(ctx, distGroupName); err != nil {
			logr.Error(err, "Failed to delete NFQLB service, will retry", "distGroup", distGroupName)
			errs = append(errs, fmt.Errorf("failed to delete NFQLB service for %q: %w", distGroupName, err))
		} else {
			delete(c.instances, distGroupName)
			delete(c.targets, distGroupName)
			delete(c.flows, distGroupName)
		}
	}

	// Note: nftables manager is shared, not cleaned up per-DG

	// Remove readiness file. Attempted independently of the NFQLB cleanup above —
	// a stuck DeleteInstance must not keep this DG's VIP advertised via BGP
	// (readiness.Manager.IsReady() is keyed off file presence) while cleanup retries.
	if err := c.Readiness.Remove(distGroupName); err != nil {
		logr.Error(err, "Failed to remove readiness file", "distGroup", distGroupName)
		errs = append(errs, fmt.Errorf("failed to remove readiness file for %q: %w", distGroupName, err))
	}

	if len(errs) > 0 {
		return ctrl.Result{}, errors.Join(errs...)
	}

	return ctrl.Result{}, nil
}

// belongsToGateway checks if a DistributionGroup belongs to this Gateway
// by checking if any L34Route references both this Gateway and this DistributionGroup
func (c *Controller) belongsToGateway(ctx context.Context, distGroup *meridio2v1alpha1.DistributionGroup) (bool, error) {
	// 1. Direct parentRefs: DistributionGroup.spec.parentRefs → Gateway
	for _, parentRef := range distGroup.Spec.ParentRefs {
		group := groupGatewayAPI
		if parentRef.Group != nil {
			group = *parentRef.Group
		}
		kind := kindGateway
		if parentRef.Kind != nil {
			kind = *parentRef.Kind
		}
		namespace := distGroup.Namespace
		if parentRef.Namespace != nil {
			namespace = *parentRef.Namespace
		}
		if group == groupGatewayAPI && kind == kindGateway &&
			parentRef.Name == c.GatewayName && namespace == c.GatewayNamespace {
			return true, nil
		}
	}

	// 2. Indirect via L34Route: L34Route.parentRefs → Gateway AND L34Route.backendRefs → DG
	l34routeList := &meridio2v1alpha1.L34RouteList{}
	if err := c.List(ctx, l34routeList, client.InNamespace(c.GatewayNamespace)); err != nil {
		return false, fmt.Errorf("failed to list L34Routes: %w", err)
	}

	for i := range l34routeList.Items {
		route := &l34routeList.Items[i]

		// Check if route references this Gateway
		if !c.referencesGateway(route) {
			continue
		}

		// Check if route references this DistributionGroup
		for _, backendRef := range route.Spec.BackendRefs {
			key, isDG := resolveBackendRefDG(backendRef, route.Namespace)
			if isDG && key.Name == distGroup.Name && key.Namespace == distGroup.Namespace {
				return true, nil
			}
		}
	}

	return false, nil
}

// gatewayEnqueue enqueues reconcile requests for all DistributionGroups when Gateway status changes
func (c *Controller) gatewayEnqueue(ctx context.Context, obj client.Object) []ctrl.Request {
	// Only process our Gateway
	if obj.GetName() != c.GatewayName || obj.GetNamespace() != c.GatewayNamespace {
		return nil
	}

	enqueued, err := c.listOwnedDistributionGroupKeys(ctx)
	if err != nil {
		log.FromContext(ctx).Error(err, "Failed to list DistributionGroups in gatewayEnqueue")
	}

	requests := make([]ctrl.Request, 0, len(enqueued))
	for key := range enqueued {
		requests = append(requests, ctrl.Request{NamespacedName: key})
	}

	return requests
}

// listOwnedDistributionGroupKeys returns the set of DistributionGroup ObjectKeys that
// belong to this Gateway, via direct spec.parentRefs or indirectly through an L34Route.
// Both lists are attempted independently so a failure in one does not suppress results
// from the other; the returned error (if any) is the combination of both failures, with
// whatever partial results were gathered already included in the returned map.
func (c *Controller) listOwnedDistributionGroupKeys(ctx context.Context) (map[client.ObjectKey]struct{}, error) {
	enqueued := make(map[client.ObjectKey]struct{})
	var errs []error

	// 1. Direct: DGs with spec.parentRefs pointing to this Gateway
	dgList := &meridio2v1alpha1.DistributionGroupList{}
	if err := c.List(ctx, dgList, client.InNamespace(c.GatewayNamespace)); err != nil {
		errs = append(errs, fmt.Errorf("failed to list DistributionGroups: %w", err))
	} else {
		for _, dg := range dgList.Items {
			for _, parentRef := range dg.Spec.ParentRefs {
				namespace := dg.Namespace
				if parentRef.Namespace != nil {
					namespace = *parentRef.Namespace
				}

				if parentRef.Name == c.GatewayName && namespace == c.GatewayNamespace {
					enqueued[client.ObjectKey{Name: dg.Name, Namespace: dg.Namespace}] = struct{}{}
					break
				}
			}
		}
	}

	// 2. Indirect: DGs referenced by L34Routes that point to this Gateway
	l34routeList := &meridio2v1alpha1.L34RouteList{}
	if err := c.List(ctx, l34routeList, client.InNamespace(c.GatewayNamespace)); err != nil {
		errs = append(errs, fmt.Errorf("failed to list L34Routes: %w", err))
	} else {
		for i := range l34routeList.Items {
			route := &l34routeList.Items[i]
			if !c.referencesGateway(route) {
				continue
			}
			for _, backendRef := range route.Spec.BackendRefs {
				if key, isDG := resolveBackendRefDG(backendRef, route.Namespace); isDG && key.Namespace == c.GatewayNamespace {
					enqueued[key] = struct{}{}
				}
			}
		}
	}

	return enqueued, errors.Join(errs...)
}

func (c *Controller) SetupWithManager(mgr ctrl.Manager) error {
	// Initialize shared nftables manager
	var err error
	nolb, notargets := c.NFQLB.DropFwmarks()
	if c.NftManagerFactory != nil {
		c.nftManager, err = c.NftManagerFactory(0, 4, uint32(nolb), uint32(notargets))
	} else {
		c.nftManager, err = nftablesmanager.NewManager(0, 4, uint32(nolb), uint32(notargets))
	}
	if err != nil {
		return fmt.Errorf("failed to create nftables manager: %w", err)
	}

	// Setup shared nftables table
	if err := c.nftManager.Setup(); err != nil {
		// Cleanup partially created resources
		_ = c.nftManager.Cleanup()
		return fmt.Errorf("failed to setup nftables: %w", err)
	}

	// Clean up readiness directory on startup
	if !c.Readiness.Enabled() {
		log.Log.Info("Readiness signaling disabled (--readiness-dir/MERIDIO_READINESS_DIR is empty), no readiness files will be written")
	}
	if err := c.Readiness.Cleanup(); err != nil {
		return fmt.Errorf("failed to cleanup readiness directory: %w", err)
	}

	// Register field indexer for LoadBalancerEndpointSlice lookups by DG name.
	// This enables client.MatchingFields{"spec.distributionGroupName": ...} in List calls.
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &meridio2v1alpha1.LoadBalancerEndpointSlice{},
		"spec.distributionGroupName",
		func(obj client.Object) []string {
			lbes := obj.(*meridio2v1alpha1.LoadBalancerEndpointSlice)
			return []string{lbes.Spec.DistributionGroupName}
		},
	); err != nil {
		return fmt.Errorf("failed to index LoadBalancerEndpointSlice by distributionGroupName: %w", err)
	}

	// Index by gatewayRef.name for efficient per-Gateway slice lookups for example in reconcileTargets.
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &meridio2v1alpha1.LoadBalancerEndpointSlice{},
		"spec.gatewayRef.name",
		func(obj client.Object) []string {
			lbes := obj.(*meridio2v1alpha1.LoadBalancerEndpointSlice)
			return []string{lbes.Spec.GatewayRef.Name}
		},
	); err != nil {
		return fmt.Errorf("failed to index LoadBalancerEndpointSlice by gatewayRef.name: %w", err)
	}

	// GC NFQLB shm segments left over from a previous process lifetime (container
	// restart) once per DistributionGroup deleted while this container was down —
	// nothing else ever revisits those segments, since both nfqlb.instances and
	// c.instances start empty on every startup. Runs once, after the manager's
	// caches have synced, so the "live" set reflects the full current DG list
	// rather than a partial one.
	//
	// Errors are logged, not returned: a Runnable's error is sent to the manager's
	// shared errChan and crashes the whole process (see controller-runtime
	// internal.go Start()). A GC failure (e.g. one unparseable or busy segment)
	// must not take down an otherwise-healthy load balancer — that would recreate
	// the exact kind of outage this sweep exists to prevent.
	if err := mgr.Add(manager.RunnableFunc(func(ctx context.Context) error {
		if !mgr.GetCache().WaitForCacheSync(ctx) {
			log.Log.Info("Cache sync did not complete, skipping NFQLB shm garbage collection")
			return nil
		}
		if err := c.gcStaleNFQLBInstances(ctx); err != nil {
			log.Log.Error(err, "NFQLB shm garbage collection failed; stale segments may remain until next restart")
		}
		return nil
	})); err != nil {
		return fmt.Errorf("failed to register NFQLB shm garbage collector: %w", err)
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&meridio2v1alpha1.DistributionGroup{}).
		Watches(&meridio2v1alpha1.LoadBalancerEndpointSlice{}, handler.EnqueueRequestsFromMapFunc(c.endpointSliceEnqueue)).
		Watches(&meridio2v1alpha1.L34Route{}, handler.EnqueueRequestsFromMapFunc(c.l34RouteEnqueue)).
		Watches(&gatewayv1.Gateway{}, handler.EnqueueRequestsFromMapFunc(c.gatewayEnqueue)).
		Named("loadbalancer").
		Complete(c)
}

// gcStaleNFQLBInstances lists the DistributionGroups currently owned by this Gateway
// and asks the NFQLB layer to remove any shm segment not in that set. See
// NFQueueLoadBalancer.GCStaleInstances for why this sweep is necessary.
func (c *Controller) gcStaleNFQLBInstances(ctx context.Context) error {
	keys, err := c.listOwnedDistributionGroupKeys(ctx)
	if err != nil {
		return fmt.Errorf("failed to list owned DistributionGroups for NFQLB shm garbage collection: %w", err)
	}

	keep := make(map[string]struct{}, len(keys))
	for key := range keys {
		keep[key.Name] = struct{}{}
	}

	return c.NFQLB.GCStaleInstances(ctx, keep)
}

// endpointSliceEnqueue maps LoadBalancerEndpointSlice events to DistributionGroup reconcile requests
func (c *Controller) endpointSliceEnqueue(ctx context.Context, obj client.Object) []ctrl.Request {
	lbeps, ok := obj.(*meridio2v1alpha1.LoadBalancerEndpointSlice)
	if !ok {
		return nil
	}

	// Only trigger if in our namespace
	if obj.GetNamespace() != c.GatewayNamespace {
		return nil
	}

	// Skip slices not scoped to this Gateway
	if lbeps.Spec.GatewayRef.Name != c.GatewayName || lbeps.Spec.GatewayRef.Namespace != c.GatewayNamespace {
		return nil
	}

	// Find owning DG via ownerReference
	for _, ownerRef := range obj.GetOwnerReferences() {
		if ownerRef.APIVersion == meridio2v1alpha1.GroupVersion.String() &&
			ownerRef.Kind == kindDistributionGroup &&
			ownerRef.Controller != nil && *ownerRef.Controller {
			return []ctrl.Request{{
				NamespacedName: client.ObjectKey{
					Name:      ownerRef.Name,
					Namespace: obj.GetNamespace(),
				},
			}}
		}
	}

	return nil
}

// l34RouteEnqueue maps L34Route events to DistributionGroup reconcile requests
func (c *Controller) l34RouteEnqueue(ctx context.Context, obj client.Object) []ctrl.Request {
	route, ok := obj.(*meridio2v1alpha1.L34Route)
	if !ok {
		return nil
	}

	// Check if route references this Gateway
	if !c.referencesGateway(route) {
		return nil
	}

	// Enqueue all DistributionGroups referenced by this route
	var requests []ctrl.Request
	for _, backendRef := range route.Spec.BackendRefs {
		if key, isDG := resolveBackendRefDG(backendRef, route.Namespace); isDG && key.Namespace == c.GatewayNamespace {
			requests = append(requests, ctrl.Request{NamespacedName: key})
		}
	}

	return requests
}
