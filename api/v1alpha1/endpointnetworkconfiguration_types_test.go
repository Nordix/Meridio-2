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

package v1alpha1

import (
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestEndpointNetworkConfiguration_DeepCopy(t *testing.T) {
	// 1. Construct a fully populated object
	original := &EndpointNetworkConfiguration{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "default",
			Labels:    map[string]string{"app": "meridio"},
		},
		Spec: EndpointNetworkConfigurationSpec{
			Gateways: []GatewayConnection{
				{
					Name: "gateway-1",
					Domains: []NetworkDomain{
						{
							Name:     "domain-1",
							IPFamily: "IPv4",
							Network: NetworkIdentity{
								Subnet:        "192.168.0.0/24",
								InterfaceHint: "net1",
							},
							VIPs:     []string{"20.0.0.1", "20.0.0.2"},
							NextHops: []string{"192.168.0.1"},
						},
						{
							Name:     "domain-2",
							IPFamily: "IPv6",
							Network: NetworkIdentity{
								Subnet:        "100:1::/64",
								InterfaceHint: "net1",
							},
							VIPs:     []string{"2dea::1"},
							NextHops: []string{"100:1::1", "100:1::2"},
						},
					},
				},
			},
		},
		Status: EndpointNetworkConfigurationStatus{
			Conditions: []metav1.Condition{
				{
					Type:   "Ready",
					Status: metav1.ConditionTrue,
					Reason: "Configured",
				},
			},
		},
	}

	// 2. Perform DeepCopy
	copied := original.DeepCopy()

	// 3. Verify Equality and Pointer Independence
	assert.Equal(t, original, copied, "Struct content should be identical")
	assert.NotSame(t, original, copied, "Root pointers must be different")

	// Verify Spec Pointers
	assert.NotSame(t, &original.Spec.Gateways, &copied.Spec.Gateways, "Gateways slice header must be different")
	assert.NotSame(t, &original.Spec.Gateways[0], &copied.Spec.Gateways[0], "Gateway elements must be different addresses")

	// Verify Nested Domain Pointers
	assert.NotSame(t, &original.Spec.Gateways[0].Domains, &copied.Spec.Gateways[0].Domains, "Domains slice header must be different")
	assert.NotSame(t, &original.Spec.Gateways[0].Domains[0].VIPs, &copied.Spec.Gateways[0].Domains[0].VIPs, "VIPs slice header must be different")

	// 4. Verify that modifying the copy does NOT affect the original
	copied.Spec.Gateways[0].Name = "modified-gateway"
	copied.Spec.Gateways[0].Domains[0].VIPs[0] = "1.1.1.1"
	copied.Status.Conditions[0].Type = "ModifiedStatus"

	assert.Equal(t, "gateway-1", original.Spec.Gateways[0].Name, "Original gateway name should remain unchanged")
	assert.Equal(t, "20.0.0.1", original.Spec.Gateways[0].Domains[0].VIPs[0], "Original VIP should remain unchanged")
	assert.Equal(t, "Ready", original.Status.Conditions[0].Type, "Original status condition should remain unchanged")
}
