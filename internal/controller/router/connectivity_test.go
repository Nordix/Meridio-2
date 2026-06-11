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

package router

import (
	"context"
	"testing"
	"time"

	meridio2v1alpha1 "github.com/nordix/meridio-2/api/v1alpha1"
	"github.com/nordix/meridio-2/internal/bird"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestClassifyConnectivityByFamily(t *testing.T) {
	tests := []struct {
		name      string
		protocols []bird.ProtocolStatus
		familyMap map[string]string // protocolName → "IPv4"/"IPv6"
		wantIPv4  bool
		wantIPv6  bool
	}{
		{
			name: "dual-stack both established",
			protocols: []bird.ProtocolStatus{
				{Name: "NBR-gw-v4", State: bird.ProtocolStateUp, Info: "Established"},
				{Name: "NBR-gw-v6", State: bird.ProtocolStateUp, Info: "Established"},
			},
			familyMap: map[string]string{"NBR-gw-v4": "IPv4", "NBR-gw-v6": "IPv6"},
			wantIPv4:  true, wantIPv6: true,
		},
		{
			name: "IPv4 up, IPv6 down",
			protocols: []bird.ProtocolStatus{
				{Name: "NBR-gw-v4", State: bird.ProtocolStateUp, Info: "Established"},
				{Name: "NBR-gw-v6", State: bird.ProtocolStateDown, Info: "Connection closed"},
			},
			familyMap: map[string]string{"NBR-gw-v4": "IPv4", "NBR-gw-v6": "IPv6"},
			wantIPv4:  true, wantIPv6: false,
		},
		{
			name: "IPv6 up, IPv4 down",
			protocols: []bird.ProtocolStatus{
				{Name: "NBR-gw-v4", State: bird.ProtocolStateDown, Info: "Connection closed"},
				{Name: "NBR-gw-v6", State: bird.ProtocolStateUp, Info: "Established"},
			},
			familyMap: map[string]string{"NBR-gw-v4": "IPv4", "NBR-gw-v6": "IPv6"},
			wantIPv4:  false, wantIPv6: true,
		},
		{
			name: "both down",
			protocols: []bird.ProtocolStatus{
				{Name: "NBR-gw-v4", State: bird.ProtocolStateDown, Info: "Connection closed"},
				{Name: "NBR-gw-v6", State: bird.ProtocolStateDown, Info: "Connection closed"},
			},
			familyMap: map[string]string{"NBR-gw-v4": "IPv4", "NBR-gw-v6": "IPv6"},
			wantIPv4:  false, wantIPv6: false,
		},
		{
			name: "unknown protocol ignored",
			protocols: []bird.ProtocolStatus{
				{Name: "NBR-unknown", State: bird.ProtocolStateUp, Info: "Established"},
			},
			familyMap: map[string]string{},
			wantIPv4:  false, wantIPv6: false,
		},
		{
			name: "multiple IPv4 peers, one established suffices",
			protocols: []bird.ProtocolStatus{
				{Name: "NBR-gw1", State: bird.ProtocolStateDown, Info: "Connection closed"},
				{Name: "NBR-gw2", State: bird.ProtocolStateUp, Info: "Established"},
			},
			familyMap: map[string]string{"NBR-gw1": "IPv4", "NBR-gw2": "IPv4"},
			wantIPv4:  true, wantIPv6: false,
		},
		{
			name:      "empty protocols",
			protocols: []bird.ProtocolStatus{},
			familyMap: map[string]string{},
			wantIPv4:  false, wantIPv6: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ipv4, ipv6 := ClassifyConnectivityByFamily(tt.protocols, tt.familyMap)
			assert.Equal(t, tt.wantIPv4, ipv4, "IPv4 connectivity")
			assert.Equal(t, tt.wantIPv6, ipv6, "IPv6 connectivity")
		})
	}
}

