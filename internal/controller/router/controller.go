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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	meridio2v1alpha1 "github.com/nordix/meridio-2/api/v1alpha1"
	"github.com/nordix/meridio-2/internal/bird"
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
}

// +kubebuilder:rbac:groups=meridio-2.nordix.org,resources=gatewayrouters,verbs=get;list;watch
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=gateways,verbs=get;list;watch

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

	// Gateway API uses plain IPs; BIRD's vipsToCidr converts to CIDR notation
	vips := getVIPs(gateway)

	log.Info("Reconciling router", "vips", vips, "gatewayRouters", len(gatewayRouters))

	if err := r.Bird.Configure(ctx, vips, gatewayRouters); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to configure BIRD: %w", err)
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *RouterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&gatewayapiv1.Gateway{}).
		Watches(&meridio2v1alpha1.GatewayRouter{}, handler.EnqueueRequestsFromMapFunc(r.gatewayRouterEnqueue)).
		Named("gatewayrouter").
		Complete(r)
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
