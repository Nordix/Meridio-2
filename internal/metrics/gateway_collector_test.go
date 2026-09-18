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

package metrics

import (
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/nordix/meridio-2/internal/common/gatewayutil"
)

// testControllerName is the controller name used across the collector tests
const testControllerName = "example.com/gateway-controller"

// acceptedCondition returns an Accepted=True condition whose Message matches what
// gatewayutil.IsGatewayAcceptedByController looks for, for the given controller name.
func acceptedCondition(controllerName string) metav1.Condition {
	return metav1.Condition{
		Type:               string(gatewayv1.GatewayConditionAccepted),
		Status:             metav1.ConditionTrue,
		Reason:             string(gatewayv1.GatewayReasonAccepted),
		Message:            gatewayutil.GatewayAcceptedMessagePrefix + controllerName,
		LastTransitionTime: metav1.Now(),
	}
}

func gcScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	_ = gatewayv1.Install(scheme)
	return scheme
}

func gcProgrammedCondition(status metav1.ConditionStatus) metav1.Condition {
	return metav1.Condition{
		Type:               string(gatewayv1.GatewayConditionProgrammed),
		Status:             status,
		Reason:             string(gatewayv1.GatewayReasonProgrammed),
		Message:            "programmed",
		LastTransitionTime: metav1.Now(),
	}
}

func TestGatewayCollector_CountAndProgrammed_AcceptedByUs(t *testing.T) {
	gw := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "gw-a", Namespace: "ns-a"},
		Status: gatewayv1.GatewayStatus{
			Conditions: []metav1.Condition{
				acceptedCondition(testControllerName),
				gcProgrammedCondition(metav1.ConditionTrue),
			},
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(gcScheme()).WithObjects(gw).Build()
	collector := NewGatewayCollector(fakeClient, alwaysSyncedWaiter{}, time.Second, "", testControllerName, "meridio_2")

	expected := `
# HELP meridio_2_gateway_count Number of Gateways with Accepted=True managed by this controller.
# TYPE meridio_2_gateway_count gauge
meridio_2_gateway_count 1
# HELP meridio_2_gateway_programmed Whether the Gateway's LB Deployment has been successfully reconciled (Programmed condition), as 0 or 1.
# TYPE meridio_2_gateway_programmed gauge
meridio_2_gateway_programmed{gateway="gw-a",namespace="ns-a"} 1
`
	require.NoError(t, testutil.CollectAndCompare(collector, strings.NewReader(expected),
		"meridio_2_gateway_count", "meridio_2_gateway_programmed"))
}

func TestGatewayCollector_ProgrammedFalse_WhenNotProgrammed(t *testing.T) {
	gw := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "gw-a", Namespace: "ns-a"},
		Status: gatewayv1.GatewayStatus{
			Conditions: []metav1.Condition{
				acceptedCondition(testControllerName),
				gcProgrammedCondition(metav1.ConditionFalse),
			},
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(gcScheme()).WithObjects(gw).Build()
	collector := NewGatewayCollector(fakeClient, alwaysSyncedWaiter{}, time.Second, "", testControllerName, "meridio_2")

	expected := `
# HELP meridio_2_gateway_count Number of Gateways with Accepted=True managed by this controller.
# TYPE meridio_2_gateway_count gauge
meridio_2_gateway_count 1
# HELP meridio_2_gateway_programmed Whether the Gateway's LB Deployment has been successfully reconciled (Programmed condition), as 0 or 1.
# TYPE meridio_2_gateway_programmed gauge
meridio_2_gateway_programmed{gateway="gw-a",namespace="ns-a"} 0
`
	require.NoError(t, testutil.CollectAndCompare(collector, strings.NewReader(expected),
		"meridio_2_gateway_count", "meridio_2_gateway_programmed"))
}