func TestBuildFamilyMap(t *testing.T) {
	tests := []struct {
		name     string
		routers  []*meridio2v1alpha1.GatewayRouter
		expected map[string]string
	}{
		{
			name: "IPv4 router",
			routers: []*meridio2v1alpha1.GatewayRouter{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "gw-v4"},
					Spec:       meridio2v1alpha1.GatewayRouterSpec{Address: "169.254.100.150"},
				},
			},
			expected: map[string]string{"NBR-gw-v4": "IPv4"},
		},
		{
			name: "IPv6 router",
			routers: []*meridio2v1alpha1.GatewayRouter{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "gw-v6"},
					Spec:       meridio2v1alpha1.GatewayRouterSpec{Address: "100:100::150"},
				},
			},
			expected: map[string]string{"NBR-gw-v6": "IPv6"},
		},
		{
			name: "dual-stack two routers",
			routers: []*meridio2v1alpha1.GatewayRouter{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "gw-v4"},
					Spec:       meridio2v1alpha1.GatewayRouterSpec{Address: "169.254.100.150"},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Name: "gw-v6"},
					Spec:       meridio2v1alpha1.GatewayRouterSpec{Address: "100:100::150"},
				},
			},
			expected: map[string]string{"NBR-gw-v4": "IPv4", "NBR-gw-v6": "IPv6"},
		},
		{
			name:     "empty routers",
			routers:  []*meridio2v1alpha1.GatewayRouter{},
			expected: map[string]string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := BuildFamilyMap(tt.routers)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestDiscoverGates(t *testing.T) {
	tests := []struct {
		name         string
		pod          *corev1.Pod
		wantIPv4Gate bool
		wantIPv6Gate bool
	}{
		{
			name: "both gates declared",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "test-pod", Namespace: "default"},
				Spec: corev1.PodSpec{
					ReadinessGates: []corev1.PodReadinessGate{
						{ConditionType: "meridio-2.nordix.org/ipv4-connectivity"},
						{ConditionType: "meridio-2.nordix.org/ipv6-connectivity"},
					},
				},
			},
			wantIPv4Gate: true,
			wantIPv6Gate: true,
		},
		{
			name: "only IPv4 declared",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "test-pod", Namespace: "default"},
				Spec: corev1.PodSpec{
					ReadinessGates: []corev1.PodReadinessGate{
						{ConditionType: "meridio-2.nordix.org/ipv4-connectivity"},
					},
				},
			},
			wantIPv4Gate: true,
			wantIPv6Gate: false,
		},
		{
			name: "only IPv6 declared",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "test-pod", Namespace: "default"},
				Spec: corev1.PodSpec{
					ReadinessGates: []corev1.PodReadinessGate{
						{ConditionType: "meridio-2.nordix.org/ipv6-connectivity"},
					},
				},
			},
			wantIPv4Gate: false,
			wantIPv6Gate: true,
		},
		{
			name: "no gates declared",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "test-pod", Namespace: "default"},
				Spec:       corev1.PodSpec{},
			},
			wantIPv4Gate: false,
			wantIPv6Gate: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeClient := fake.NewClientBuilder().
				WithObjects(tt.pod).
				Build()

			mgr := NewConnectivityGateManager(fakeClient, tt.pod.Name, tt.pod.Namespace, tt.pod.UID, 10*time.Second)
			err := mgr.DiscoverGates(context.Background())
			assert.NoError(t, err)
			assert.Equal(t, tt.wantIPv4Gate, mgr.HasIPv4Gate())
			assert.Equal(t, tt.wantIPv6Gate, mgr.HasIPv6Gate())
		})
	}
}

