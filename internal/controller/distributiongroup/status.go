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

package distributiongroup

import (
	"context"
	"fmt"
	"strings"

	meridio2v1alpha1 "github.com/nordix/meridio-2/api/v1alpha1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// updateStatus updates DistributionGroup status conditions
func (r *DistributionGroupReconciler) updateStatus(ctx context.Context, dg *meridio2v1alpha1.DistributionGroup, hasEndpoints bool, capacityInfo *maglevCapacityInfo, message string) error {
	changed := false

	// Set Ready condition
	readyCondition := buildReadyCondition(hasEndpoints, dg.Generation, message)
	changed = meta.SetStatusCondition(&dg.Status.Conditions, readyCondition) || changed

	// Handle CapacityExceeded condition (Maglev only)
	if capacityInfo != nil && len(capacityInfo.networkIssues) > 0 {
		capacityCondition := buildCapacityCondition(capacityInfo.networkIssues, dg.Generation)
		changed = meta.SetStatusCondition(&dg.Status.Conditions, capacityCondition) || changed
	} else {
		changed = meta.RemoveStatusCondition(&dg.Status.Conditions, conditionTypeCapacityExceeded) || changed
	}

	if changed {
		return r.Status().Update(ctx, dg)
	}
	return nil
}

// buildReadyCondition creates the Ready condition based on endpoint availability
func buildReadyCondition(hasEndpoints bool, generation int64, message string) metav1.Condition {
	condition := metav1.Condition{
		Type:               conditionTypeReady,
		ObservedGeneration: generation,
	}

	if hasEndpoints {
		condition.Status = metav1.ConditionTrue
		condition.Reason = reasonEndpointsAvailable
		condition.Message = messageEndpointsAvailable
	} else {
		condition.Status = metav1.ConditionFalse
		condition.Reason = reasonNoEndpoints
		if message != "" {
			condition.Message = message
		} else {
			condition.Message = messageNoEndpointsAvailable
		}
	}

	return condition
}

// buildCapacityCondition creates the CapacityExceeded condition
func buildCapacityCondition(issues map[string]struct{ excluded, total int32 }, generation int64) metav1.Condition {
	return metav1.Condition{
		Type:               conditionTypeCapacityExceeded,
		Status:             metav1.ConditionTrue,
		ObservedGeneration: generation,
		Reason:             reasonMaglevCapacityExceeded,
		Message:            buildCapacityMessage(issues),
	}
}

// buildCapacityMessage creates a human-readable message for capacity issues
func buildCapacityMessage(issues map[string]struct{ excluded, total int32 }) string {
	if len(issues) == 0 {
		return ""
	}

	parts := make([]string, 0, len(issues))
	for cidr, info := range issues {
		capacity := info.total - info.excluded
		parts = append(parts, fmt.Sprintf("%s: %d/%d pods excluded (%d capacity)",
			cidr, info.excluded, info.total, capacity))
	}

	msg := "Networks with capacity issues: " + strings.Join(parts, ", ")

	// Truncate if too long (keep under 250 chars for readability)
	if len(msg) > 250 {
		msg = msg[:247] + "..."
	}

	return msg
}
