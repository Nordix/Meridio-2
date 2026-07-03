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

package endpointnetworkconfiguration

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	meridio2v1alpha1 "github.com/nordix/meridio-2/api/v1alpha1"
	"github.com/nordix/meridio-2/internal/common/constants"
)

func mapperScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = corev1.AddToScheme(s)
	_ = meridio2v1alpha1.AddToScheme(s)
	_ = gatewayv1.Install(s)
	return s
}

func mapperReconciler(objs ...client.Object) *Reconciler {
	return &Reconciler{
		Client:    fake.NewClientBuilder().WithScheme(mapperScheme()).WithObjects(objs...).Build(),
		Scheme:    mapperScheme(),
		Namespace: "default",
	}
}

// --- mapDGToPods ---

func TestMapDGToPods_MatchingPods(t *testing.T) {
	pod1 := newPod("pod-1", corev1.PodRunning, map[string]string{"app": "target"})
	pod2 := newPod("pod-2", corev1.PodRunning, map[string]string{"app": "other"})
	dg := newDG("dg-1", map[string]string{"app": "target"}, "")

	r := mapperReconciler(pod1, pod2, dg)
	reqs := r.mapDGToPods(context.Background(), dg)

	assert.Len(t, reqs, 1)
	assert.Equal(t, "pod-1", reqs[0].Name)
}

func TestMapDGToPods_NilSelector(t *testing.T) {
	pod := newPod("pod-1", corev1.PodRunning, map[string]string{"app": "target"})
	dg := &meridio2v1alpha1.DistributionGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "dg-nil", Namespace: "default"},
		Spec:       meridio2v1alpha1.DistributionGroupSpec{Selector: nil},
	}

	r := mapperReconciler(pod, dg)
	reqs := r.mapDGToPods(context.Background(), dg)

	assert.Empty(t, reqs) // nil selector = match nothing
}

// --- mapGatewayToPods (via direct DG parentRef) ---

func TestMapGatewayToPods_DirectParentRef(t *testing.T) {
	pod := newPod("pod-1", corev1.PodRunning, map[string]string{"app": "target"})
	dg := newDG("dg-1", map[string]string{"app": "target"}, "gw-1")
	gw := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "gw-1", Namespace: "default"},
		Spec:       gatewayv1.GatewaySpec{GatewayClassName: "test"},
	}

	r := mapperReconciler(pod, dg, gw)
	reqs := r.mapGatewayToPods(context.Background(), gw)

	assert.Len(t, reqs, 1)
	assert.Equal(t, "pod-1", reqs[0].Name)
}

// --- mapGatewayToPods (via indirect L34Route→DG) ---

func TestMapGatewayToPods_IndirectViaL34Route(t *testing.T) {
	pod := newPod("pod-1", corev1.PodRunning, map[string]string{"app": "target"})
	dg := newDG("dg-1", map[string]string{"app": "target"}, "") // no direct parentRef
	gwGroup := gatewayv1.Group(gatewayv1.GroupName)
	gwKind := gatewayv1.Kind(kindGateway)
	dgGroup := gatewayv1.Group(meridio2v1alpha1.GroupVersion.Group)
	dgKind := gatewayv1.Kind(kindDistributionGroup)

	route := &meridio2v1alpha1.L34Route{
		ObjectMeta: metav1.ObjectMeta{Name: "route-1", Namespace: "default"},
		Spec: meridio2v1alpha1.L34RouteSpec{
			ParentRefs: []gatewayv1.ParentReference{
				{Group: &gwGroup, Kind: &gwKind, Name: "gw-1"},
			},
			BackendRefs: []gatewayv1.BackendRef{
				{BackendObjectReference: gatewayv1.BackendObjectReference{
					Group: &dgGroup, Kind: &dgKind, Name: "dg-1",
				}},
			},
		},
	}
	gw := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "gw-1", Namespace: "default"},
		Spec:       gatewayv1.GatewaySpec{GatewayClassName: "test"},
	}

	r := mapperReconciler(pod, dg, route, gw)
	reqs := r.mapGatewayToPods(context.Background(), gw)

	assert.Len(t, reqs, 1)
	assert.Equal(t, "pod-1", reqs[0].Name)
}

func TestMapGatewayToPods_NoDGs(t *testing.T) {
	gw := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "gw-1", Namespace: "default"},
		Spec:       gatewayv1.GatewaySpec{GatewayClassName: "test"},
	}

	r := mapperReconciler(gw)
	reqs := r.mapGatewayToPods(context.Background(), gw)

	assert.Empty(t, reqs)
}

