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
	"maps"
	"sync"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	meridio2v1alpha1 "github.com/nordix/meridio-2/api/v1alpha1"
	"github.com/nordix/meridio-2/internal/common/readiness"
	lbmetrics "github.com/nordix/meridio-2/internal/metrics/loadbalancer"
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

	// ConfigErrors counts failed data-plane config operations by reason. Nil-safe: a typed-nil
	// *loadbalancer.ConfigErrors is a valid value whose Inc no-ops (see its doc). Assign only a
	// typed-nil when metrics are disabled, NEVER an untyped nil — an untyped nil would make this
	// interface field itself non-nil and panic on Inc. run.go sets it accordingly.
	ConfigErrors configErrorRecorder

	// PerTargetMetrics enables per-target packet counters (opt-in; --enable-per-target-metrics).
	// When false, only per-DG packet/byte counting is installed.
	PerTargetMetrics bool

	mu          sync.Mutex
	instances   map[string]nfqlbInstance                         // key: DistributionGroup name
	nftManager  nftablesManager                                  // Shared nftables manager for all DGs
	targets     map[string]map[int]struct{}                      // key: DistributionGroup name -> active identifiers
	flows       map[string]map[string]*meridio2v1alpha1.L34Route // key: DistributionGroup name -> L34Route name
	dgRanges    map[string]nftablesmanager.DGRange               // key: DistributionGroup name -> fwmark range (for per-DG counter labeling)
	currentVIPs []string                                         // Currently configured VIPs (to avoid redundant updates)
}

// nftablesManager interface for nftables operations
type nftablesManager interface {
	Setup() error
	SetVIPs(cidrs []string) error
	Cleanup() error
	// Counter-rule management for per-DG (and opt-in per-target) packet/byte accounting.
	ReconcileDGCounters(dg nftablesmanager.DGCounters, perTarget bool) error
	RemoveDGCounters(dg string, base, count int) error
	// PacketByteCounts reads per-DG (and, in per-target mode, per-target) forwarded totals from
	// the target-accounting chain. The DG<->fwmark-range mapping is passed in by the caller (the
	// controller owns it under c.mu); the Manager keeps no mutable range state of its own.
	PacketByteCounts(ranges map[string]nftablesmanager.DGRange) (perDG map[string]nftablesmanager.Counts, perTarget map[nftablesmanager.DGTarget]nftablesmanager.Counts, err error)
}

// configErrorRecorder records failed data-plane config operations by reason (see
// internal/metrics/loadbalancer.ConfigErrors). *loadbalancer.ConfigErrors satisfies it; a fake is
// used in tests. A typed-nil value is a valid no-op (Inc no-ops on a nil receiver), so the
// reconcile path calls it unconditionally while construction stays gated on metrics being enabled.
type configErrorRecorder interface {
	Inc(reason string)
}

// NftablesReader exposes the shared nftables manager as a metrics read source, if the concrete
// manager supports the read methods (it does; the test fake may not). Returns nil when the manager
// is absent or does not implement the reader — in which case run.go skips registering the nftables
// collector. Must be called after SetupWithManager has initialized the shared manager.
func (c *Controller) NftablesReader() lbmetrics.NftablesReader {
	if r, ok := c.nftManager.(lbmetrics.NftablesReader); ok {
		return r
	}
	return nil
}

// PacketByteReader exposes the per-DG packet/byte metrics read as a scrape-time source. It
// returns an adapter (not the raw manager) that, on each read, snapshots the controller's
// DG<->fwmark-range mapping under c.mu and passes it to the manager's PacketByteCounts. This is
// what keeps the range mapping race-free: the controller mutates it under c.mu in the reconcile
// path, and the scrape reads a locked snapshot — the Manager itself holds no range state.
// Returns nil when the manager does not support the read (e.g. a test fake); run.go then skips the
// packet/byte collector. Must be called after SetupWithManager.
func (c *Controller) PacketByteReader() lbmetrics.PacketByteReader {
	if _, ok := c.nftManager.(packetByteCounter); !ok {
		return nil
	}
	return &packetByteReadAdapter{c: c}
}

// packetByteCounter is the manager-side read the adapter delegates to (parameterized by the
// range mapping the controller owns).
type packetByteCounter interface {
	PacketByteCounts(ranges map[string]nftablesmanager.DGRange) (map[string]nftablesmanager.Counts, map[nftablesmanager.DGTarget]nftablesmanager.Counts, error)
}

// packetByteReadAdapter binds the controller's locked range snapshot to the manager's
// parameterized read, satisfying lbmetrics.PacketByteReader (arg-less).
type packetByteReadAdapter struct{ c *Controller }

func (a *packetByteReadAdapter) PacketByteCounts() (map[string]nftablesmanager.Counts, map[nftablesmanager.DGTarget]nftablesmanager.Counts, error) {
	// Snapshot the range mapping under c.mu so the concurrent reconcile writes cannot race the
	// read (a concurrent map read+write would crash the process).
	a.c.mu.Lock()
	ranges := make(map[string]nftablesmanager.DGRange, len(a.c.dgRanges))
	maps.Copy(ranges, a.c.dgRanges)
	counter := a.c.nftManager.(packetByteCounter)
	a.c.mu.Unlock()

	return counter.PacketByteCounts(ranges)
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
	if instance, exists := c.instances[distGroupName]; exists {
		logr.Info("Deleting NFQLB service", "distGroup", distGroupName)
		// Remove this DG's packet/byte counter rules + range mapping (shared table, so this is
		// per-DG cleanup, not a table teardown). Done before DeleteInstance frees the offset.
		base, count := instance.FwmarkRange()
		if err := c.nftManager.RemoveDGCounters(distGroupName, base, count); err != nil {
			logr.Error(err, "Failed to remove packet/byte counter rules", "distGroup", distGroupName)
		}
		delete(c.dgRanges, distGroupName) // under c.mu; drop this DG's range from the metric read
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

	// Deduplicate: a DG may be referenced both directly and via L34Route
	enqueued := make(map[client.ObjectKey]struct{})

	// 1. Direct: DGs with spec.parentRefs pointing to this Gateway
	dgList := &meridio2v1alpha1.DistributionGroupList{}
	if err := c.List(ctx, dgList, client.InNamespace(c.GatewayNamespace)); err != nil {
		log.FromContext(ctx).Error(err, "Failed to list DistributionGroups in gatewayEnqueue")
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
		log.FromContext(ctx).Error(err, "Failed to list L34Routes in gatewayEnqueue, returning partial results")
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

	requests := make([]ctrl.Request, 0, len(enqueued))
	for key := range enqueued {
		requests = append(requests, ctrl.Request{NamespacedName: key})
	}

	return requests
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

	return ctrl.NewControllerManagedBy(mgr).
		For(&meridio2v1alpha1.DistributionGroup{}).
		Watches(&meridio2v1alpha1.LoadBalancerEndpointSlice{}, handler.EnqueueRequestsFromMapFunc(c.endpointSliceEnqueue)).
		Watches(&meridio2v1alpha1.L34Route{}, handler.EnqueueRequestsFromMapFunc(c.l34RouteEnqueue)).
		Watches(&gatewayv1.Gateway{}, handler.EnqueueRequestsFromMapFunc(c.gatewayEnqueue)).
		Named("loadbalancer").
		Complete(c)
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
