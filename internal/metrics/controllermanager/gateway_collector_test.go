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

package controllermanager

import (
	"context"
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

const (
	// testControllerName is the controller name treated as "ours" in these tests.
	testControllerName = "example.com/gateway-controller"
	// otherControllerName is a different controller, used to build Gateways/GatewayClasses that
	// must NOT be attributed to us.
	otherControllerName = "other.example.com/gateway-controller"

	// ourClassName references a GatewayClass whose controllerName is testControllerName, so
	// Gateways referencing it are class-ours and emit gateway_programmed; otherClassName belongs
	// to otherControllerName.
	ourClassName   = "meridio-class"
	otherClassName = "other-class"
)

// alwaysSyncedWaiter is a metricsutil.CacheSyncWaiter test double that always reports synced
type alwaysSyncedWaiter struct{}

func (alwaysSyncedWaiter) WaitForCacheSync(_ context.Context) bool {
	return true
}

func gcScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	_ = gatewayv1.Install(scheme)
	return scheme
}

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

func gcProgrammedCondition(status metav1.ConditionStatus) metav1.Condition {
	return metav1.Condition{
		Type:               string(gatewayv1.GatewayConditionProgrammed),
		Status:             status,
		Reason:             string(gatewayv1.GatewayReasonProgrammed),
		Message:            "programmed",
		LastTransitionTime: metav1.Now(),
	}
}

// gcDefaultConditions returns the Accepted and Programmed conditions a freshly-created Gateway
// carries before any controller acts on it, per the Gateway API CRD default (both Unknown/Pending/
// "Waiting for controller"; see the conditions default on GatewayStatus in gateway_types.go).
func gcDefaultConditions() []metav1.Condition {
	pending := func(condType string) metav1.Condition {
		return metav1.Condition{
			Type:               condType,
			Status:             metav1.ConditionUnknown,
			Reason:             string(gatewayv1.GatewayReasonPending),
			Message:            "Waiting for controller",
			LastTransitionTime: metav1.Unix(0, 0),
		}
	}
	return []metav1.Condition{
		pending(string(gatewayv1.GatewayConditionAccepted)),
		pending(string(gatewayv1.GatewayConditionProgrammed)),
	}
}

// gcClass returns a GatewayClass with the given name and controllerName.
func gcClass(name, controllerName string) *gatewayv1.GatewayClass {
	return &gatewayv1.GatewayClass{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       gatewayv1.GatewayClassSpec{ControllerName: gatewayv1.GatewayController(controllerName)},
	}
}

// gcOurClass returns a GatewayClass owned by testControllerName.
func gcOurClass() *gatewayv1.GatewayClass { return gcClass(ourClassName, testControllerName) }

// gcOtherClass returns a GatewayClass owned by a different controller.
func gcOtherClass() *gatewayv1.GatewayClass { return gcClass(otherClassName, otherControllerName) }

func TestGatewayCollector_CountAndProgrammed_AcceptedByUs(t *testing.T) {
	gw := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "gw-a", Namespace: "ns-a"},
		Spec:       gatewayv1.GatewaySpec{GatewayClassName: ourClassName},
		Status: gatewayv1.GatewayStatus{
			Conditions: []metav1.Condition{
				acceptedCondition(testControllerName),
				gcProgrammedCondition(metav1.ConditionTrue),
			},
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(gcScheme()).WithObjects(gcOurClass(), gw).Build()
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
		Spec:       gatewayv1.GatewaySpec{GatewayClassName: ourClassName},
		Status: gatewayv1.GatewayStatus{
			Conditions: []metav1.Condition{
				acceptedCondition(testControllerName),
				gcProgrammedCondition(metav1.ConditionFalse),
			},
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(gcScheme()).WithObjects(gcOurClass(), gw).Build()
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

// TestGatewayCollector_CountAcceptedGated_ProgrammedClassGated verifies the decoupled predicates:
// gateway_count is gated on Accepted-by-us (billing semantics, #153), while gateway_programmed is
// gated on GatewayClass ownership independent of Accepted. The two are exercised with:
//   - gw-ours: class-ours AND accepted-by-us   -> counted, programmed emitted
//   - gw-foreign-accept: class-ours but its Accepted message names a different controller
//     -> NOT counted, but programmed IS emitted (class-gated, Accepted-independent)
//   - gw-pending: class-ours, fresh Gateway API default conditions (Accepted/Programmed both
//     Unknown/Pending) -> NOT counted, programmed emitted as 0
//   - gw-not-ours: references a GatewayClass owned by another controller -> neither counted nor
//     programmed-emitted
func TestGatewayCollector_CountAcceptedGated_ProgrammedClassGated(t *testing.T) {
	ours := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "gw-ours", Namespace: "ns-a"},
		Spec:       gatewayv1.GatewaySpec{GatewayClassName: ourClassName},
		Status: gatewayv1.GatewayStatus{
			Conditions: []metav1.Condition{
				acceptedCondition(testControllerName),
				gcProgrammedCondition(metav1.ConditionTrue),
			},
		},
	}
	foreignAccept := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "gw-foreign-accept", Namespace: "ns-a"},
		Spec:       gatewayv1.GatewaySpec{GatewayClassName: ourClassName},
		Status: gatewayv1.GatewayStatus{
			Conditions: []metav1.Condition{
				acceptedCondition(otherControllerName),
				gcProgrammedCondition(metav1.ConditionTrue),
			},
		},
	}
	pending := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "gw-pending", Namespace: "ns-a"},
		Spec:       gatewayv1.GatewaySpec{GatewayClassName: ourClassName},
		Status:     gatewayv1.GatewayStatus{Conditions: gcDefaultConditions()},
	}
	notOurs := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "gw-not-ours", Namespace: "ns-a"},
		Spec:       gatewayv1.GatewaySpec{GatewayClassName: otherClassName},
		Status: gatewayv1.GatewayStatus{
			Conditions: []metav1.Condition{
				acceptedCondition(otherControllerName),
				gcProgrammedCondition(metav1.ConditionTrue),
			},
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(gcScheme()).
		WithObjects(gcOurClass(), gcOtherClass(), ours, foreignAccept, pending, notOurs).Build()
	collector := NewGatewayCollector(fakeClient, alwaysSyncedWaiter{}, time.Second, "", testControllerName, "meridio_2")

	expected := `
# HELP meridio_2_gateway_count Number of Gateways with Accepted=True managed by this controller.
# TYPE meridio_2_gateway_count gauge
meridio_2_gateway_count 1
# HELP meridio_2_gateway_programmed Whether the Gateway's LB Deployment has been successfully reconciled (Programmed condition), as 0 or 1.
# TYPE meridio_2_gateway_programmed gauge
meridio_2_gateway_programmed{gateway="gw-ours",namespace="ns-a"} 1
meridio_2_gateway_programmed{gateway="gw-foreign-accept",namespace="ns-a"} 1
meridio_2_gateway_programmed{gateway="gw-pending",namespace="ns-a"} 0
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
