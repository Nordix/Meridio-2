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
// +kubebuilder:validation:XValidation:rule="self.protocol != 'Static' || has(self.static)",message="static is required when protocol is Static"
// +kubebuilder:validation:XValidation:rule="self.protocol != 'BGP' || has(self.bgp)",message="bgp is required when protocol is BGP"
// +kubebuilder:validation:XValidation:rule="(self.protocol != 'BGP' || !has(self.static)) && (self.protocol != 'Static' || !has(self.bgp))",message="bgp and static are mutually exclusive"
type GatewayRouterSpec struct {
	// gatewayRef references the Gateway this router peers with.
	GatewayRef gatewayapiv1.ParentReference `json:"gatewayRef"`

	// Name of the interface to reach external gateway
	Interface string `json:"interface"`

	// +kubebuilder:validation:XValidation:rule=isIP(self),message=Must be an ip address

	// Address of the Gateway Router
	Address string `json:"address"`

	// protocol selects the routing protocol for this peering.
	// +kubebuilder:validation:Enum=BGP;Static
	Protocol RoutingProtocol `json:"protocol"`

	// bgp defines BGP session parameters. Required when protocol is BGP.
	// Parameters to set up the BGP session to specified Address.
	// If the Protocol is bgp, the minimal parameters to be defined in bgp properties
	// are RemoteASN and LocalASN
	// +optional
	BGP *BgpSpec `json:"bgp,omitempty"`

	// static defines static routing with BFD supervision. Required when protocol is Static.
	// +optional
	Static *StaticSpec `json:"static,omitempty"`
}

type BgpSpec struct {
	// The ASN number of the Gateway Router
	//
	// Note: Format="" suppresses the default int32 format that kubebuilder generates
	// for uint32 fields. Without it, 4-byte ASNs (> 2147483647) would be rejected
	// by the CRD schema validation. The explicit Maximum/Minimum enforce the full
	// uint32 range at the API level.
	// +kubebuilder:validation:Format=""
	// +kubebuilder:validation:Maximum=4294967295
	// +kubebuilder:validation:Minimum=0
	// +required
	RemoteASN uint32 `json:"remoteASN"`

	// The ASN number of the system where the Attractor FrontEnds locates
	//
	// Note: Format="" suppresses the default int32 format that kubebuilder generates
	// for uint32 fields. Without it, 4-byte ASNs (> 2147483647) would be rejected
	// by the CRD schema validation. The explicit Maximum/Minimum enforce the full
	// uint32 range at the API level.
	// +kubebuilder:validation:Format=""
	// +kubebuilder:validation:Maximum=4294967295
	// +kubebuilder:validation:Minimum=0
	// +required
	LocalASN uint32 `json:"localASN"`

	// BFD monitoring of BGP session.
	// +optional
	BFD *BfdSpec `json:"bfd,omitempty"`

	// +kubebuilder:validation:XValidation:rule=duration(self) >= duration('3s'),message=Must be at least 3s

	// Hold timer of the BGP session. Please refer to BGP material to understand what this implies.
	// The value must be a valid duration format. For example, 90s, 1m, 1h.
	// The duration will be rounded by second
	// Minimum duration is 3s.
	// +kubebuilder:default="240s"
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

	// BGP authentication with TCP Authentication Option (RFC5925).
	// +optional
	Authentication *BgpTcpAoSpec `json:"authentication,omitempty"`
}

// +enum
type RoutingProtocol string

const (
	RoutingProtocolBGP    RoutingProtocol = "BGP"
	RoutingProtocolStatic RoutingProtocol = "Static"
)

type StaticSpec struct {
	// BFD monitoring of the static next-hop.
	// +optional
	BFD *BfdSpec `json:"bfd,omitempty"`
}

type BfdSpec struct {
	// Min-tx timer of bfd session. Please refer to bird BFD documentation to understand what this implies.
	// The value must be a valid duration format. For example, 300ms, 90s, 1m, 1h.
	// The duration will be rounded by millisecond.
	// +required
	MinTx string `json:"minTx"`

	// Min-rx timer of bfd session. Please refer to bird BFD documentation to understand what this implies.
	// The value must be a valid duration format. For example, 300ms, 90s, 1m, 1h.
	// The duration will be rounded by millisecond.
	// +required
	MinRx string `json:"minRx"`

	// Multiplier of bfd session.
	// When this number of bfd packets failed to receive, bfd session will go down.
	// +required
	Multiplier uint16 `json:"multiplier"`
}

// BgpTcpAo defines the parameters to configure TCP Authentication Option (RFC5925).
type BgpTcpAoSpec struct {
	// KeyChain defines the list of TCP-AO keys for authentication and rotation.
	// At least one key must be provided.
	// +kubebuilder:validation:MinItems=1
	Keychain []TcpAoKeyChain `json:"keychain"`

	// CurrentKeyId specifies the active key ID for sending (only this key gets preferred in BIRD).
	// If unset, no keys are marked preferred.
	// +optional
	CurrentKeyId *uint8 `json:"currentKeyId,omitempty"`

	// NextKeyId specifies the key ID the peer should transition to (maps to BIRD's rnext id).
	// +optional
	NextKeyId *uint8 `json:"nextKeyId,omitempty"`
}

// TcpAoKeyChain defines a single TCP-AO key configuration.
type TcpAoKeyChain struct {
	// SendId is the Send_ID for this key (0-255).
	SendId uint8 `json:"sendId"`

	// RecvId is the Recv_ID for this key (0-255).
	RecvId uint8 `json:"recvId"`

	// Algorithm specifies the MAC algorithm for this key.
	// Supported by BIRD as of version 3.3.1:
	// "hmac md5"
	// "hmac sha1"
	// "hmac sha224"
	// "hmac sha256"
	// "hmac sha384"
	// "hmac sha512"
	// "cmac aes128"
	// Unknown values are passed through and rejected by BIRD at config load.
	// +kubebuilder:validation:MinLength=1
	Algorithm string `json:"algorithm"`

	// SecretName is the name of the Kubernetes Secret containing the master key.
	// +kubebuilder:validation:MinLength=1
	SecretName string `json:"secretName"`

	// SecretKey is the key in the Secret's data section containing the master key value.
	// +kubebuilder:validation:MinLength=1
	SecretKey string `json:"secretKey"`
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
