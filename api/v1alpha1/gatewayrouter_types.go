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

// GatewayRouterSpec defines the desired state of GatewayRouter
type GatewayRouterSpec struct {

	GatewayRef gatewayapiv1.ParentReference `json:"gatewayRef"`

	// Name of the interface to reach external gateway
	Interface string `json:"interface"`

	//+kubebuilder:validation:XValidation:rule=isIP(self),message=Must be an ip address

	// Address of the Gateway Router
	Address string `json:"address"`

	// Parameters to set up the BGP session to specified Address.
	// If the Protocol is bgp, the minimal parameters to be defined in bgp properties
	// are RemoteASN and LocalASN
	BGP BgpSpec `json:"bgp"`
}

type BgpSpec struct {
	// The ASN number of the Gateway Router
	RemoteASN uint32 `json:"remoteASN"`

	// The ASN number of the system where the Attractor FrontEnds locates
	LocalASN uint32 `json:"localASN"`

	// BFD monitoring of BGP session.
	// +optional
	BFD *BfdSpec `json:"bfd,omitempty"`

	// +kubebuilder:validation:XValidation:rule=duration(self) >= duration('3s'),message=Must be at least 3s

	// Hold timer of the BGP session. Please refere to BGP material to understand what this implies.
	// The value must be a valid duration format. For example, 90s, 1m, 1h.
	// The duration will be rounded by second
	// Minimum duration is 3s.
	// +optional
	HoldTime string `json:"holdTime,omitempty"`

	// +kubebuilder:default=179
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535

	// BGP listening port of the Gateway Router.
	// +optional
	RemotePort *uint16 `json:"remotePort,omitempty"`

	// +kubebuilder:default=179
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535

	// BGP listening port of the Attractor FrontEnds.
	// +optional
	LocalPort *uint16 `json:"localPort,omitempty"`
}

type BfdSpec struct {
	// BFD monitoring.
	// Valid values are:
	// - false: no BFD monitoring;
	// - true: turns on the BFD monitoring.
	// When left empty, there is no BFD monitoring.
	// +optional
	Switch *bool `json:"switch,omitempty"`

	// Min-tx timer of bfd session. Please refere to BFD material to understand what this implies.
	// The value must be a valid duration format. For example, 300ms, 90s, 1m, 1h.
	// The duration will be rounded by millisecond.
	// +optional
	MinTx string `json:"minTx,omitempty"`

	// Min-rx timer of bfd session. Please refere to BFD material to understand what this implies.
	// The value must be a valid duration format. For example, 300ms, 90s, 1m, 1h.
	// The duration will be rounded by millisecond.
	// +optional
	MinRx string `json:"minRx,omitempty"`

	// Multiplier of bfd session.
	// When this number of bfd packets failed to receive, bfd session will go down.
	// +optional
	Multiplier *uint16 `json:"multiplier,omitempty"`
}

// GatewayRouterStatus defines the observed state of GatewayRouter.
type GatewayRouterStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// For Kubernetes API conventions, see:
	// https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md#typical-status-properties

	// conditions represent the current state of the GatewayRouter resource.
	// Each condition has a unique type and reflects the status of a specific aspect of the resource.
	//
	// Standard condition types include:
	// - "Available": the resource is fully functional
	// - "Progressing": the resource is being created or updated
	// - "Degraded": the resource failed to reach or maintain its desired state
	//
	// The status of each condition is one of True, False, or Unknown.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// GatewayRouter is the Schema for the gatewayrouters API
type GatewayRouter struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of GatewayRouter
	// +required
	Spec GatewayRouterSpec `json:"spec"`

	// status defines the observed state of GatewayRouter
	// +optional
	Status GatewayRouterStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// GatewayRouterList contains a list of GatewayRouter
type GatewayRouterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []GatewayRouter `json:"items"`
}

func init() {
	SchemeBuilder.Register(&GatewayRouter{}, &GatewayRouterList{})
}
