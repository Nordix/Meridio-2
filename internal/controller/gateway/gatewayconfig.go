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

	meridio2v1alpha1 "github.com/nordix/meridio-2/api/v1alpha1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// mapGatewayConfigToGateway maps GatewayConfiguration changes to Gateway reconciliation requests
func (r *GatewayReconciler) mapGatewayConfigToGateway(ctx context.Context, obj client.Object) []ctrl.Request {
	gwConfig, ok := obj.(*meridio2v1alpha1.GatewayConfiguration)
	if !ok {
		return nil
	}

	// List all Gateways referencing this GatewayConfiguration
	var gwList gatewayv1.GatewayList
	listOpts := []client.ListOption{client.InNamespace(gwConfig.Namespace)}
	if err := r.List(ctx, &gwList, listOpts...); err != nil {
		return nil
	}

	requests := make([]ctrl.Request, 0, len(gwList.Items))
	for _, gw := range gwList.Items {
		// Check if Gateway references this GatewayConfiguration
		if gw.Spec.Infrastructure == nil || gw.Spec.Infrastructure.ParametersRef == nil {
			continue
		}

		ref := gw.Spec.Infrastructure.ParametersRef
		if string(ref.Group) != meridio2v1alpha1.GroupVersion.Group ||
			string(ref.Kind) != kindGatewayConfiguration ||
			ref.Name != gwConfig.Name {
			continue
		}

		requests = append(requests, ctrl.Request{
			NamespacedName: client.ObjectKeyFromObject(&gw),
		})
	}
	return requests
}
