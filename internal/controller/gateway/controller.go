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
	"errors"
	"fmt"

	meridio2v1alpha1 "github.com/nordix/meridio-2/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// GatewayReconciler reconciles a Gateway object
type GatewayReconciler struct {
	client.Client
	Scheme           *runtime.Scheme
	ControllerName   string
	Namespace        string // Namespace to watch (empty = all namespaces)
	TemplatePath     string // Path to template directory (defaults to /templates)
	LBServiceAccount string // ServiceAccount name for LB Deployment pods
}

// TODO(RBAC): Split into namespace-scoped Role and cluster-scoped ClusterRole
// This allows principle of least privilege for namespace-scoped deployments.
//
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=gateways,verbs=get;list;watch
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=gateways/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=gatewayclasses,verbs=get;list;watch
// +kubebuilder:rbac:groups=meridio-2.nordix.org,resources=l34routes,verbs=get;list;watch
// +kubebuilder:rbac:groups=meridio-2.nordix.org,resources=gatewayconfigurations,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch

// Reconcile manages Gateway lifecycle
func (r *GatewayReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// 1. Fetch Gateway
	var gw gatewayv1.Gateway
	if err := r.Get(ctx, req.NamespacedName, &gw); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Verify no finalizers (design decision: use ownerReferences only)
	if !gw.DeletionTimestamp.IsZero() {
		err := fmt.Errorf("gateway %s/%s has DeletionTimestamp set but controller uses no finalizers (finalizers: %v) - resource may be stuck in Terminating state",
			gw.Namespace, gw.Name, gw.Finalizers)
		log.Error(err, "unexpected state detected - skipping reconciliation to avoid retry loop")
		return ctrl.Result{}, nil
	}

	// 2. Check if we should manage this Gateway (via GatewayClass)
	shouldManage, err := r.shouldManageGateway(ctx, &gw)
	if err != nil {
		return ctrl.Result{}, err
	}

	if !shouldManage {
		// Gateway is not managed by this controller (different GatewayClass).
		// This covers two cases:
		// 1. Gateway was never managed by us (different GatewayClass from the start)
		// 2. Gateway ownership transferred (user changed gatewayClassName)
		//
		// We cannot reliably distinguish between these cases, but we can check if
		// we previously set the Accepted condition and reset it to default if so.
		//
		// The LB Deployment (when we implement it) will have an ownerReference to
		// the Gateway, so:
		// - If Gateway is deleted → Kubernetes GC deletes Deployment
		// - If ownership transfers → new controller can delete/replace Deployment
		//
		// Gateway API strongly discourages changing gatewayClassName, so we don't
		// over-invest in this edge case.
		if isGatewayAcceptedByController(&gw, r.ControllerName) {
			log.Info("Gateway no longer managed by this controller, resetting Accepted status",
				"gatewayClass", gw.Spec.GatewayClassName)

			// Reset Accepted to default (best effort)
			// We keep Programmed as-is since it reflects actual data plane state
			if err := r.updateAcceptedStatus(ctx, &gw, metav1.ConditionUnknown, string(gatewayv1.GatewayReasonPending), messageWaitingForController); err != nil {
				if apierrors.IsConflict(err) {
					// Another controller might be taking over, that's fine
					return ctrl.Result{}, nil
				}
				log.Error(err, "failed to reset Accepted status")
			}
		}
		return ctrl.Result{}, nil
	}

	// 3. Validate Gateway configuration (fetch GatewayConfiguration, load template, validate)
	gwConfig, template, err := r.validateGateway(ctx, &gw)
	if err != nil {
		var valErr *validationError
		var tmplErr *templateError
		if errors.As(err, &valErr) || errors.As(err, &tmplErr) {
			if statusErr := r.updateAcceptedStatus(ctx, &gw, metav1.ConditionFalse,
				string(gatewayv1.GatewayReasonInvalidParameters), err.Error()); statusErr != nil {
				if apierrors.IsConflict(statusErr) {
					return ctrl.Result{Requeue: true}, nil
				}
				return ctrl.Result{}, statusErr
			}
			if errors.As(err, &tmplErr) {
				return ctrl.Result{}, err // Requeue with exponential backoff (allow hot-fixing)
			}
			return ctrl.Result{}, nil // Validation error: don't requeue, wait for user fix
		}
		return ctrl.Result{}, err // Transient error: retry
	}

	// 4. Set Accepted status condition
	if err := r.updateAcceptedStatus(ctx, &gw, metav1.ConditionTrue, string(gatewayv1.GatewayReasonAccepted), r.acceptedMessage()); err != nil {
		if apierrors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, err
	}

	// 5. Update status.addresses from L34Routes
	if err := r.updateAddressesFromRoutes(ctx, &gw); err != nil {
		if apierrors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, err
	}

	// 6. Reconcile LB Deployment (using pre-fetched gwConfig and template)
	if err := r.reconcileLBDeployment(ctx, &gw, gwConfig, template); err != nil {
		// Handle transient errors (requeue without setting Programmed=False)
		if apierrors.IsConflict(err) || apierrors.IsAlreadyExists(err) {
			return ctrl.Result{Requeue: true}, nil
		}

		// Set Programmed=False only for permanent errors (name collision, create failure)
		var permErr *permanentDeploymentError
		if errors.As(err, &permErr) {
			if statusErr := r.updateProgrammedStatus(ctx, &gw, metav1.ConditionFalse,
				string(gatewayv1.GatewayReasonInvalid), err.Error()); statusErr != nil {
				if apierrors.IsConflict(statusErr) {
					return ctrl.Result{Requeue: true}, nil
				}
			}
		}
		return ctrl.Result{}, err
	}

	// 7. Set Programmed=True status condition
	if err := r.updateProgrammedStatus(ctx, &gw, metav1.ConditionTrue,
		string(gatewayv1.GatewayReasonProgrammed), messageProgrammed); err != nil {
		if apierrors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *GatewayReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&gatewayv1.Gateway{}).
		Owns(&appsv1.Deployment{}).
		Watches(&gatewayv1.GatewayClass{}, handler.EnqueueRequestsFromMapFunc(r.mapGatewayClassToGateway)).
		Watches(&meridio2v1alpha1.L34Route{}, handler.EnqueueRequestsFromMapFunc(r.mapL34RouteToGateway)).
		Watches(&meridio2v1alpha1.GatewayConfiguration{}, handler.EnqueueRequestsFromMapFunc(r.mapGatewayConfigToGateway)).
		Named("gateway").
		Complete(r)
}
