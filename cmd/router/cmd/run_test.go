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

package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	meridio2v1alpha1 "github.com/nordix/meridio-2/api/v1alpha1"
	"github.com/nordix/meridio-2/internal/bird"
)

func TestApplyBfdState(t *testing.T) {
	bfdSpec := &meridio2v1alpha1.BfdSpec{
		MinTx:      "300ms",
		MinRx:      "300ms",
		Multiplier: 3,
	}

	tests := []struct {
		name          string
		protocols     []bird.ProtocolStatus
		bfdSessions   []bird.BfdSession
		routers       []*meridio2v1alpha1.GatewayRouter
		expectedState []bird.ProtocolState
		expectedInfo  []string
	}{
		{
			name: "static with BFD up - sets Established",
			protocols: []bird.ProtocolStatus{
				{Name: "NBR-gw-static-v4", Proto: "Static", State: bird.ProtocolStateUp},
			},
			bfdSessions: []bird.BfdSession{
				{IP: "169.254.12.150", Interface: "vlan-100", State: "Up"},
			},
			routers: []*meridio2v1alpha1.GatewayRouter{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "gw-static-v4"},
					Spec: meridio2v1alpha1.GatewayRouterSpec{
						Address:   "169.254.12.150",
						Interface: "vlan-100",
						Protocol:  meridio2v1alpha1.RoutingProtocolStatic,
						Static:    &meridio2v1alpha1.StaticSpec{BFD: bfdSpec},
					},
				},
			},
			expectedState: []bird.ProtocolState{bird.ProtocolStateUp},
			expectedInfo:  []string{bird.BgpInfoEstablished},
		},
		{
			name: "static with BFD down - Info set to BFD Down",
			protocols: []bird.ProtocolStatus{
				{Name: "NBR-gw-static-v4", Proto: "Static", State: bird.ProtocolStateUp},
			},
			bfdSessions: []bird.BfdSession{
				{IP: "169.254.12.150", Interface: "vlan-100", State: "Down"},
			},
			routers: []*meridio2v1alpha1.GatewayRouter{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "gw-static-v4"},
					Spec: meridio2v1alpha1.GatewayRouterSpec{
						Address:   "169.254.12.150",
						Interface: "vlan-100",
						Protocol:  meridio2v1alpha1.RoutingProtocolStatic,
						Static:    &meridio2v1alpha1.StaticSpec{BFD: bfdSpec},
					},
				},
			},
			expectedState: []bird.ProtocolState{bird.ProtocolStateUp},
			expectedInfo:  []string{bird.BfdInfoDown},
		},
		{
			name: "static with BFD - session not found - Info set to BFD Down",
			protocols: []bird.ProtocolStatus{
				{Name: "NBR-gw-static-v4", Proto: "Static", State: bird.ProtocolStateUp},
			},
			bfdSessions: []bird.BfdSession{},
			routers: []*meridio2v1alpha1.GatewayRouter{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "gw-static-v4"},
					Spec: meridio2v1alpha1.GatewayRouterSpec{
						Address:   "169.254.12.150",
						Interface: "vlan-100",
						Protocol:  meridio2v1alpha1.RoutingProtocolStatic,
						Static:    &meridio2v1alpha1.StaticSpec{BFD: bfdSpec},
					},
				},
			},
			expectedState: []bird.ProtocolState{bird.ProtocolStateUp},
			expectedInfo:  []string{bird.BfdInfoDown},
		},
		{
			name: "static without BFD - sets Established",
			protocols: []bird.ProtocolStatus{
				{Name: "NBR-gw-static-v4", Proto: "Static", State: bird.ProtocolStateUp},
			},
			bfdSessions: []bird.BfdSession{},
			routers: []*meridio2v1alpha1.GatewayRouter{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "gw-static-v4"},
					Spec: meridio2v1alpha1.GatewayRouterSpec{
						Address:   "169.254.12.150",
						Interface: "vlan-100",
						Protocol:  meridio2v1alpha1.RoutingProtocolStatic,
						Static:    &meridio2v1alpha1.StaticSpec{},
					},
				},
			},
			expectedState: []bird.ProtocolState{bird.ProtocolStateUp},
			expectedInfo:  []string{bird.BgpInfoEstablished},
		},
		{
			name: "BGP protocol - not touched",
			protocols: []bird.ProtocolStatus{
				{Name: "NBR-gw-bgp-v4", Proto: "BGP", State: bird.ProtocolStateUp, Info: "Established"},
			},
			bfdSessions: []bird.BfdSession{},
			routers: []*meridio2v1alpha1.GatewayRouter{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "gw-bgp-v4"},
					Spec: meridio2v1alpha1.GatewayRouterSpec{
						Address:  "169.254.12.150",
						Protocol: meridio2v1alpha1.RoutingProtocolBGP,
					},
				},
			},
			expectedState: []bird.ProtocolState{bird.ProtocolStateUp},
			expectedInfo:  []string{"Established"},
		},
		{
			name: "static protocol already down - not touched",
			protocols: []bird.ProtocolStatus{
				{Name: "NBR-gw-static-v4", Proto: "Static", State: bird.ProtocolStateDown},
			},
			bfdSessions: []bird.BfdSession{
				{IP: "169.254.12.150", Interface: "vlan-100", State: "Up"},
			},
			routers: []*meridio2v1alpha1.GatewayRouter{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "gw-static-v4"},
					Spec: meridio2v1alpha1.GatewayRouterSpec{
						Address:   "169.254.12.150",
						Interface: "vlan-100",
						Protocol:  meridio2v1alpha1.RoutingProtocolStatic,
						Static:    &meridio2v1alpha1.StaticSpec{BFD: bfdSpec},
					},
				},
			},
			expectedState: []bird.ProtocolState{bird.ProtocolStateDown},
			expectedInfo:  []string{""},
		},
		{
			name: "static protocol with no matching router - not touched",
			protocols: []bird.ProtocolStatus{
				{Name: "NBR-unknown-static", Proto: "Static", State: bird.ProtocolStateUp},
			},
			bfdSessions:   []bird.BfdSession{},
			routers:       []*meridio2v1alpha1.GatewayRouter{},
			expectedState: []bird.ProtocolState{bird.ProtocolStateUp},
			expectedInfo:  []string{""},
		},
		{
			name: "mixed BGP and static with BFD",
			protocols: []bird.ProtocolStatus{
				{Name: "NBR-gw-bgp-v4", Proto: "BGP", State: bird.ProtocolStateUp, Info: "Established"},
				{Name: "NBR-gw-static-v4", Proto: "Static", State: bird.ProtocolStateUp},
				{Name: "NBR-gw-static-v6", Proto: "Static", State: bird.ProtocolStateUp},
			},
			bfdSessions: []bird.BfdSession{
				{IP: "169.254.12.150", Interface: "vlan-100", State: "Up"},
				{IP: "fd00::150", Interface: "vlan-100", State: "Down"},
			},
			routers: []*meridio2v1alpha1.GatewayRouter{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "gw-bgp-v4"},
					Spec: meridio2v1alpha1.GatewayRouterSpec{
						Address:  "169.254.12.100",
						Protocol: meridio2v1alpha1.RoutingProtocolBGP,
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Name: "gw-static-v4"},
					Spec: meridio2v1alpha1.GatewayRouterSpec{
						Address:   "169.254.12.150",
						Interface: "vlan-100",
						Protocol:  meridio2v1alpha1.RoutingProtocolStatic,
						Static:    &meridio2v1alpha1.StaticSpec{BFD: bfdSpec},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Name: "gw-static-v6"},
					Spec: meridio2v1alpha1.GatewayRouterSpec{
						Address:   "fd00::150",
						Interface: "vlan-100",
						Protocol:  meridio2v1alpha1.RoutingProtocolStatic,
						Static:    &meridio2v1alpha1.StaticSpec{BFD: bfdSpec},
					},
				},
			},
			expectedState: []bird.ProtocolState{bird.ProtocolStateUp, bird.ProtocolStateUp, bird.ProtocolStateUp},
			expectedInfo:  []string{"Established", bird.BgpInfoEstablished, bird.BfdInfoDown},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			applyBfdState(tt.protocols, tt.bfdSessions, tt.routers, ctrl.Log)

			for i, p := range tt.protocols {
				assert.Equal(t, tt.expectedState[i], p.State, "protocol[%d] %s State", i, p.Name)
				assert.Equal(t, tt.expectedInfo[i], p.Info, "protocol[%d] %s Info", i, p.Name)
			}
		})
	}
}

