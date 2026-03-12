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
	"testing"

	meridio2v1alpha1 "github.com/nordix/meridio-2/api/v1alpha1"
	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// Note: The fake client simulates a Kubernetes API server using an in-memory
// object store. However, it doesn't replicate all API server functionalities.
// Notably, CRD defaults, webhooks, and metadata.generation are not automatically
// applied/updated.

const testControllerName = "example.com/gateway-controller"
const testGatewayClassName = "test-class"
const testGatewayName = "test-gateway"
const testNamespace = "default"

func newScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	_ = gatewayv1.Install(scheme)
	_ = meridio2v1alpha1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
	return scheme
}

func newGatewayClass(controllerName string) *gatewayv1.GatewayClass {
	return &gatewayv1.GatewayClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: testGatewayClassName,
		},
		Spec: gatewayv1.GatewayClassSpec{
			ControllerName: gatewayv1.GatewayController(controllerName),
		},
	}
}

func newGateway(gatewayClassName string) *gatewayv1.Gateway {
	return &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testGatewayName,
			Namespace: testNamespace,
		},
		Spec: gatewayv1.GatewaySpec{
			GatewayClassName: gatewayv1.ObjectName(gatewayClassName),
		},
	}
}

func newGatewayConfiguration() *meridio2v1alpha1.GatewayConfiguration {
	return &meridio2v1alpha1.GatewayConfiguration{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gwconfig",
			Namespace: testNamespace,
		},
		Spec: meridio2v1alpha1.GatewayConfigurationSpec{
			NetworkSubnets: []meridio2v1alpha1.NetworkSubnet{
				{
					AttachmentType: "NAD",
					CIDRs:          []string{"192.168.100.0/24"},
				},
			},
			HorizontalScaling: meridio2v1alpha1.HorizontalScaling{
				Replicas:        2,
				EnforceReplicas: false,
			},
		},
	}
}

func attachGatewayConfiguration(gw *gatewayv1.Gateway, gwConfig *meridio2v1alpha1.GatewayConfiguration) {
	gw.Spec.Infrastructure = &gatewayv1.GatewayInfrastructure{
		ParametersRef: &gatewayv1.LocalParametersReference{
			Group: gatewayv1.Group(meridio2v1alpha1.GroupVersion.Group),
			Kind:  gatewayv1.Kind("GatewayConfiguration"),
			Name:  gwConfig.Name,
		},
	}
}

func setupReconciler(objects ...client.Object) (*GatewayReconciler, client.Client) {
	fakeClient := fake.NewClientBuilder().
		WithScheme(newScheme()).
		WithObjects(objects...).
		WithStatusSubresource(&gatewayv1.Gateway{}).
		Build()

	return &GatewayReconciler{
		Client:         fakeClient,
		Scheme:         newScheme(),
		ControllerName: testControllerName,
		TemplatePath:   "../../../config/templates",
	}, fakeClient
}

func assertGatewayAccepted(t *testing.T, fakeClient client.Client, gw *gatewayv1.Gateway) {
	fetched := &gatewayv1.Gateway{}
	err := fakeClient.Get(context.Background(), types.NamespacedName{Name: gw.Name, Namespace: gw.Namespace}, fetched)
	assert.NoError(t, err)

	// Check Accepted condition
	var acceptedCondition *metav1.Condition
	for i := range fetched.Status.Conditions {
		if fetched.Status.Conditions[i].Type == string(gatewayv1.GatewayConditionAccepted) {
			acceptedCondition = &fetched.Status.Conditions[i]
			break
		}
	}

	assert.NotNil(t, acceptedCondition, "Accepted condition should be set")
	assert.Equal(t, metav1.ConditionTrue, acceptedCondition.Status)
	assert.Equal(t, string(gatewayv1.GatewayReasonAccepted), acceptedCondition.Reason)
}

