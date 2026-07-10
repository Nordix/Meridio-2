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
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestBuildReadyCondition_WithEndpoints(t *testing.T) {
	cond := buildReadyCondition(true, 5, "")

	if cond.Type != conditionTypeReady {
		t.Errorf("Expected type %q, got %q", conditionTypeReady, cond.Type)
	}
	if cond.Status != metav1.ConditionTrue {
		t.Errorf("Expected status True, got %v", cond.Status)
	}
	if cond.Reason != reasonEndpointsAvailable {
		t.Errorf("Expected reason %q, got %q", reasonEndpointsAvailable, cond.Reason)
	}
	if cond.ObservedGeneration != 5 {
		t.Errorf("Expected generation 5, got %d", cond.ObservedGeneration)
	}
}

func TestBuildReadyCondition_NoEndpoints(t *testing.T) {
	cond := buildReadyCondition(false, 3, "")

	if cond.Status != metav1.ConditionFalse {
		t.Errorf("Expected status False, got %v", cond.Status)
	}
	if cond.Reason != reasonNoEndpoints {
		t.Errorf("Expected reason %q, got %q", reasonNoEndpoints, cond.Reason)
	}
	if cond.Message != messageNoEndpointsAvailable {
		t.Errorf("Expected default message, got %q", cond.Message)
	}
}

func TestBuildReadyCondition_NoEndpointsWithCustomMessage(t *testing.T) {
	customMsg := "Custom reason for no endpoints"
	cond := buildReadyCondition(false, 3, customMsg)

	if cond.Status != metav1.ConditionFalse {
		t.Errorf("Expected status False, got %v", cond.Status)
	}
	if cond.Reason != reasonNoEndpoints {
		t.Errorf("Expected reason %q, got %q", reasonNoEndpoints, cond.Reason)
	}
	if cond.Message != customMsg {
		t.Errorf("Expected custom message %q, got %q", customMsg, cond.Message)
	}
}

func TestBuildReadyCondition_MultipleGateways(t *testing.T) {
	cond := buildReadyCondition(false, 3, messageMultipleGateways)

	if cond.Type != conditionTypeReady {
		t.Errorf("Expected type %q, got %q", conditionTypeReady, cond.Type)
	}
	if cond.Status != metav1.ConditionFalse {
		t.Errorf("Expected status False, got %v", cond.Status)
	}
	if cond.Reason != reasonMultipleGateways {
		t.Errorf("Expected reason %q, got %q", reasonMultipleGateways, cond.Reason)
	}
	if cond.Message != messageMultipleGateways {
		t.Errorf("Expected message %q, got %q", messageMultipleGateways, cond.Message)
	}
}

func TestBuildCapacityCondition(t *testing.T) {
	info := &maglevCapacityInfo{excluded: 5, total: 37}

	cond := buildCapacityCondition(info, 10)

	if cond.Type != conditionTypeCapacityExceeded {
		t.Errorf("Expected type %q, got %q", conditionTypeCapacityExceeded, cond.Type)
	}
	if cond.Status != metav1.ConditionTrue {
		t.Errorf("Expected status True, got %v", cond.Status)
	}
	if cond.Reason != reasonMaglevCapacityExceeded {
		t.Errorf("Expected reason %q, got %q", reasonMaglevCapacityExceeded, cond.Reason)
	}
	if cond.ObservedGeneration != 10 {
		t.Errorf("Expected generation 10, got %d", cond.ObservedGeneration)
	}
	if !strings.Contains(cond.Message, "5/37 pods excluded") {
		t.Errorf("Message should contain pod counts, got: %q", cond.Message)
	}
	if !strings.Contains(cond.Message, "32 capacity") {
		t.Errorf("Message should contain capacity, got: %q", cond.Message)
	}
}
