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

package router

import (
	"context"
	"fmt"
	"slices"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	bldr "sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/source"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	meridio2v1alpha1 "github.com/nordix/meridio-2/api/v1alpha1"
	"github.com/nordix/meridio-2/internal/bird"
	"github.com/nordix/meridio-2/internal/common/readiness"
)

// RouterReconciler reconciles a GatewayRouter object
type RouterReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// Name of the gateway in which this controller is running
	GatewayName string
	// Namespace of the gateway in which this controller is running
	GatewayNamespace string
	// BIRD instance
	Bird bird.BirdInterface
	// Readiness manages LB readiness gating.
	// If nil or Dir is empty, readiness gating is disabled (VIPs always advertised).
	Readiness *readiness.Manager
}

// RBAC for the router controller is managed via config/rbac/lb-serviceaccount.yaml
// (dedicated ServiceAccount/Role for LB Pods). No kubebuilder:rbac markers here.

// Reconcile implements the reconciliation of the Gateway for the router.
// This function is triggered by any change (create/update/delete) in any resource related
// to the object (GatewayRouter/Gateway).
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.22.4/pkg/reconcile
func (r *RouterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	if req.Name != r.GatewayName || req.Namespace != r.GatewayNamespace {
		return ctrl.Result{}, nil
	}

	gateway := &gatewayapiv1.Gateway{}
	err := r.Get(ctx, req.NamespacedName, gateway)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("failed to get gateway: %w", err)
	}

	if !gateway.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	gatewayRouters, err := r.getGatewayRouters(ctx, req.NamespacedName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to get gateway routers: %w", err)
	}

	passwords, ok := r.resolvePasswords(ctx, gatewayRouters)
	if !ok {
		log.Info("TCP-AO secret resolution incomplete, retaining current BIRD config")
		return ctrl.Result{}, nil
	}

	// Gateway API uses plain IPs; BIRD's vipsToCidr converts to CIDR notation
	vips := getVIPs(gateway)

	// Gate VIP advertisement on LB readiness
	if r.Readiness.Enabled() && !r.Readiness.IsReady() {
		log.Info("LB not ready, suppressing VIP advertisement")
		vips = nil
	}

	log.Info("Reconciling router", "vips", vips, "gatewayRouters", len(gatewayRouters))

	if err := r.Bird.Configure(ctx, vips, gatewayRouters, passwords); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to configure BIRD: %w", err)
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *RouterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	builder := ctrl.NewControllerManagedBy(mgr).
		For(&gatewayapiv1.Gateway{}).
		Watches(&meridio2v1alpha1.GatewayRouter{}, handler.EnqueueRequestsFromMapFunc(r.gatewayRouterEnqueue)).
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(r.secretEnqueue),
			bldr.WithPredicates(r.secretReferencedPredicate()))

	if r.Readiness.Enabled() {
		ch := make(chan event.GenericEvent, 1)
		_ = mgr.Add(manager.RunnableFunc(func(ctx context.Context) error {
			return r.watchLBReadinessDir(ctx, ch)
		}))
		builder = builder.WatchesRawSource(source.Channel(ch, &handler.EnqueueRequestForObject{}))
	} else {
		logf.Log.WithName("setup").Info("Readiness signaling disabled (--readiness-dir/MERIDIO_READINESS_DIR is empty), VIPs will be advertised without waiting for LB targets")
	}

	return builder.Named("gatewayrouter").Complete(r)
}

func makeNamespacedName(ref gatewayapiv1.ParentReference, defaultNs string) types.NamespacedName {
	ns := defaultNs
	if ref.Namespace != nil {
		ns = string(*ref.Namespace)
	}
	return types.NamespacedName{
		Name:      string(ref.Name),
		Namespace: ns,
	}
}

func (r *RouterReconciler) getGatewayRouters(ctx context.Context, gateway types.NamespacedName) ([]*meridio2v1alpha1.GatewayRouter, error) {
	list := &meridio2v1alpha1.GatewayRouterList{}
	err := r.List(ctx, list, client.InNamespace(gateway.Namespace))
	if err != nil {
		return nil, fmt.Errorf("failed to list gateway routers: %w", err)
	}

	result := make([]*meridio2v1alpha1.GatewayRouter, 0, len(list.Items))
	for i := range list.Items {
		ref := makeNamespacedName(list.Items[i].Spec.GatewayRef, gateway.Namespace)
		if ref == gateway {
			result = append(result, &list.Items[i])
		}
	}
	return result, nil
}

func getVIPs(gateway *gatewayapiv1.Gateway) []string {
	vips := make([]string, 0, len(gateway.Status.Addresses))
	seen := make(map[string]struct{})

	for _, addr := range gateway.Status.Addresses {
		if addr.Type == nil || *addr.Type != gatewayapiv1.IPAddressType {
			continue
		}
		if _, exists := seen[addr.Value]; !exists {
			vips = append(vips, addr.Value)
			seen[addr.Value] = struct{}{}
		}
	}

	// Sort for deterministic BIRD config generation: Gateway status addresses
	// are pre-sorted by the gateway controller, but sort defensively to avoid
	// config rewrites if that invariant is ever broken.
	slices.Sort(vips)

	return vips
}