func TestProtocolStateLabel(t *testing.T) {
	tests := []struct {
		name     string
		protocol bird.ProtocolStatus
		expected string
	}{
		{"state only", bird.ProtocolStatus{State: bird.ProtocolStateUp}, "up"},
		{"state with info", bird.ProtocolStatus{State: bird.ProtocolStateUp, Info: "Established"}, "up/Established"},
		{"down with info",
			bird.ProtocolStatus{State: bird.ProtocolStateDown, Info: "Connection closed"}, "down/Connection closed"},
		{"start no info", bird.ProtocolStatus{State: bird.ProtocolStateStart}, "start"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, protocolStateLabel(tt.protocol))
		})
	}
}

func TestProtocolsEqual(t *testing.T) {
	tests := []struct {
		name     string
		a, b     []bird.ProtocolStatus
		expected bool
	}{
		{"both nil", nil, nil, true},
		{"both empty", []bird.ProtocolStatus{}, []bird.ProtocolStatus{}, true},
		{"nil vs empty", nil, []bird.ProtocolStatus{}, true},
		{"same single", []bird.ProtocolStatus{{Name: "NBR-gw1", State: bird.ProtocolStateUp, Info: "Established"}},
			[]bird.ProtocolStatus{{Name: "NBR-gw1", State: bird.ProtocolStateUp, Info: "Established"}}, true},
		{"different length", []bird.ProtocolStatus{{Name: "NBR-gw1"}},
			[]bird.ProtocolStatus{{Name: "NBR-gw1"}, {Name: "NBR-gw2"}}, false},
		{"different name", []bird.ProtocolStatus{{Name: "NBR-gw1", State: bird.ProtocolStateUp}},
			[]bird.ProtocolStatus{{Name: "NBR-gw2", State: bird.ProtocolStateUp}}, false},
		{"different state", []bird.ProtocolStatus{{Name: "NBR-gw1", State: bird.ProtocolStateUp}},
			[]bird.ProtocolStatus{{Name: "NBR-gw1", State: bird.ProtocolStateDown}}, false},
		{"different info", []bird.ProtocolStatus{{Name: "NBR-gw1", State: bird.ProtocolStateUp, Info: "Established"}},
			[]bird.ProtocolStatus{{Name: "NBR-gw1", State: bird.ProtocolStateUp, Info: "Connect"}}, false},
		{"different order", []bird.ProtocolStatus{{Name: "NBR-gw1"}, {Name: "NBR-gw2"}},
			[]bird.ProtocolStatus{{Name: "NBR-gw2"}, {Name: "NBR-gw1"}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, protocolsEqual(tt.a, tt.b))
		})
	}
}
