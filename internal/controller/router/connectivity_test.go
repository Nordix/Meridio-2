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
			ipv4, ipv6 := classifyConnectivityByFamily(tt.protocols, tt.familyMap)
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
			result := buildFamilyMap(tt.routers)
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