func (r *RouterReconciler) gatewayRouterEnqueue(_ context.Context, obj client.Object) []ctrl.Request {
	gwr, ok := obj.(*meridio2v1alpha1.GatewayRouter)
	if !ok {
		return nil
	}

	ref := gwr.Spec.GatewayRef
	ns := gwr.Namespace
	if ref.Namespace != nil {
		ns = string(*ref.Namespace)
	}
	if string(ref.Name) != r.GatewayName || ns != r.GatewayNamespace {
		return nil
	}

	return []ctrl.Request{{NamespacedName: client.ObjectKey{Name: r.GatewayName, Namespace: r.GatewayNamespace}}}
}

// secretReferencedPredicate filters Secret events to only those referenced by
// a GatewayRouter's keychain that belongs to this controller's gateway.
// This prevents listing GatewayRouters on every unrelated Secret event.
func (r *RouterReconciler) secretReferencedPredicate() predicate.Funcs {
	matches := func(obj client.Object) bool {
		list := &meridio2v1alpha1.GatewayRouterList{}
		if err := r.List(context.Background(), list, client.InNamespace(obj.GetNamespace())); err != nil {
			return true
		}
		for i := range list.Items {
			gwr := &list.Items[i]
			if gwr.Spec.BGP == nil || gwr.Spec.BGP.Authentication == nil {
				continue
			}
			for _, key := range gwr.Spec.BGP.Authentication.Keychain {
				if key.SecretName == obj.GetName() {
					ref := gwr.Spec.GatewayRef
					ns := gwr.Namespace
					if ref.Namespace != nil {
						ns = string(*ref.Namespace)
					}
					if string(ref.Name) == r.GatewayName && ns == r.GatewayNamespace {
						return true
					}
				}
			}
		}
		return false
	}
	return predicate.Funcs{
		CreateFunc:  func(e event.CreateEvent) bool { return matches(e.Object) },
		UpdateFunc:  func(e event.UpdateEvent) bool { return matches(e.ObjectNew) },
		DeleteFunc:  func(e event.DeleteEvent) bool { return matches(e.Object) },
		GenericFunc: func(e event.GenericEvent) bool { return matches(e.Object) },
	}
}

func (r *RouterReconciler) secretEnqueue(_ context.Context, _ client.Object) []ctrl.Request {
	// The predicate already confirmed this secret is referenced by a GatewayRouter
	// belonging to our gateway — just trigger reconciliation.
	return []ctrl.Request{{NamespacedName: client.ObjectKey{Name: r.GatewayName, Namespace: r.GatewayNamespace}}}
}

// watchLBReadinessDir watches the LB readiness directory and sends a GenericEvent
// when the readiness state transitions.
func (r *RouterReconciler) watchLBReadinessDir(ctx context.Context, ch chan<- event.GenericEvent) error {
	log := logf.Log.WithName("lbwatcher")
	log.Info("watching LB readiness directory", "path", r.Readiness.Path())

	rch, err := r.Readiness.Watch(ctx)
	if err != nil {
		return fmt.Errorf("failed to watch LB readiness directory: %w", err)
	}

	// Each receive means a readiness state transition occurred.
	// Translate it into a GenericEvent to trigger a reconcile.
	// The loop exits when the channel is closed (context cancelled).
	for range rch {
		log.Info("LB readiness state changed", "ready", r.Readiness.IsReady())
		select {
		case ch <- event.GenericEvent{Object: &gatewayapiv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{
				Name:      r.GatewayName,
				Namespace: r.GatewayNamespace,
			},
		}}:
		default:
		}
	}

	return nil
}

func (r *RouterReconciler) getTcpAoSecret(ctx context.Context, namespace, name, key string) (string, error) {
	secret := &corev1.Secret{}
	err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, secret)
	if err != nil {
		return "", fmt.Errorf("failed to get secret %s/%s: %w", namespace, name, err)
	}

	value, ok := secret.Data[key]
	if !ok {
		return "", fmt.Errorf("key %s not found in secret %s/%s", key, namespace, name)
	}

	return string(value), nil
}

func (r *RouterReconciler) resolvePasswords(ctx context.Context, routers []*meridio2v1alpha1.GatewayRouter) (map[string]map[uint8]string, bool) {
	result := make(map[string]map[uint8]string)
	allResolved := true
	for _, router := range routers {
		if router.Spec.BGP == nil || router.Spec.BGP.Authentication == nil {
			continue
		}
		passwords := make(map[uint8]string)
		failed := false
		for _, key := range router.Spec.BGP.Authentication.Keychain {
			password, err := r.getTcpAoSecret(ctx, router.Namespace, key.SecretName, key.SecretKey)
			if err != nil {
				logf.FromContext(ctx).Info("Failed to fetch TCP-AO secret",
					"router", router.Name, "secretName", key.SecretName, "secretKey", key.SecretKey, "reason", err)
				failed = true
				break
			}
			passwords[key.SendId] = password
		}
		if failed {
			allResolved = false
		} else {
			result[router.Name] = passwords
		}
	}

	return result, allResolved
}
