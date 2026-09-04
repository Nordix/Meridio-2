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

package gatewayutil

import (
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func TestIsGatewayAcceptedByController(t *testing.T) {
	t.Run("AcceptedByController", func(t *testing.T) {
		gw := &gatewayv1.Gateway{
			Status: gatewayv1.GatewayStatus{
				Conditions: []metav1.Condition{
					{
						Type:    string(gatewayv1.GatewayConditionAccepted),
						Status:  metav1.ConditionTrue,
						Message: GatewayAcceptedMessagePrefix + "test-controller",
					},
				},
			},
		}
		assert.True(t, IsGatewayAcceptedByController(gw, "test-controller"))
	})

	t.Run("AcceptedByDifferentController", func(t *testing.T) {
		gw := &gatewayv1.Gateway{
			Status: gatewayv1.GatewayStatus{
				Conditions: []metav1.Condition{
					{
						Type:    string(gatewayv1.GatewayConditionAccepted),
						Status:  metav1.ConditionTrue,
						Message: GatewayAcceptedMessagePrefix + "other-controller",
					},
				},
			},
		}
		assert.False(t, IsGatewayAcceptedByController(gw, "test-controller"))
	})

	// Regression test: controllerName must be matched as the exact remainder after
	// GatewayAcceptedMessagePrefix, not merely as a bare suffix of the whole message. Before this was
	// anchored to the prefix, a shorter controller name that happened to be a suffix of a
	// longer, different one (e.g. "org/gateway-controller" is a suffix of
	// "meridio-2.nordix.org/gateway-controller") would false-positive match.
	t.Run("ShorterControllerNameSuffixOfDifferentController_NotAccepted", func(t *testing.T) {
		gw := &gatewayv1.Gateway{
			Status: gatewayv1.GatewayStatus{
				Conditions: []metav1.Condition{
					{
						Type:    string(gatewayv1.GatewayConditionAccepted),
						Status:  metav1.ConditionTrue,
						Message: GatewayAcceptedMessagePrefix + "meridio-2.nordix.org/gateway-controller",
					},
				},
			},
		}
		assert.False(t, IsGatewayAcceptedByController(gw, "org/gateway-controller"))
		assert.True(t, IsGatewayAcceptedByController(gw, "meridio-2.nordix.org/gateway-controller"))
	})

	t.Run("NotAccepted", func(t *testing.T) {
		gw := &gatewayv1.Gateway{
			Status: gatewayv1.GatewayStatus{
				Conditions: []metav1.Condition{
					{
						Type:   string(gatewayv1.GatewayConditionAccepted),
						Status: metav1.ConditionFalse,
					},
				},
			},
		}
		assert.False(t, IsGatewayAcceptedByController(gw, "test-controller"))
	})

	t.Run("NoConditions", func(t *testing.T) {
		gw := &gatewayv1.Gateway{}
		assert.False(t, IsGatewayAcceptedByController(gw, "test-controller"))
	})

	t.Run("FoundNotAtIndex0", func(t *testing.T) {
		gw := &gatewayv1.Gateway{
			Status: gatewayv1.GatewayStatus{
				Conditions: []metav1.Condition{
					{
						Type:   string(gatewayv1.GatewayConditionProgrammed),
						Status: metav1.ConditionTrue,
					},
					{
						Type:    string(gatewayv1.GatewayConditionAccepted),
						Status:  metav1.ConditionTrue,
						Message: GatewayAcceptedMessagePrefix + "test-controller",
					},
				},
			},
		}
		assert.True(t, IsGatewayAcceptedByController(gw, "test-controller"))
	})
}

func TestIsGatewayProgrammed(t *testing.T) {
	t.Run("ProgrammedTrue", func(t *testing.T) {
		gw := &gatewayv1.Gateway{
			Status: gatewayv1.GatewayStatus{
				Conditions: []metav1.Condition{
					{
						Type:   string(gatewayv1.GatewayConditionProgrammed),
						Status: metav1.ConditionTrue,
					},
				},
			},
		}
		assert.True(t, IsGatewayProgrammed(gw))
	})

	t.Run("ProgrammedFalse", func(t *testing.T) {
		gw := &gatewayv1.Gateway{
			Status: gatewayv1.GatewayStatus{
				Conditions: []metav1.Condition{
					{
						Type:   string(gatewayv1.GatewayConditionProgrammed),
						Status: metav1.ConditionFalse,
					},
				},
			},
		}
		assert.False(t, IsGatewayProgrammed(gw))
	})

	t.Run("NoConditions", func(t *testing.T) {
		gw := &gatewayv1.Gateway{}
		assert.False(t, IsGatewayProgrammed(gw))
	})
}
