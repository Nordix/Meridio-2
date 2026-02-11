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
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// +enum
type TransportProtocol string

const (
	// TCP represents the layer 4 protocol.
	TCP TransportProtocol = "TCP"
	// UDP represents the layer 4 protocol.
	UDP TransportProtocol = "UDP"
	// SCTP represents the layer 4 protocol.
	SCTP TransportProtocol = "SCTP"
)

// L34RouteSpec defines the desired state of L34Route
type L34RouteSpec struct {
	// ParentRefs specifies the Gateway in which the route will be configured.
	// Reference to gatewayapiv1.Gateway object.
	//
	// A L34Route defines the set of VIP addresses that a Gateway must support,
	// so only a single parent Gateway is allowed. Reuse of an L34Route across
	// multiple Gateways is not supported, as VIP ownership would otherwise
	// become ambiguous.
	//
	// Future extensions could consider allowing shared L34Routes, but (possibly)
	// only for Route objects that do not define destinationCIDRs.
	//
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=1
	ParentRefs []gatewayapiv1.ParentReference `json:"parentRefs"`

	// BackendRefs defines the backend(s) where matching requests should be
	// sent. If unspecified or invalid (refers to a non-existent resource or a
	// Service with no endpoints), the underlying implementation MUST actively
	// reject connection attempts to this backend. Connection rejections must
	// respect weight; if an invalid backend is requested to have 80% of
	// connections, then 80% of connections must be rejected instead.
	//
	// Support: Core for Kubernetes Service
	//
	// Support: Extended for Kubernetes ServiceImport
	//
	// Support: Implementation-specific for any other resource
	//
	// Support for weight: Extended
	//
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=1
	BackendRefs []gatewayapiv1.BackendRef `json:"backendRefs,omitempty"`

	// Destination CIDRs that this L34Route will send traffic to.
	// It is interpreted by the implementation as the set of VIPs exposed by the Gateway.
	// The destination CIDRs should not have overlaps.
	// Each destinationCIDR must be an IPv4/32 or IPv6/128 CIDR
	// +kubebuilder:validation:MaxItems=100
	// +kubebuilder:validation:items:MaxLength=50
	// +kubebuilder:validation:XValidation:message="each destinationCIDR must be an IPv4/32 or IPv6/128 CIDR",rule="self.all(c, isCIDR(c) && (cidr(c).prefixLength() == 32 || cidr(c).prefixLength() == 128))"
	//nolint:tagliatelle
	DestinationCIDRs []string `json:"destinationCIDRs"`

	// Source CIDRs allowed in the L34Route.
	// The source CIDRs should not have overlaps.
	// +kubebuilder:validation:MaxItems=100
	// +kubebuilder:validation:items:MaxLength=50
	// +kubebuilder:validation:XValidation:message="each sourceCIDR must be a valid CIDR",rule="self.all(c, isCIDR(c))"
	//nolint:tagliatelle
	SourceCIDRs []string `json:"sourceCIDRs,omitempty"`

	// Source port ranges allowed in the L34Route.
	// The ports should not have overlaps.
	// Ports can be defined by:
	// - a single port, such as 3000;
	// - a port range, such as 3000-4000;
	// - "any", which is equivalent to port range 0-65535.
	// +kubebuilder:validation:MaxItems=20
	// +kubebuilder:validation:items:MaxLength=11
	// +kubebuilder:validation:XValidation:message="each sourcePort must be a single port, a port range, or 'any'",rule="self.all(port, port == 'any' || (port.matches('^\\\\d+$') && int(port) >= 0 && int(port) <= 65535) || (port.matches('^\\\\d+-\\\\d+$') && int(port.split('-')[0]) >= 0 && int(port.split('-')[0]) <= 65535 && int(port.split('-')[1]) >= 0 && int(port.split('-')[1]) <= 65535 && int(port.split('-')[0]) <= int(port.split('-')[1])))"
	SourcePorts []string `json:"sourcePorts,omitempty"`

	// Destination port ranges allowed in the L34Route.
	// The ports should not have overlaps.
	// Ports can be defined by:
	// - a single port, such as 3000;
	// - a port range, such as 3000-4000;
	// - "any", which is equivalent to port range 0-65535.
	// +kubebuilder:validation:MaxItems=20
	// +kubebuilder:validation:items:MaxLength=11
	// +kubebuilder:validation:XValidation:message="each destinationPort must be a single port, a port range, or 'any'",rule="self.all(port, port == 'any' || (port.matches('^\\\\d+$') && int(port) >= 0 && int(port) <= 65535) || (port.matches('^\\\\d+-\\\\d+$') && int(port.split('-')[0]) >= 0 && int(port.split('-')[0]) <= 65535 && int(port.split('-')[1]) >= 0 && int(port.split('-')[1]) <= 65535 && int(port.split('-')[0]) <= int(port.split('-')[1])))"
	DestinationPorts []string `json:"destinationPorts,omitempty"`

	// Protocols allowed in this L34Route.
	// The protocols should not have overlaps.
	// +kubebuilder:validation:MaxItems=3
	// +kubebuilder:validation:XValidation:message="protocols must not contain duplicates",rule="self.all(p, self.exists_one(x, x == p))"
	Protocols []TransportProtocol `json:"protocols"`

	// Priority of the L34Route
	// Multiple L34Route resources may be associated with the same Gateway, and multiple Routes may reference the same backendRef.
	// When multiple Routes match a packet, the Route with the highest priority value is selected.
	// The priority is greater than 0
	// +kubebuilder:validation:Minimum=1
	Priority int32 `json:"priority"`

	// ByteMatches matches bytes in the L4 header in the L34Route.
	// +optional
	// +kubebuilder:validation:MaxItems=10
	// +kubebuilder:validation:items:MaxLength=80
	// +kubebuilder:validation:XValidation:message="each byteMatch must be a valid string",rule="self.all(byteMatch, byteMatch.matches(\"^(sctp|tcp|udp)\\\\[[0-9]+ *: *[124]\\\\]( *& *0x[0-9a-f]+)? *= *([0-9]+|0x[0-9a-f]+)$\"))"
	ByteMatches []string `json:"byteMatches,omitempty"`
}

// L34RouteStatus defines the observed state of L34Route.
type L34RouteStatus struct{}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// L34Route is the Schema for the L34Routes API
type L34Route struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of L34Route
	// +required
	Spec L34RouteSpec `json:"spec"`

	// status defines the observed state of L34Route
	// +optional
	Status L34RouteStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// L34RouteList contains a list of L34Route resources.
type L34RouteList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`
	Items           []L34Route `json:"items"`
}

func init() {
	SchemeBuilder.Register(&L34Route{}, &L34RouteList{})
}
