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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// GatewayConfigurationSpec defines the desired state of GatewayConfiguration

type GatewayConfigurationSpec struct {

	// +kubebuilder:validation:MaxItems=2
	NetworkAttachments []NetworkAttachment `json:"networkAttachments"`

	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=2

	// Indicates in which subnet(s) the application endpoint IP(s) are, is distinc for each type of network
	NetworkSubnets []NetworkSubnet `json:"networkSubnets"`

	HorizontalScaling HorizontalScaling `json:"horizontalScaling"`

	// +optional
	VerticalScaling *VerticalScaling `json:"verticalScaling,omitempty"`
}

type NetworkSubnet struct {

	// +kubebuilder:default=NAD
	// +kubebuilder:validation:Enum=NAD;DRA
	AttachmentType string `json:"attachmentType"`

	// +kubebuilder:validation:MaxItems=15
	// +kubebuilder:validation:items:XValidation:rule=isCIDR(self),message="Must be a valid CIDR notation!"
	CIDRs []string `json:"cidrs"`
}

// +kubebuilder:validation:XValidation:rule=self.type == "NAD" && self.nad != null || self.type == "DRA" && self.dra != null,message="If type is NAD, field NAD must not be null, otherwise DRA must not be null"
type NetworkAttachment struct {

	// +optional
	Description string `json:"description"`

	// +kubebuilder:default=NAD
	// +kubebuilder:validation:Enum=NAD;DRA
	Type string `json:"type"`

	// +optional
	NAD *NAD `json:"nad,omitempty"`

	// +optional
	DRA *DRA `json:"dra,omitempty"`
}

type NAD struct {
	Interface string `json:"interface"`
	Name string `json:"name"`
	Namespace string `json:"namespace"`
}

// TODO implement
type DRA struct { /* ... */ }

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
	// +optional
	Containers []ContainerArgs `json:"containers,omitempty"`
}

type ContainerArgs struct {
	Name string `json:"name"`

	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// +kubebuilder:default=false

	// Control Knob for VPA Deferral for THIS container
	// If true, controller enforces 'resources' via patch/template.
	// If false, controller ignores 'resources', deferring to VPA/other external tool.
	EnforceResources bool `json:"enforceResources"`

	ResizePolicy []corev1.ContainerResizePolicy `json:"resizePolicy,omitempty"`
}

// GatewayConfigurationStatus defines the observed state of GatewayConfiguration.
type GatewayConfigurationStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// For Kubernetes API conventions, see:
	// https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md#typical-status-properties

	// conditions represent the current state of the GatewayConfiguration resource.
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

// GatewayConfiguration is the Schema for the gatewayconfigurations API
type GatewayConfiguration struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of GatewayConfiguration
	// +required
	Spec GatewayConfigurationSpec `json:"spec"`

	// status defines the observed state of GatewayConfiguration
	// +optional
	Status GatewayConfigurationStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// GatewayConfigurationList contains a list of GatewayConfiguration
type GatewayConfigurationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []GatewayConfiguration `json:"items"`
}

func init() {
	SchemeBuilder.Register(&GatewayConfiguration{}, &GatewayConfigurationList{})
}