// TestGatewayCollector_AcceptedByDifferentController_Excluded verifies that a Gateway accepted
// by another controller (Accepted=True, but the Message names a different controller) is
// excluded from gateway_count and does not emit a gateway_programmed series — this is the exact
// scenario gatewayutil.IsGatewayAcceptedByController exists to filter (Gateway API allows
// multiple controllers to interact with the same Gateway object).
func TestGatewayCollector_AcceptedByDifferentController_Excluded(t *testing.T) {
	ours := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "gw-ours", Namespace: "ns-a"},
		Status: gatewayv1.GatewayStatus{
			Conditions: []metav1.Condition{
				acceptedCondition(testControllerName),
				gcProgrammedCondition(metav1.ConditionTrue),
			},
		},
	}
	other := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "gw-other", Namespace: "ns-a"},
		Status: gatewayv1.GatewayStatus{
			Conditions: []metav1.Condition{
				acceptedCondition("other.example.com/gateway-controller"),
				gcProgrammedCondition(metav1.ConditionTrue),
			},
		},
	}
	notAccepted := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "gw-pending", Namespace: "ns-a"},
		Status: gatewayv1.GatewayStatus{
			Conditions: []metav1.Condition{
				{
					Type:               string(gatewayv1.GatewayConditionAccepted),
					Status:             metav1.ConditionUnknown,
					Reason:             string(gatewayv1.GatewayReasonPending),
					Message:            "waiting for controller",
					LastTransitionTime: metav1.Now(),
				},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(gcScheme()).
		WithObjects(ours, other, notAccepted).Build()
	collector := NewGatewayCollector(fakeClient, alwaysSyncedWaiter{}, time.Second, "", testControllerName, "meridio_2")

	expected := `
# HELP meridio_2_gateway_count Number of Gateways with Accepted=True managed by this controller.
# TYPE meridio_2_gateway_count gauge
meridio_2_gateway_count 1
# HELP meridio_2_gateway_programmed Whether the Gateway's LB Deployment has been successfully reconciled (Programmed condition), as 0 or 1.
# TYPE meridio_2_gateway_programmed gauge
meridio_2_gateway_programmed{gateway="gw-ours",namespace="ns-a"} 1
`
	require.NoError(t, testutil.CollectAndCompare(collector, strings.NewReader(expected),
		"meridio_2_gateway_count", "meridio_2_gateway_programmed"))
}

func TestGatewayCollector_NoGateways_ZeroCount(t *testing.T) {
	fakeClient := fake.NewClientBuilder().WithScheme(gcScheme()).Build()
	collector := NewGatewayCollector(fakeClient, alwaysSyncedWaiter{}, time.Second, "", testControllerName, "meridio_2")

	expected := `
# HELP meridio_2_gateway_count Number of Gateways with Accepted=True managed by this controller.
# TYPE meridio_2_gateway_count gauge
meridio_2_gateway_count 0
`
	require.NoError(t, testutil.CollectAndCompare(collector, strings.NewReader(expected), "meridio_2_gateway_count"))
}

// TestGatewayCollector_NamespaceScoping verifies that a non-empty collector namespace restricts
// the List to that namespace (mirroring ManagerConfig.Namespace), so a Gateway in a different
// namespace is not counted even though it would otherwise be Accepted by us.
func TestGatewayCollector_NamespaceScoping(t *testing.T) {
	inScope := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "gw-in", Namespace: "ns-a"},
		Status:     gatewayv1.GatewayStatus{Conditions: []metav1.Condition{acceptedCondition(testControllerName)}},
	}
	outOfScope := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "gw-out", Namespace: "ns-b"},
		Status:     gatewayv1.GatewayStatus{Conditions: []metav1.Condition{acceptedCondition(testControllerName)}},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(gcScheme()).WithObjects(inScope, outOfScope).Build()
	collector := NewGatewayCollector(fakeClient, alwaysSyncedWaiter{}, time.Second, "ns-a", testControllerName, "meridio_2")

	expected := `
# HELP meridio_2_gateway_count Number of Gateways with Accepted=True managed by this controller.
# TYPE meridio_2_gateway_count gauge
meridio_2_gateway_count 1
`
	require.NoError(t, testutil.CollectAndCompare(collector, strings.NewReader(expected), "meridio_2_gateway_count"))
}