func TestPatchGateCondition(t *testing.T) {
	t.Run("SetsConditionTrue", func(t *testing.T) {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "test-pod", Namespace: "default", UID: "uid-1"},
			Spec: corev1.PodSpec{
				ReadinessGates: []corev1.PodReadinessGate{
					{ConditionType: "meridio-2.nordix.org/ipv4-connectivity"},
				},
			},
		}
		fakeClient := fake.NewClientBuilder().
			WithObjects(pod).
			WithStatusSubresource(pod).
			Build()

		mgr := NewConnectivityGateManager(fakeClient, "test-pod", "default", "uid-1", 10*time.Second)
		err := mgr.patchGateCondition(context.Background(), ReadinessGateIPv4, true)
		assert.NoError(t, err)

		// Verify condition was set
		updated := &corev1.Pod{}
		err = fakeClient.Get(context.Background(), types.NamespacedName{Name: "test-pod", Namespace: "default"}, updated)
		assert.NoError(t, err)

		var found bool
		for _, c := range updated.Status.Conditions {
			if c.Type == corev1.PodConditionType(ReadinessGateIPv4) {
				assert.Equal(t, corev1.ConditionTrue, c.Status)
				found = true
			}
		}
		assert.True(t, found, "condition should be set")
	})

	t.Run("SetsConditionFalse", func(t *testing.T) {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "test-pod", Namespace: "default", UID: "uid-1"},
			Spec: corev1.PodSpec{
				ReadinessGates: []corev1.PodReadinessGate{
					{ConditionType: "meridio-2.nordix.org/ipv6-connectivity"},
				},
			},
		}
		fakeClient := fake.NewClientBuilder().
			WithObjects(pod).
			WithStatusSubresource(pod).
			Build()

		mgr := NewConnectivityGateManager(fakeClient, "test-pod", "default", "uid-1", 10*time.Second)
		err := mgr.patchGateCondition(context.Background(), ReadinessGateIPv6, false)
		assert.NoError(t, err)

		updated := &corev1.Pod{}
		err = fakeClient.Get(context.Background(), types.NamespacedName{Name: "test-pod", Namespace: "default"}, updated)
		assert.NoError(t, err)

		var found bool
		for _, c := range updated.Status.Conditions {
			if c.Type == corev1.PodConditionType(ReadinessGateIPv6) {
				assert.Equal(t, corev1.ConditionFalse, c.Status)
				found = true
			}
		}
		assert.True(t, found, "condition should be set")
	})

	t.Run("UpdatesExistingCondition", func(t *testing.T) {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "test-pod", Namespace: "default", UID: "uid-1"},
			Spec: corev1.PodSpec{
				ReadinessGates: []corev1.PodReadinessGate{
					{ConditionType: "meridio-2.nordix.org/ipv4-connectivity"},
				},
			},
			Status: corev1.PodStatus{
				Conditions: []corev1.PodCondition{
					{
						Type:   corev1.PodConditionType(ReadinessGateIPv4),
						Status: corev1.ConditionFalse,
					},
				},
			},
		}
		fakeClient := fake.NewClientBuilder().
			WithObjects(pod).
			WithStatusSubresource(pod).
			Build()

		mgr := NewConnectivityGateManager(fakeClient, "test-pod", "default", "uid-1", 10*time.Second)
		err := mgr.patchGateCondition(context.Background(), ReadinessGateIPv4, true)
		assert.NoError(t, err)

		updated := &corev1.Pod{}
		err = fakeClient.Get(context.Background(), types.NamespacedName{Name: "test-pod", Namespace: "default"}, updated)
		assert.NoError(t, err)

		for _, c := range updated.Status.Conditions {
			if c.Type == corev1.PodConditionType(ReadinessGateIPv4) {
				assert.Equal(t, corev1.ConditionTrue, c.Status)
			}
		}
	})
}

