/*
Copyright 2026.

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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// GatewayConfigSpec defines the desired state of GatewayConfig

// +kubebuilder:validation:XValidation:rule=self.networkSubnets.size() == self.networks.size(),message="Size of networkSubnets, and networks arrays must match!"
type GatewayConfigSpec struct {

	// List of k8s.v1.cni.cncf.io/networks interfaces, for which gateway workloads should be attached to
	CNINetworks []CNINetwork `json:"cniNetworks"`

	// +kubebuilder:validation:MinItems=1

	// NOTE I'm assuming here that len(Networks) == len(NetworksSubnets) should match, otherwise it would be weird

	// Networks application pods must be attached to in order to consider them as endpoint
	Networks []Network `json:"networks"`

	// +kubebuilder:validation:XValidation:rule=isCIDR(self),message="Must be a valid CIDR notation!"

	// Indicates in which subnet(s) the application endpoint IP(s) are
	NetworkSubnets []string `json:"networkSubnets"`

	HorizontalScaling HorizontalScaling `json:"horizontalScaling"`

	// +optional
	VerticalScaling *VerticalScaling `json:"verticalScaling,omitempty"`
}

type CNINetwork struct {
	Name      string `json:"name"`
	Interface string `json:"interface"`
}

type Network struct {
	Name      string `json:"name"`
	Interface string `json:"net1"`
}

type HorizontalScaling struct {

	// +kubebuilder:default=2
	// +kubebuilder:validation:Minimum=1

	Replicas uint `json:"replicas"`

	// +kubebuilder:default=false
	// Control Knob: If true, the controller enforces 'replicas'.
	// If false, the controller steps aside, allowing HPA to control the Deployment.
	EnforceReplicas bool `json:"enforceReplicas"`
}

type VerticalScaling struct {
	// TODO not sure if we want this as an array, if we only set router, and sllb containers, then just using two fields
	// should suffice.
	// +optional
	Containers []ContainerArgs `json:"containers,omitempty"`

	// Resizing Strategy: Applies to ALL containers where enforceResources is true
	ResizeStrategy ResizeStrategy `json:"resizeStrategy"`
}

type ContainerArgs struct {
	Name string `json:"name"`

	// TODO I've just put the one defined by k8s/core/v1/api here,
	// it has more fields than just limit, and
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// +kubebuilder:default=false

	// Control Knob for VPA Deferral for THIS container
	// If true, controller enforces 'resources' via patch/template.
	// If false, controller ignores 'resources', deferring to VPA/other external tool.
	EnforceResource bool `json:"enforceResources"`
}

type ResizeStrategy struct {
	// +kubebuilder:validation:Enum=InPlace;RollingUpgrade
	Mode string `json:"mode"`
}

// GatewayConfigStatus defines the observed state of GatewayConfig.
type GatewayConfigStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// For Kubernetes API conventions, see:
	// https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md#typical-status-properties

	// conditions represent the current state of the GatewayConfig resource.
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

// GatewayConfig is the Schema for the gatewayconfigs API
type GatewayConfig struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of GatewayConfig
	// +required
	Spec GatewayConfigSpec `json:"spec"`

	// status defines the observed state of GatewayConfig
	// +optional
	Status GatewayConfigStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// GatewayConfigList contains a list of GatewayConfig
type GatewayConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []GatewayConfig `json:"items"`
}

func init() {
	SchemeBuilder.Register(&GatewayConfig{}, &GatewayConfigList{})
}
