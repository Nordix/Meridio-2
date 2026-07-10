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

// GatewayConfigurationSpec defines the desired state of GatewayConfiguration.
// Referenced by Gateway.spec.infrastructure.parametersRef to configure the
// LB Deployment that the gateway controller creates for each Gateway.
type GatewayConfigurationSpec struct {

	// networkAttachments defines the secondary network interfaces for the LB
	// Deployment managed by the gateway controller. These only affect the LB Pods
	// and have no relation to network attachments on user application Pods.
	// +kubebuilder:validation:MaxItems=10
	NetworkAttachments []NetworkAttachment `json:"networkAttachments"`

	// internalSubnets identifies the subnet(s) where application endpoint IPs reside.
	// Used by the ENC controller to match secondary interfaces in application Pods
	// and to determine IP family (IPv4/IPv6) for VIP and next-hop assignment.
	// Each entry represents a single subnet of one IP family. For dual-stack, use two
	// separate entries (one IPv4, one IPv6).
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=2
	// +kubebuilder:validation:XValidation:rule="self.size() <= 1 || cidr(self[0].cidr).ip().family() != cidr(self[1].cidr).ip().family()",message="Each internalSubnet must represent a different IP family"
	InternalSubnets []InternalSubnet `json:"internalSubnets"`

	// horizontalScaling controls the replica count of the LB Deployment.
	// For further details on horizontal scaling, see the Kubernetes documentation:
	// https://kubernetes.io/docs/concepts/workloads/autoscaling/horizontal-pod-autoscale/
	HorizontalScaling HorizontalScaling `json:"horizontalScaling"`

	// verticalScaling configures per-container resource requests, limits, and resize
	// policies for the LB Deployment. When omitted, containers use the resources
	// defined in the LB Deployment template.
	// +optional
	VerticalScaling *VerticalScaling `json:"verticalScaling,omitempty"`
}

// InternalSubnet identifies a network segment where application endpoints are reachable.
// Each entry represents a single subnet of one IP family. For dual-stack, use two separate
// InternalSubnet entries (one IPv4, one IPv6).
type InternalSubnet struct {

	// attachmentType specifies how the network is attached to Pods.
	// Currently only NAD (NetworkAttachmentDefinition) is supported.
	// +kubebuilder:default=NAD
	// +kubebuilder:validation:Enum=NAD
	AttachmentType string `json:"attachmentType"`

	// cidr is the subnet CIDR for this network segment (e.g. "192.168.100.0/24").
	// Must not overlap with CIDRs in other InternalSubnets. Default routes (0.0.0.0/0,
	// ::/0) and IPv6 link-local addresses (fe80::/10) are not allowed.
	// +kubebuilder:validation:MaxLength=43
	// +kubebuilder:validation:XValidation:rule=isCIDR(self),message="Must be a valid CIDR notation!"
	CIDR string `json:"cidr"`
}

// NetworkAttachment defines a secondary network interface for the LB Deployment Pods.
// +kubebuilder:validation:XValidation:rule=self.type == "NAD" && self.nad != null,message="NAD field must not be null when type is NAD"
type NetworkAttachment struct {

	// description is a human-readable description of this network attachment.
	// +optional
	Description string `json:"description"`

	// type specifies the attachment mechanism. Currently only NAD is supported.
	// +kubebuilder:default=NAD
	// +kubebuilder:validation:Enum=NAD
	Type string `json:"type"`

	// nad specifies the NetworkAttachmentDefinition reference and interface name.
	// Required when type is NAD.
	// +optional
	NAD *NAD `json:"nad,omitempty"`
}

// NAD references a Multus NetworkAttachmentDefinition and the interface name
// to request on the Pod.
type NAD struct {
	// interface is the name to assign to the network interface inside the Pod (e.g. "net1").
	Interface string `json:"interface"`
	// name is the name of the NetworkAttachmentDefinition resource.
	Name string `json:"name"`
	// namespace is the namespace of the NetworkAttachmentDefinition resource.
	// Defaults to the GatewayConfiguration's namespace if empty.
	Namespace string `json:"namespace"`
}

// HorizontalScaling controls the replica count of the LB Deployment.
type HorizontalScaling struct {

	// replicas is the desired number of LB Deployment Pods.
	// +kubebuilder:default=2
	// +kubebuilder:validation:Minimum=1
	Replicas uint `json:"replicas"`

	// enforceReplicas controls whether the controller manages the replica count.
	// If true, the controller enforces the replicas value on every reconcile.
	// If false, the controller only sets replicas on initial creation, allowing
	// HPA or other autoscalers to manage the count afterward.
	// +kubebuilder:default=false
	EnforceReplicas bool `json:"enforceReplicas"`
}

// VerticalScaling configures per-container resource requirements for the LB Deployment.
type VerticalScaling struct {
	// containers lists per-container resource overrides. Each entry targets a
	// container by name in the LB Deployment template.
	// +optional
	Containers []ContainerArgs `json:"containers,omitempty"`
}

// ContainerArgs defines resource requirements and resize policy for a single container.
type ContainerArgs struct {
	// name is the container name in the LB Deployment template to configure.
	Name string `json:"name"`

	// resources specifies the CPU and memory requests and limits for this container.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// enforceResources controls whether the controller manages this container's resources.
	// If true, the controller enforces the resources value on every reconcile.
	// If false, the controller only sets resources on initial creation, allowing
	// VPA or other tools to manage resources afterward.
	// +kubebuilder:default=false
	EnforceResources bool `json:"enforceResources"`

	// resizePolicy specifies the restart policy for in-place resource resizing per resource type.
	// +optional
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
