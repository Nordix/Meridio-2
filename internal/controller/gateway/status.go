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
	"fmt"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// updateAcceptedStatus sets the Accepted condition on the Gateway
func (r *GatewayReconciler) updateAcceptedStatus(ctx context.Context, gw *gatewayv1.Gateway, status metav1.ConditionStatus, reason, message string) error {
	condition := metav1.Condition{
		Type:               string(gatewayv1.GatewayConditionAccepted),
		Status:             status,
		ObservedGeneration: gw.Generation,
		Reason:             reason,
		Message:            message,
	}

	if meta.SetStatusCondition(&gw.Status.Conditions, condition) {
		return r.Status().Update(ctx, gw)
	}

	return nil
}

// updateProgrammedStatus sets the Programmed condition on the Gateway
func (r *GatewayReconciler) updateProgrammedStatus(ctx context.Context, gw *gatewayv1.Gateway, status metav1.ConditionStatus, reason, message string) error {
	condition := metav1.Condition{
		Type:               string(gatewayv1.GatewayConditionProgrammed),
		Status:             status,
		ObservedGeneration: gw.Generation,
		Reason:             reason,
		Message:            message,
	}

	if meta.SetStatusCondition(&gw.Status.Conditions, condition) {
		return r.Status().Update(ctx, gw)
	}

	return nil
}

// acceptedMessage returns the standard message for Accepted=True condition
func (r *GatewayReconciler) acceptedMessage() string {
	return fmt.Sprintf("Gateway accepted by %s", r.ControllerName)
}