func TestSetAllGatesFalse(t *testing.T) {
	t.Run("SetsAllDeclaredGatesToFalse", func(t *testing.T) {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "test-pod", Namespace: "default", UID: "uid-1"},
			Spec: corev1.PodSpec{
				ReadinessGates: []corev1.PodReadinessGate{
					{ConditionType: "meridio-2.nordix.org/ipv4-connectivity"},
					{ConditionType: "meridio-2.nordix.org/ipv6-connectivity"},
				},
			},
		}
		fakeClient := fake.NewClientBuilder().
			WithObjects(pod).
			WithStatusSubresource(pod).
			Build()

		mgr := NewConnectivityGateManager(fakeClient, "test-pod", "default", "uid-1", 10*time.Second)
		err := mgr.DiscoverGates(context.Background())
		assert.NoError(t, err)

		err = mgr.SetAllGatesFalse(context.Background())
		assert.NoError(t, err)

		// Verify both conditions are False
		updated := &corev1.Pod{}
		err = fakeClient.Get(context.Background(), types.NamespacedName{Name: "test-pod", Namespace: "default"}, updated)
		assert.NoError(t, err)

		for _, condType := range []string{"meridio-2.nordix.org/ipv4-connectivity", "meridio-2.nordix.org/ipv6-connectivity"} {
			var found bool
			for _, c := range updated.Status.Conditions {
				if string(c.Type) == condType {
					assert.Equal(t, corev1.ConditionFalse, c.Status)
					found = true
				}
			}
			assert.True(t, found, "condition %s should be set", condType)
		}
	})

	t.Run("IPv4Only_SetsOnlyIPv4Gate", func(t *testing.T) {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "test-pod", Namespace: "default", UID: "uid-1"},
			Spec: corev1.PodSpec{
				ReadinessGates: []corev1.PodReadinessGate{
					{ConditionType: "meridio-2.nordix.org/ipv4-connectivity"},
				},
			},
		}
		fakeClient := fake.NewClientBuilder().
			WithObjects(pod).
			WithStatusSubresource(pod).
			Build()

		mgr := NewConnectivityGateManager(fakeClient, "test-pod", "default", "uid-1", 10*time.Second)
		err := mgr.DiscoverGates(context.Background())
		assert.NoError(t, err)

		err = mgr.SetAllGatesFalse(context.Background())
		assert.NoError(t, err)

		updated := &corev1.Pod{}
		err = fakeClient.Get(context.Background(), types.NamespacedName{Name: "test-pod", Namespace: "default"}, updated)
		assert.NoError(t, err)
		assert.Len(t, updated.Status.Conditions, 1)
		assert.Equal(t, corev1.PodConditionType("meridio-2.nordix.org/ipv4-connectivity"), updated.Status.Conditions[0].Type)
		assert.Equal(t, corev1.ConditionFalse, updated.Status.Conditions[0].Status)
	})

	t.Run("IPv6Only_SetsOnlyIPv6Gate", func(t *testing.T) {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "test-pod", Namespace: "default", UID: "uid-1"},
			Spec: corev1.PodSpec{
				ReadinessGates: []corev1.PodReadinessGate{
					{ConditionType: "meridio-2.nordix.org/ipv6-connectivity"},
				},
			},
		}
		fakeClient := fake.NewClientBuilder().
			WithObjects(pod).
			WithStatusSubresource(pod).
			Build()

		mgr := NewConnectivityGateManager(fakeClient, "test-pod", "default", "uid-1", 10*time.Second)
		err := mgr.DiscoverGates(context.Background())
		assert.NoError(t, err)

		err = mgr.SetAllGatesFalse(context.Background())
		assert.NoError(t, err)

		updated := &corev1.Pod{}
		err = fakeClient.Get(context.Background(), types.NamespacedName{Name: "test-pod", Namespace: "default"}, updated)
		assert.NoError(t, err)
		assert.Len(t, updated.Status.Conditions, 1)
		assert.Equal(t, corev1.PodConditionType("meridio-2.nordix.org/ipv6-connectivity"), updated.Status.Conditions[0].Type)
		assert.Equal(t, corev1.ConditionFalse, updated.Status.Conditions[0].Status)
	})

	t.Run("NoGatesDeclared_NoOp", func(t *testing.T) {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "test-pod", Namespace: "default", UID: "uid-1"},
			Spec:       corev1.PodSpec{},
		}
		fakeClient := fake.NewClientBuilder().
			WithObjects(pod).
			WithStatusSubresource(pod).
			Build()

		mgr := NewConnectivityGateManager(fakeClient, "test-pod", "default", "uid-1", 10*time.Second)
		err := mgr.DiscoverGates(context.Background())
		assert.NoError(t, err)

		err = mgr.SetAllGatesFalse(context.Background())
		assert.NoError(t, err)

		// No conditions should be set
		updated := &corev1.Pod{}
		err = fakeClient.Get(context.Background(), types.NamespacedName{Name: "test-pod", Namespace: "default"}, updated)
		assert.NoError(t, err)
		assert.Empty(t, updated.Status.Conditions)
	})
}

