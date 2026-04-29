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
	"fmt"

	meridio2v1alpha1 "github.com/nordix/meridio-2/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// Reconciler reconciles Pods to create/update/delete EndpointNetworkConfiguration resources.
//
// Runs inside the controller-manager. For each Pod, resolves which Gateways the Pod
// participates in (via DistributionGroup selector → L34Route → Gateway chain), computes
// the desired network state (VIPs, next-hops, network identity), and writes it to an ENC
// resource with the same name as the Pod.
//
// The ENC is consumed by the sidecar controller (internal/controller/sidecar/) which
// applies VIPs, policy routing rules, and ECMP routes to the Pod's network namespace.
type Reconciler struct {
	client.Client
	Scheme         *runtime.Scheme
	ControllerName string
	Namespace      string // Namespace to watch (empty = all namespaces)

	// IPScraper extracts secondary IP from Pod for a given network context.
	// Injected for testing. Defaults to defaultIPScraper if nil.
	IPScraper func(pod *corev1.Pod, cidr, attachmentType string) string
}

// RBAC: See config/rbac/manager-role.yaml and manager-clusterrole.yaml for required permissions.

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// 1. Fetch Pod
	var pod corev1.Pod
	if err := r.Get(ctx, req.NamespacedName, &pod); err != nil {
		if apierrors.IsNotFound(err) {
			// Pod deleted → ENC garbage-collected via ownerReference
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// 2. Skip non-running Pods (clean up ENC if one exists)
	if pod.Status.Phase != corev1.PodRunning {
		return ctrl.Result{}, r.deleteENCIfExists(ctx, req.NamespacedName)
	}

	// 3. Resolve Pod → Gateways (via DG selector matching + L34Route chain)
	gatewayConnections, err := r.resolveGatewayConnections(ctx, &pod)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to resolve gateway connections: %w", err)
	}

	log.V(1).Info("resolved gateway connections", "pod", pod.Name, "gateways", len(gatewayConnections))

	// 4. If no gateway connections, delete ENC if it exists
	if len(gatewayConnections) == 0 {
		return ctrl.Result{}, r.deleteENCIfExists(ctx, req.NamespacedName)
	}

	// 5. Create or update ENC
	return ctrl.Result{}, r.reconcileENC(ctx, &pod, gatewayConnections)
}

// SetupWithManager sets up the controller with the Manager.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Pod{}).
		Owns(&meridio2v1alpha1.EndpointNetworkConfiguration{}).
		Watches(&meridio2v1alpha1.DistributionGroup{},
			handler.EnqueueRequestsFromMapFunc(r.mapDGToPods)).
		Watches(&gatewayv1.Gateway{},
			handler.EnqueueRequestsFromMapFunc(r.mapGatewayToPods)).
		Watches(&meridio2v1alpha1.L34Route{},
			handler.EnqueueRequestsFromMapFunc(r.mapL34RouteToPods)).
		Watches(&meridio2v1alpha1.GatewayConfiguration{},
			handler.EnqueueRequestsFromMapFunc(r.mapGatewayConfigToPods)).
		// SLLBR Pod watch: triggers when SLLBR Pods get/lose secondary network IPs.
		// Filtered by gateway-name label to avoid processing unrelated Pods.
		Watches(&corev1.Pod{},
			handler.EnqueueRequestsFromMapFunc(r.mapSLLBRPodToPods),
			builder.WithPredicates(predicate.NewPredicateFuncs(func(obj client.Object) bool {
				_, ok := obj.GetLabels()[labelGatewayName]
				return ok
			}))).
		Named("endpointnetworkconfiguration").
		Complete(r)
}

// reconcileENC creates or updates the EndpointNetworkConfiguration for a Pod.
func (r *Reconciler) reconcileENC(ctx context.Context, pod *corev1.Pod, connections []meridio2v1alpha1.GatewayConnection) error {
	desired := &meridio2v1alpha1.EndpointNetworkConfiguration{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pod.Name,
			Namespace: pod.Namespace,
		},
		Spec: meridio2v1alpha1.EndpointNetworkConfigurationSpec{
			Gateways: connections,
		},
	}

	var existing meridio2v1alpha1.EndpointNetworkConfiguration
	err := r.Get(ctx, client.ObjectKeyFromObject(desired), &existing)
	if apierrors.IsNotFound(err) {
		if err := ctrl.SetControllerReference(pod, desired, r.Scheme); err != nil {
			return fmt.Errorf("failed to set owner reference: %w", err)
		}
		return r.Create(ctx, desired)
	}
	if err != nil {
		return err
	}

	if !apiequality.Semantic.DeepEqual(existing.Spec, desired.Spec) {
		existing.Spec = desired.Spec
		return r.Update(ctx, &existing)
	}

	return nil
}

// deleteENCIfExists deletes the ENC for a Pod if it exists.
func (r *Reconciler) deleteENCIfExists(ctx context.Context, key client.ObjectKey) error {
	var enc meridio2v1alpha1.EndpointNetworkConfiguration
	if err := r.Get(ctx, key, &enc); err != nil {
		return client.IgnoreNotFound(err)
	}
	return r.Delete(ctx, &enc)
}
