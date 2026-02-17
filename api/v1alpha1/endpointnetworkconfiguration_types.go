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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NetworkIdentity defines the criteria used to identify and verify the target
// secondary interface within the Pod.
type NetworkIdentity struct {
	// Subnet: The network prefix expected on the target interface (e.g., "192.168.1.0/24").
	// The processing entity MUST verify this subnet matches the interface's primary IP
	// to ensure configuration is applied to the correct network segment.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=64
	Subnet string `json:"subnet"`

	// InterfaceHint: An optional interface name (e.g., "net1") to accelerate
	// the discovery process.
	// +optional
	InterfaceHint string `json:"interfaceHint,omitempty"`
}

// NetworkDomain represents the desired networking state for a specific network segment.
//
// +kubebuilder:validation:XValidation:rule="cidr(self.network.subnet).ip().family() == (self.ipFamily == 'IPv4' ? 4 : 6)",message="Subnet IP family mismatch"
type NetworkDomain struct {
	// Name: A unique logical identifier for this domain (e.g., "sllb-a-v4").
	// The consumer uses this name to maintain local resource state, such as
	// routing table ID mapping.
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// IPFamily: The IP version used in this domain.
	// This helps the consumer apply protocol-specific settings (like IPv6 nodad).
	// +kubebuilder:validation:Enum=IPv4;IPv6
	// +kubebuilder:validation:Required
	IPFamily string `json:"ipFamily"`

	// Network: Parameters used to identify and validate the physical or
	// virtual interface associated with this domain.
	// +kubebuilder:validation:Required
	Network NetworkIdentity `json:"network"`

	// VIPs: Virtual IP addresses to be assigned as local IPs on the interface.
	// These addresses can serve as the source IPs for outbound traffic; the consumer
	// MUST ensure traffic originating from these VIPs is steered according to the
	// domain's configuration (e.g., via source-based routing).
	// +optional
	// +listType=atomic
	VIPs []string `json:"vips,omitempty"`

	// NextHops: Addresses for source-based routing.
	// If provided, the consumer MUST steer all return traffic or traffic
	// originating from a VIP through these IPs using a dedicated routing table.
	// +optional
	// +listType=atomic
	NextHops []string `json:"nextHops,omitempty"`
}

// GatewayConnection defines the desired state for a specific Gateway connection.
type GatewayConnection struct {
	// Name: The identifier of the remote Gateway.
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// Domains: The dual-stack network configurations for this specific Gateway.
	// +kubebuilder:validation:MaxItems=2
	// +listType=map
	// +listMapKey=ipFamily
	Domains []NetworkDomain `json:"domains"`
}

// EndpointNetworkConfigurationSpec defines the desired state of the Pod's networking.
type EndpointNetworkConfigurationSpec struct {
	// Gateways: List of Gateways the Pod has joined.
	// +optional
	// +listType=map
	// +listMapKey=name
	Gateways []GatewayConnection `json:"gateways,omitempty"`
}

// EndpointNetworkConfigurationStatus defines the observed state of the network plumbing.
// Using ObservedGeneration within conditions is recommended to track which version of the
// Spec each individual condition represents.
type EndpointNetworkConfigurationStatus struct {
	// Conditions represent the latest available observations of the state.
	// Common types: "Ready".
	// The status of each condition is one of True, False, or Unknown.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +genclient
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=enc,categories={meridio,all}
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="Observed-Gen",type="integer",JSONPath=".status.conditions[?(@.type=='Ready')].observedGeneration"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// EndpointNetworkConfiguration is the Schema for the endpointnetworkconfigurations API.
// Defines the declarative network contract for a specific application Pod.
// The name of the resource MUST be identical to the Pod's name for
// automatic discovery by local agents.
type EndpointNetworkConfiguration struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   EndpointNetworkConfigurationSpec   `json:"spec,omitempty"`
	Status EndpointNetworkConfigurationStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// EndpointNetworkConfigurationList contains a list of EndpointNetworkConfiguration
type EndpointNetworkConfigurationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []EndpointNetworkConfiguration `json:"items"`
}

func init() {
	SchemeBuilder.Register(&EndpointNetworkConfiguration{}, &EndpointNetworkConfigurationList{})
}
