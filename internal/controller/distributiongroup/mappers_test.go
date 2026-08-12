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
	"testing"

	meridio2v1alpha1 "github.com/nordix/meridio-2/api/v1alpha1"
	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

const testOtherNamespace = "other-ns"

func TestMapL34RouteToDistributionGroup_SameNamespace(t *testing.T) {
	dgGroup := meridio2v1alpha1.GroupVersion.Group
	dgKind := kindDistributionGroup

	route := &meridio2v1alpha1.L34Route{
		ObjectMeta: metav1.ObjectMeta{Name: "route-1", Namespace: "default"},
		Spec: meridio2v1alpha1.L34RouteSpec{
			BackendRefs: []gatewayv1.BackendRef{
				{BackendObjectReference: gatewayv1.BackendObjectReference{
					Group: (*gatewayv1.Group)(&dgGroup),
					Kind:  (*gatewayv1.Kind)(&dgKind),
					Name:  "dg-1",
				}},
			},
		},
	}

	r := &DistributionGroupReconciler{Namespace: "default"}
	requests := r.mapL34RouteToDistributionGroup(context.Background(), route)

	assert.Len(t, requests, 1)
	assert.Equal(t, "dg-1", requests[0].Name)
	assert.Equal(t, "default", requests[0].Namespace)
}

func TestMapL34RouteToDistributionGroup_CrossNamespaceRejected(t *testing.T) {
	dgGroup := meridio2v1alpha1.GroupVersion.Group
	dgKind := kindDistributionGroup
	otherNs := testOtherNamespace

	// backendRef explicitly points outside the controller's watched namespace.
	route := &meridio2v1alpha1.L34Route{
		ObjectMeta: metav1.ObjectMeta{Name: "route-1", Namespace: "default"},
		Spec: meridio2v1alpha1.L34RouteSpec{
			BackendRefs: []gatewayv1.BackendRef{
				{BackendObjectReference: gatewayv1.BackendObjectReference{
					Group:     (*gatewayv1.Group)(&dgGroup),
					Kind:      (*gatewayv1.Kind)(&dgKind),
					Name:      "dg-1",
					Namespace: (*gatewayv1.Namespace)(&otherNs),
				}},
			},
		},
	}

	r := &DistributionGroupReconciler{Namespace: "default"}
	requests := r.mapL34RouteToDistributionGroup(context.Background(), route)

	assert.Empty(t, requests)
}

func TestMapL34RouteToDistributionGroup_ClusterWideAllowsAnyNamespace(t *testing.T) {
	dgGroup := meridio2v1alpha1.GroupVersion.Group
	dgKind := kindDistributionGroup
	otherNs := testOtherNamespace

	route := &meridio2v1alpha1.L34Route{
		ObjectMeta: metav1.ObjectMeta{Name: "route-1", Namespace: "default"},
		Spec: meridio2v1alpha1.L34RouteSpec{
			BackendRefs: []gatewayv1.BackendRef{
				{BackendObjectReference: gatewayv1.BackendObjectReference{
					Group:     (*gatewayv1.Group)(&dgGroup),
					Kind:      (*gatewayv1.Kind)(&dgKind),
					Name:      "dg-1",
					Namespace: (*gatewayv1.Namespace)(&otherNs),
				}},
			},
		},
	}

	// Namespace == "" means cluster-wide operation: no restriction applies.
	r := &DistributionGroupReconciler{Namespace: ""}
	requests := r.mapL34RouteToDistributionGroup(context.Background(), route)

	assert.Len(t, requests, 1)
	assert.Equal(t, "dg-1", requests[0].Name)
	assert.Equal(t, testOtherNamespace, requests[0].Namespace)
}
