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

// Package gatewayutil holds Meridio-2's own helpers for interpreting Gateway API objects,
// shared across controllers and consumers that must agree on what a Gateway's status conditions
// mean without depending on the Gateway reconciler package itself.
//
// Currently limited to Gateway status-condition interpretation (IsGatewayAcceptedByController,
// IsGatewayProgrammed). Gateway-reference resolution helpers (e.g. L34Route -> Gateway parentRef
// matching, currently duplicated across the gateway/distributiongroup/enc controllers) are
// expected to consolidate here as well.
package gatewayutil

import (
	"strings"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// GatewayAcceptedMessagePrefix is the fixed prefix used when constructing and matching the Accepted
// condition's Message for the controller that accepted a Gateway (see acceptedMessage in
// internal/controller/gateway/status.go for where a full message is built from this prefix).
//
// Exported as a shared constant, rather than left as separate literals in the writer
// (status.go) and reader (IsGatewayAcceptedByController below), specifically to keep IsGatewayAcceptedByController's
// suffix match anchored to "GatewayAcceptedMessagePrefix + controllerName" instead of a bare
// controllerName suffix: matching only strings.HasSuffix(message, controllerName) is fragile —
// controllerName is a user-configurable, unbounded string (see --controller-name), so a shorter
// controller name that happens to be a suffix of another one (e.g. "org/gateway-controller" is
// a suffix of "meridio-2.nordix.org/gateway-controller") would false-positive match. Anchoring
// on the full "<prefix><exact controllerName>" string removes that class of accidental
// collision (the message must end with this literal prefix followed by the exact
// controllerName, not just end with a substring that happens to match).
const GatewayAcceptedMessagePrefix = "Gateway accepted by "

// IsGatewayAcceptedByController reports whether gw has an Accepted=True condition that was set by
// controllerName specifically.
//
// Gateway API allows multiple controllers to interact with the same Gateway object (e.g. across
// a GatewayClass change), so checking Type/Status alone (as meta.IsStatusConditionTrue does) is
// not sufficient here — an Accepted=True condition set by a different controller must not be
// treated as "accepted by us". This package's callers rely on the Gateway reconciler encoding
// the accepting controller's name after GatewayAcceptedMessagePrefix (see
// internal/controller/gateway/status.go's acceptedMessage); there is currently no dedicated,
// machine-readable field for this on metav1.Condition (Reason is free-form too, and changing to
// use it would be a separate, larger change to the condition's Reason/Message contract with
// existing tests asserting on message content).
func IsGatewayAcceptedByController(gw *gatewayv1.Gateway, controllerName string) bool {
	want := GatewayAcceptedMessagePrefix + controllerName
	for _, cond := range gw.Status.Conditions {
		if cond.Type == string(gatewayv1.GatewayConditionAccepted) &&
			cond.Status == metav1.ConditionTrue &&
			strings.HasSuffix(cond.Message, want) {
			return true
		}
	}
	return false
}

// IsGatewayProgrammed reports whether gw's Programmed status condition is currently True. Unlike
// Accepted, Programmed is only ever set by the single controller that owns the Gateway (once
// Accepted), so no message-based ownership check is needed here — a plain
// meta.IsStatusConditionTrue is sufficient and preferred over a hand-rolled loop.
func IsGatewayProgrammed(gw *gatewayv1.Gateway) bool {
	return meta.IsStatusConditionTrue(gw.Status.Conditions, string(gatewayv1.GatewayConditionProgrammed))
}