func TestDamping(t *testing.T) {
	t.Run("DownTransition_Immediate", func(t *testing.T) {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "test-pod", Namespace: "default", UID: "uid-1"},
			Spec: corev1.PodSpec{
				ReadinessGates: []corev1.PodReadinessGate{
					{ConditionType: "meridio-2.nordix.org/ipv4-connectivity"},
				},
			},
		}
		fakeClient := fake.NewClientBuilder().
			WithObjects(pod).
			WithStatusSubresource(pod).
			Build()

		mgr := NewConnectivityGateManager(fakeClient, "test-pod", "default", "uid-1", 100*time.Millisecond)
		_ = mgr.DiscoverGates(context.Background())
		_ = mgr.SetAllGatesFalse(context.Background())

		// Bring connectivity up: first tick starts timer, wait holdTime, second tick sets True
		_ = mgr.OnStatusUpdate(context.Background(), true, false)
		time.Sleep(150 * time.Millisecond)
		_ = mgr.OnStatusUpdate(context.Background(), true, false)

		// Verify gate is True
		updated := &corev1.Pod{}
		_ = fakeClient.Get(context.Background(), types.NamespacedName{Name: "test-pod", Namespace: "default"}, updated)
		for _, c := range updated.Status.Conditions {
			if string(c.Type) == ReadinessGateIPv4 {
				assert.Equal(t, corev1.ConditionTrue, c.Status, "gate should be True before drop")
			}
		}

		// Connectivity drops — gate should be False immediately (no 100ms damping applied)
		err := mgr.OnStatusUpdate(context.Background(), false, false)
		assert.NoError(t, err)

		_ = fakeClient.Get(context.Background(), types.NamespacedName{Name: "test-pod", Namespace: "default"}, updated)
		for _, c := range updated.Status.Conditions {
			if string(c.Type) == ReadinessGateIPv4 {
				assert.Equal(t, corev1.ConditionFalse, c.Status, "gate should be False immediately on down (no damping)")
			}
		}
	})

	t.Run("UpTransition_Damped", func(t *testing.T) {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "test-pod", Namespace: "default", UID: "uid-1"},
			Spec: corev1.PodSpec{
				ReadinessGates: []corev1.PodReadinessGate{
					{ConditionType: "meridio-2.nordix.org/ipv4-connectivity"},
				},
			},
		}
		fakeClient := fake.NewClientBuilder().
			WithObjects(pod).
			WithStatusSubresource(pod).
			Build()

		mgr := NewConnectivityGateManager(fakeClient, "test-pod", "default", "uid-1", 100*time.Millisecond)
		_ = mgr.DiscoverGates(context.Background())
		_ = mgr.SetAllGatesFalse(context.Background())

		// Connectivity comes up — gate should NOT be True yet (hold time not elapsed)
		err := mgr.OnStatusUpdate(context.Background(), true, false)
		assert.NoError(t, err)

		updated := &corev1.Pod{}
		_ = fakeClient.Get(context.Background(), types.NamespacedName{Name: "test-pod", Namespace: "default"}, updated)
		for _, c := range updated.Status.Conditions {
			if string(c.Type) == ReadinessGateIPv4 {
				assert.Equal(t, corev1.ConditionFalse, c.Status, "gate should still be False during hold time")
			}
		}

		// Wait for hold time to elapse
		time.Sleep(150 * time.Millisecond)

		// Call again — now it should be True
		err = mgr.OnStatusUpdate(context.Background(), true, false)
		assert.NoError(t, err)

		_ = fakeClient.Get(context.Background(), types.NamespacedName{Name: "test-pod", Namespace: "default"}, updated)
		for _, c := range updated.Status.Conditions {
			if string(c.Type) == ReadinessGateIPv4 {
				assert.Equal(t, corev1.ConditionTrue, c.Status, "gate should be True after hold time")
			}
		}
	})

	t.Run("UpTransition_FlapDuringHold_ResetsTimer", func(t *testing.T) {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "test-pod", Namespace: "default", UID: "uid-1"},
			Spec: corev1.PodSpec{
				ReadinessGates: []corev1.PodReadinessGate{
					{ConditionType: "meridio-2.nordix.org/ipv4-connectivity"},
				},
			},
		}
		fakeClient := fake.NewClientBuilder().
			WithObjects(pod).
			WithStatusSubresource(pod).
			Build()

		mgr := NewConnectivityGateManager(fakeClient, "test-pod", "default", "uid-1", 100*time.Millisecond)
		_ = mgr.DiscoverGates(context.Background())
		_ = mgr.SetAllGatesFalse(context.Background())

		// Connectivity up
		_ = mgr.OnStatusUpdate(context.Background(), true, false)

		// Flap down during hold — should reset timer and set False
		time.Sleep(50 * time.Millisecond)
		_ = mgr.OnStatusUpdate(context.Background(), false, false)

		// Wait past original hold time
		time.Sleep(80 * time.Millisecond)

		// Gate should still be False (timer was reset by the flap)
		updated := &corev1.Pod{}
		_ = fakeClient.Get(context.Background(), types.NamespacedName{Name: "test-pod", Namespace: "default"}, updated)
		for _, c := range updated.Status.Conditions {
			if string(c.Type) == ReadinessGateIPv4 {
				assert.Equal(t, corev1.ConditionFalse, c.Status, "gate should remain False after flap")
			}
		}
	})

	t.Run("NoChange_NoPatch", func(t *testing.T) {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "test-pod", Namespace: "default", UID: "uid-1"},
			Spec: corev1.PodSpec{
				ReadinessGates: []corev1.PodReadinessGate{
					{ConditionType: "meridio-2.nordix.org/ipv4-connectivity"},
				},
			},
		}
		fakeClient := fake.NewClientBuilder().
			WithObjects(pod).
			WithStatusSubresource(pod).
			Build()

		mgr := NewConnectivityGateManager(fakeClient, "test-pod", "default", "uid-1", 100*time.Millisecond)
		_ = mgr.DiscoverGates(context.Background())
		_ = mgr.SetAllGatesFalse(context.Background())

		// Connectivity still down — no change, should be no-op
		err := mgr.OnStatusUpdate(context.Background(), false, false)
		assert.NoError(t, err)
	})

	t.Run("GateNotDeclared_Ignored", func(t *testing.T) {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "test-pod", Namespace: "default", UID: "uid-1"},
			Spec: corev1.PodSpec{
				ReadinessGates: []corev1.PodReadinessGate{
					{ConditionType: "meridio-2.nordix.org/ipv4-connectivity"},
				},
			},
		}
		fakeClient := fake.NewClientBuilder().
			WithObjects(pod).
			WithStatusSubresource(pod).
			Build()

		mgr := NewConnectivityGateManager(fakeClient, "test-pod", "default", "uid-1", 100*time.Millisecond)
		_ = mgr.DiscoverGates(context.Background())
		_ = mgr.SetAllGatesFalse(context.Background())

		// IPv6 connectivity changes but IPv6 gate not declared — should be ignored
		err := mgr.OnStatusUpdate(context.Background(), false, true)
		assert.NoError(t, err)

		// Only IPv4 condition should exist (set to False by SetAllGatesFalse)
		updated := &corev1.Pod{}
		_ = fakeClient.Get(context.Background(), types.NamespacedName{Name: "test-pod", Namespace: "default"}, updated)
		assert.Len(t, updated.Status.Conditions, 1)
		assert.Equal(t, corev1.PodConditionType(ReadinessGateIPv4), updated.Status.Conditions[0].Type)
	})
}