func TestGatewayReconciler_Reconcile(t *testing.T) {
	t.Run("Reconcile_ManagedGateway_SetsAcceptedStatus", func(t *testing.T) {
		gwClass := newGatewayClass(testControllerName)
		gw := newGateway(gwClass.Name)
		gwConfig := newGatewayConfiguration()
		attachGatewayConfiguration(gw, gwConfig)

		reconciler, fakeClient := setupReconciler(gwClass, gw, gwConfig)

		request := reconcile.Request{NamespacedName: types.NamespacedName{Name: gw.Name, Namespace: gw.Namespace}}
		result, err := reconciler.Reconcile(context.Background(), request)

		assert.NoError(t, err)
		assert.Equal(t, ctrl.Result{}, result)

		assertGatewayAccepted(t, fakeClient, gw)
	})

	t.Run("Reconcile_UnmanagedGateway_DoesNotSetStatus", func(t *testing.T) {
		gwClass := newGatewayClass("other-controller")
		gw := newGateway(gwClass.Name)

		reconciler, fakeClient := setupReconciler(gwClass, gw)

		request := reconcile.Request{NamespacedName: types.NamespacedName{Name: gw.Name, Namespace: gw.Namespace}}
		result, err := reconciler.Reconcile(context.Background(), request)

		assert.NoError(t, err)
		assert.Equal(t, ctrl.Result{}, result)

		// Verify no status was set (fake client doesn't apply CRD defaults)
		fetched := &gatewayv1.Gateway{}
		err = fakeClient.Get(context.Background(), types.NamespacedName{Name: gw.Name, Namespace: gw.Namespace}, fetched)
		assert.NoError(t, err)
		assert.Empty(t, fetched.Status.Conditions)
	})

	t.Run("Reconcile_MissingGatewayClass_DoesNotSetStatus", func(t *testing.T) {
		gw := newGateway("nonexistent-class")

		reconciler, fakeClient := setupReconciler(gw)

		request := reconcile.Request{NamespacedName: types.NamespacedName{Name: gw.Name, Namespace: gw.Namespace}}
		result, err := reconciler.Reconcile(context.Background(), request)

		assert.NoError(t, err)
		assert.Equal(t, ctrl.Result{}, result)

		// Verify no status was set
		fetched := &gatewayv1.Gateway{}
		err = fakeClient.Get(context.Background(), types.NamespacedName{Name: gw.Name, Namespace: gw.Namespace}, fetched)
		assert.NoError(t, err)
		assert.Empty(t, fetched.Status.Conditions)
	})

	t.Run("Reconcile_GatewayDeleted_ReturnsWithoutError", func(t *testing.T) {
		// Gateway not in API server (already deleted via ownerReference GC)
		reconciler, _ := setupReconciler()

		request := reconcile.Request{NamespacedName: types.NamespacedName{Name: "deleted-gw", Namespace: "default"}}
		result, err := reconciler.Reconcile(context.Background(), request)

		// Should return without error (client.IgnoreNotFound)
		assert.NoError(t, err)
		assert.Equal(t, ctrl.Result{}, result)
	})

	t.Run("Reconcile_OwnershipTransfer_ResetsAccepted", func(t *testing.T) {
		// Gateway initially managed by this controller
		gwClass := newGatewayClass(testControllerName)
		gw := newGateway(gwClass.Name)
		reconciler, fakeClient := setupReconciler(gwClass, gw)

		gw.Status.Conditions = []metav1.Condition{
			{
				Type:    string(gatewayv1.GatewayConditionAccepted),
				Status:  metav1.ConditionTrue,
				Reason:  string(gatewayv1.GatewayReasonAccepted),
				Message: reconciler.acceptedMessage(),
			},
		}
		err := fakeClient.Status().Update(context.Background(), gw)
		assert.NoError(t, err)

		// Create another GatewayClass
		otherGwClass := newGatewayClass("other-controller")
		otherGwClass.Name = "other-class"
		err = fakeClient.Create(context.Background(), otherGwClass)
		assert.NoError(t, err)

		// Change gatewayClassName to transfer ownership
		gw.Spec.GatewayClassName = gatewayv1.ObjectName(otherGwClass.Name)
		err = fakeClient.Update(context.Background(), gw)
		assert.NoError(t, err)

		request := reconcile.Request{NamespacedName: types.NamespacedName{Name: gw.Name, Namespace: gw.Namespace}}
		result, err := reconciler.Reconcile(context.Background(), request)

		assert.NoError(t, err)
		assert.Equal(t, ctrl.Result{}, result)

		// Verify Accepted was reset to Unknown
		fetched := &gatewayv1.Gateway{}
		err = fakeClient.Get(context.Background(), types.NamespacedName{Name: gw.Name, Namespace: gw.Namespace}, fetched)
		assert.NoError(t, err)

		var acceptedCondition *metav1.Condition
		for i := range fetched.Status.Conditions {
			if fetched.Status.Conditions[i].Type == string(gatewayv1.GatewayConditionAccepted) {
				acceptedCondition = &fetched.Status.Conditions[i]
				break
			}
		}

		assert.NotNil(t, acceptedCondition)
		assert.Equal(t, metav1.ConditionUnknown, acceptedCondition.Status)
		assert.Equal(t, string(gatewayv1.GatewayReasonPending), acceptedCondition.Reason)
	})

	t.Run("Reconcile_InvalidGatewayConfiguration_SetsAcceptedFalse", func(t *testing.T) {
		gwClass := newGatewayClass(testControllerName)
		gw := newGateway(gwClass.Name)
		gw.Spec.Infrastructure = &gatewayv1.GatewayInfrastructure{
			ParametersRef: &gatewayv1.LocalParametersReference{
				Group: gatewayv1.Group(meridio2v1alpha1.GroupVersion.Group),
				Kind:  gatewayv1.Kind("GatewayConfiguration"),
				Name:  "nonexistent",
			},
		}
		reconciler, fakeClient := setupReconciler(gwClass, gw)

		request := reconcile.Request{NamespacedName: types.NamespacedName{Name: gw.Name, Namespace: gw.Namespace}}
		result, err := reconciler.Reconcile(context.Background(), request)

		assert.NoError(t, err)
		assert.Equal(t, ctrl.Result{}, result) // no requeue

		fetched := &gatewayv1.Gateway{}
		err = fakeClient.Get(context.Background(), types.NamespacedName{Name: gw.Name, Namespace: gw.Namespace}, fetched)
		assert.NoError(t, err)

		var acceptedCondition *metav1.Condition
		for i := range fetched.Status.Conditions {
			if fetched.Status.Conditions[i].Type == string(gatewayv1.GatewayConditionAccepted) {
				acceptedCondition = &fetched.Status.Conditions[i]
				break
			}
		}
		assert.NotNil(t, acceptedCondition)
		assert.Equal(t, metav1.ConditionFalse, acceptedCondition.Status)
		assert.Equal(t, string(gatewayv1.GatewayReasonInvalidParameters), acceptedCondition.Reason)
	})

	t.Run("Reconcile_TemplateMissing_SetsAcceptedFalseAndRequeues", func(t *testing.T) {
		gwClass := newGatewayClass(testControllerName)
		gw := newGateway(gwClass.Name)
		gwConfig := newGatewayConfiguration()
		attachGatewayConfiguration(gw, gwConfig)

		reconciler, fakeClient := setupReconciler(gwClass, gw, gwConfig)
		reconciler.TemplatePath = "/nonexistent/path"

		request := reconcile.Request{NamespacedName: types.NamespacedName{Name: gw.Name, Namespace: gw.Namespace}}
		result, err := reconciler.Reconcile(context.Background(), request)

		assert.Error(t, err) // requeue with backoff
		assert.Equal(t, ctrl.Result{}, result)

		fetched := &gatewayv1.Gateway{}
		err = fakeClient.Get(context.Background(), types.NamespacedName{Name: gw.Name, Namespace: gw.Namespace}, fetched)
		assert.NoError(t, err)

		var acceptedCondition *metav1.Condition
		for i := range fetched.Status.Conditions {
			if fetched.Status.Conditions[i].Type == string(gatewayv1.GatewayConditionAccepted) {
				acceptedCondition = &fetched.Status.Conditions[i]
				break
			}
		}
		assert.NotNil(t, acceptedCondition)
		assert.Equal(t, metav1.ConditionFalse, acceptedCondition.Status)
	})

	t.Run("Reconcile_DeploymentNameCollision_SetsProgrammedFalse", func(t *testing.T) {
		gwClass := newGatewayClass(testControllerName)
		gw := newGateway(gwClass.Name)
		gwConfig := newGatewayConfiguration()
		attachGatewayConfiguration(gw, gwConfig)

		isController := true
		existing := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      lbDeploymentPrefix + gw.Name,
				Namespace: gw.Namespace,
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion: gatewayv1.GroupVersion.String(),
						Kind:       kindGateway,
						Name:       "other-gateway",
						UID:        "other-uid",
						Controller: &isController,
					},
				},
			},
		}

		reconciler, fakeClient := setupReconciler(gwClass, gw, gwConfig, existing)

		request := reconcile.Request{NamespacedName: types.NamespacedName{Name: gw.Name, Namespace: gw.Namespace}}
		result, err := reconciler.Reconcile(context.Background(), request)

		assert.Error(t, err)
		assert.Equal(t, ctrl.Result{}, result)

		fetched := &gatewayv1.Gateway{}
		err = fakeClient.Get(context.Background(), types.NamespacedName{Name: gw.Name, Namespace: gw.Namespace}, fetched)
		assert.NoError(t, err)

		var programmedCondition *metav1.Condition
		for i := range fetched.Status.Conditions {
			if fetched.Status.Conditions[i].Type == string(gatewayv1.GatewayConditionProgrammed) {
				programmedCondition = &fetched.Status.Conditions[i]
				break
			}
		}
		assert.NotNil(t, programmedCondition)
		assert.Equal(t, metav1.ConditionFalse, programmedCondition.Status)
		assert.Contains(t, programmedCondition.Message, "name collision")
	})

	t.Run("Reconcile_DeletionTimestamp_SkipsReconciliation", func(t *testing.T) {
		gwClass := newGatewayClass(testControllerName)
		gw := newGateway(gwClass.Name)
		now := metav1.Now()
		gw.DeletionTimestamp = &now
		gw.Finalizers = []string{"test-finalizer"}

		reconciler, fakeClient := setupReconciler(gwClass, gw)

		request := reconcile.Request{NamespacedName: types.NamespacedName{Name: gw.Name, Namespace: gw.Namespace}}
		result, err := reconciler.Reconcile(context.Background(), request)

		assert.NoError(t, err)
		assert.Equal(t, ctrl.Result{}, result)

		fetched := &gatewayv1.Gateway{}
		err = fakeClient.Get(context.Background(), types.NamespacedName{Name: gw.Name, Namespace: gw.Namespace}, fetched)
		assert.NoError(t, err)
		assert.Empty(t, fetched.Status.Conditions)
	})
}
