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
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// findAcceptedConditionIndex returns the index of Accepted condition set by this controller, or -1
func findAcceptedConditionIndex(gw *gatewayv1.Gateway, controllerName string) int {
	for i, cond := range gw.Status.Conditions {
		if cond.Type == string(gatewayv1.GatewayConditionAccepted) &&
			cond.Status == metav1.ConditionTrue &&
			strings.HasSuffix(cond.Message, controllerName) {
			return i
		}
	}
	return -1
}

// isGatewayAcceptedByController checks if Gateway has Accepted=True condition set by this controller
// TODO: Move to internal/common/gatewayapi package - used by both Gateway and DistributionGroup controllers
func isGatewayAcceptedByController(gw *gatewayv1.Gateway, controllerName string) bool {
	return findAcceptedConditionIndex(gw, controllerName) >= 0
}
