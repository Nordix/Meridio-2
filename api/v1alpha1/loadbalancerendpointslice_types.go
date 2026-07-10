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

// LoadBalancerEndpointSliceSpec defines the desired state of LoadBalancerEndpointSlice.
type LoadBalancerEndpointSliceSpec struct {

	// distributionGroupName is the name of the DistributionGroup that owns this slice.
	// Same-namespace relationship is enforced by ownerReference.
	// +kubebuilder:validation:MinLength=1
	DistributionGroupName string `json:"distributionGroupName"`

	// gatewayRef identifies the Gateway this slice is scoped to.
	// Enables per-Gateway endpoint discovery by the LB controller.
	// +kubebuilder:validation:Required
	GatewayRef SliceGatewayRef `json:"gatewayRef"`

	// endpoints is the list of endpoints in this slice.
	// A Pod's addresses always reside in the same slice object (no cross-object correlation).
	// +listType=atomic
	Endpoints []LoadBalancerEndpoint `json:"endpoints,omitempty"`
}

// SliceGatewayRef identifies a Gateway for endpoint scoping.
type SliceGatewayRef struct {
	// name is the name of the Gateway.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// namespace is the namespace of the Gateway.
	// +kubebuilder:validation:MinLength=1
	Namespace string `json:"namespace"`
}

// LoadBalancerEndpoint represents a single endpoint with its dual-stack addresses
// and distribution algorithm metadata.
type LoadBalancerEndpoint struct {

	// target identifies the Pod backing this endpoint.
	// +kubebuilder:validation:Required
	Target EndpointTarget `json:"target"`

	// addresses is the list of IP addresses for this endpoint.
	// Dual-stack endpoints have both IPv4 and IPv6 entries in this single list.
	// Exactly one address per IP family: each endpoint occupies a single slot in
	// the distribution algorithm, so multiple IPs of the same family on the same
	// Pod would not increase weight or resilience (same slot, same failure domain).
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=2
	// +listType=map
	// +listMapKey=family
	Addresses []EndpointAddress `json:"addresses"`

	// identifier is the distribution-algorithm-assigned slot index.
	// Present when the distribution type uses numeric IDs (e.g., Maglev).
	// Absent for distribution types that do not assign per-endpoint IDs.
	// +optional
	Identifier *int32 `json:"identifier,omitempty"`

	// ready indicates whether this endpoint is ready to receive traffic.
	// The DG controller mirrors Pod readiness; the LB controller decides
	// activation policy based on this field.
	Ready bool `json:"ready"`
}

// EndpointTarget identifies the Pod backing an endpoint.
type EndpointTarget struct {
	// name is the Pod name.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// uid is the Pod UID for correlation and staleness detection.
	// +kubebuilder:validation:MinLength=1
	UID string `json:"uid"`
}

// EndpointAddress represents a single IP address with its family.
type EndpointAddress struct {
	// ip is the IP address of the endpoint.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=45
	IP string `json:"ip"`

	// family is the IP address family.
	// +kubebuilder:validation:Enum=IPv4;IPv6
	Family IPFamily `json:"family"`
}

// IPFamily represents the IP address family.
// +enum
type IPFamily string

const (
	// IPv4 represents the IPv4 address family.
	IPv4 IPFamily = "IPv4"
	// IPv6 represents the IPv6 address family.
	IPv6 IPFamily = "IPv6"
)

// +genclient
// +kubebuilder:object:root=true
// +kubebuilder:resource:shortName=lbeslice,categories={meridio,all}
// +kubebuilder:printcolumn:name="Distribution-Group",type="string",JSONPath=".spec.distributionGroupName"
// +kubebuilder:printcolumn:name="Gateway",type="string",JSONPath=".spec.gatewayRef.name"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// LoadBalancerEndpointSlice defines a set of endpoints for a DistributionGroup
// scoped to a specific Gateway. It is the contract between the DG controller
// (producer) and the LB controller (consumer) for endpoint discovery.
type LoadBalancerEndpointSlice struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec defines the endpoints and their distribution metadata.
	// +required
	Spec LoadBalancerEndpointSliceSpec `json:"spec"`
}

// +kubebuilder:object:root=true

// LoadBalancerEndpointSliceList contains a list of LoadBalancerEndpointSlice.
type LoadBalancerEndpointSliceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []LoadBalancerEndpointSlice `json:"items"`
}

func init() {
	SchemeBuilder.Register(&LoadBalancerEndpointSlice{}, &LoadBalancerEndpointSliceList{})
}