// --- mapL34RouteToPods ---

func TestMapL34RouteToPods(t *testing.T) {
	pod := newPod("pod-1", corev1.PodRunning, map[string]string{"app": "target"})
	dg := newDG("dg-1", map[string]string{"app": "target"}, "gw-1")
	gwGroup := gatewayv1.Group(gatewayv1.GroupName)
	gwKind := gatewayv1.Kind(kindGateway)

	route := &meridio2v1alpha1.L34Route{
		ObjectMeta: metav1.ObjectMeta{Name: "route-1", Namespace: "default"},
		Spec: meridio2v1alpha1.L34RouteSpec{
			ParentRefs: []gatewayv1.ParentReference{
				{Group: &gwGroup, Kind: &gwKind, Name: "gw-1"},
			},
		},
	}

	r := mapperReconciler(pod, dg, route)
	reqs := r.mapL34RouteToPods(context.Background(), route)

	assert.Len(t, reqs, 1)
	assert.Equal(t, "pod-1", reqs[0].Name)
}

// --- mapGatewayConfigToPods ---

func TestMapGatewayConfigToPods(t *testing.T) {
	pod := newPod("pod-1", corev1.PodRunning, map[string]string{"app": "target"})
	dg := newDG("dg-1", map[string]string{"app": "target"}, "gw-1")
	gw := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "gw-1", Namespace: "default"},
		Spec: gatewayv1.GatewaySpec{
			GatewayClassName: "test",
			Infrastructure: &gatewayv1.GatewayInfrastructure{
				ParametersRef: &gatewayv1.LocalParametersReference{
					Group: gatewayv1.Group(meridio2v1alpha1.GroupVersion.Group),
					Kind:  kindGatewayConfiguration,
					Name:  "gwconfig-1",
				},
			},
		},
	}
	gwConfig := &meridio2v1alpha1.GatewayConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: "gwconfig-1", Namespace: "default"},
	}

	r := mapperReconciler(pod, dg, gw, gwConfig)
	reqs := r.mapGatewayConfigToPods(context.Background(), gwConfig)

	assert.Len(t, reqs, 1)
	assert.Equal(t, "pod-1", reqs[0].Name)
}

func TestMapGatewayConfigToPods_NoMatchingGateway(t *testing.T) {
	gwConfig := &meridio2v1alpha1.GatewayConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: "gwconfig-orphan", Namespace: "default"},
	}

	r := mapperReconciler(gwConfig)
	reqs := r.mapGatewayConfigToPods(context.Background(), gwConfig)

	assert.Empty(t, reqs)
}

// --- mapSLLBRPodToPods ---

func TestMapSLLBRPodToPods(t *testing.T) {
	appPod := newPod("pod-1", corev1.PodRunning, map[string]string{"app": "target"})
	dg := newDG("dg-1", map[string]string{"app": "target"}, "gw-1")
	sllbrPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sllbr-pod-1",
			Namespace: "default",
			Labels:    map[string]string{constants.LabelGatewayName: "gw-1"},
		},
	}

	r := mapperReconciler(appPod, dg, sllbrPod)
	reqs := r.mapSLLBRPodToPods(context.Background(), sllbrPod)

	assert.Len(t, reqs, 1)
	assert.Equal(t, "pod-1", reqs[0].Name)
}

func TestMapSLLBRPodToPods_NoLabel(t *testing.T) {
	sllbrPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "unrelated-pod",
			Namespace: "default",
		},
	}

	r := mapperReconciler(sllbrPod)
	reqs := r.mapSLLBRPodToPods(context.Background(), sllbrPod)

	assert.Empty(t, reqs)
}

// --- podsForGatewayKeys deduplication ---

func TestPodsForGatewayKeys_DeduplicatesPods(t *testing.T) {
	pod := newPod("pod-1", corev1.PodRunning, map[string]string{"app": "target"})
	// Two DGs both select the same Pod, both reference the same Gateway
	dg1 := newDG("dg-1", map[string]string{"app": "target"}, "gw-1")
	dg2 := newDG("dg-2", map[string]string{"app": "target"}, "gw-1")

	r := mapperReconciler(pod, dg1, dg2)
	reqs := r.podsForGatewayKeys(context.Background(), []client.ObjectKey{
		{Namespace: "default", Name: "gw-1"},
	})

	assert.Len(t, reqs, 1) // Pod deduplicated
	assert.Equal(t, "pod-1", reqs[0].Name)
}
