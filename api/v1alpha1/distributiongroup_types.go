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

type DistributionGroupType string

const (
	DistributionGroupTypeMaglev DistributionGroupType = "Maglev"
)

// ParentReference mirrors Gateway API's ParentReference
// avoiding hard dependency towards the API.
type ParentReference struct {
	// Group is the API group of the referent.
	// +kubebuilder:default="gateway.networking.k8s.io"
	// +optional
	Group *string `json:"group,omitempty"`

	// Kind is the type of the referent.
	// +kubebuilder:default="Gateway"
	// +optional
	Kind *string `json:"kind,omitempty"`

	// Name is the name of the referent.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Namespace is the namespace of the referent.
	// +optional
	Namespace *string `json:"namespace,omitempty"`
}

// MaglevConfig defines the parameters for the Maglev hashing.
type MaglevConfig struct {
	// MaxEndpoints is the table capacity. This is currently immutable because
	// changing the capacity causes a complete reshuffle of the lookup table,
	// disrupting all active connections.
	// +kubebuilder:default=32
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="MaxEndpoints is immutable"
	MaxEndpoints int32 `json:"maxEndpoints"`
}

// DistributionGroupSpec defines the desired state of DistributionGroup.
type DistributionGroupSpec struct {
	// Selector restricts connectivity to endpoints that match these labels.
	// This uses the standard Kubernetes LabelSelector, supporting both
	// matchLabels and matchExpressions.
	// +optional
	Selector *metav1.LabelSelector `json:"selector,omitempty"`

	// Type determines how the DistributionGroup is distributed.
	// Defaults to Maglev. This is a discriminator for the config blocks below.
	// +kubebuilder:default=Maglev
	// +kubebuilder:validation:Enum=Maglev
	// +optional
	Type DistributionGroupType `json:"type,omitempty"`

	// Maglev contains parameters for the Maglev hash-ring.
	// Only valid when Type is Maglev.
	//
	// If field is omitted, the controller MUST apply the default values.
	// This defaulting allows the API to remain extensible; adding new
	// distribution types in the future won't result in the API server
	// injecting empty configuration blocks for unused types. It also
	// eliminates the need for Mutating Admission Webhooks to handle
	// conditional defaults.
	// +optional
	Maglev *MaglevConfig `json:"maglev,omitempty"`

	// ParentRefs binds this group to specific Gateways for network context.
	// This provides the secondary network identity for the endpoints.
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=1
	ParentRefs []ParentReference `json:"parentRefs,omitempty"`
}

type DistributionGroupStatus struct {
	// Conditions represent the latest available observations of the group's state.
	// Common types: "Ready", "Synchronized".
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +genclient
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=distg,categories={meridio,all}
// +kubebuilder:printcolumn:name="Type",type="string",JSONPath=".spec.type"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// DistributionGroup is the Schema for the distributiongroups API.
// It defines a logical set of endpoints on a secondary network and the
// policy by which they are accessed (e.g. Maglev).
//
// Ensure the Type cannot be changed after creation to prevent data-plane disruption.
// +kubebuilder:validation:XValidation:rule="oldSelf.spec.type == self.spec.type",message="Type is immutable"
//
// Ensure the maglev block is ONLY used when type is Maglev.
// +kubebuilder:validation:XValidation:rule="self.spec.type == 'Maglev' ? true : !has(self.spec.maglev)",message="maglev configuration can only be set when type is Maglev"
//
// Ensure all parentRefs are Kind 'Gateway'.
// +kubebuilder:validation:XValidation:rule="!has(self.spec.parentRefs) || self.spec.parentRefs.all(p, p.kind == 'Gateway')",message="Only Kind 'Gateway' is supported as a parent reference"
type DistributionGroup struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Spec defines the behavior of a DistributionGroup.
	// +optional
	Spec DistributionGroupSpec `json:"spec,omitempty"`

	// Most recently observed status of the DistributionGroup.
	// Populated by the system. Read-only.
	// +optional
	Status DistributionGroupStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// DistributionGroupList contains a list of DistributionGroup
type DistributionGroupList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DistributionGroup `json:"items"`
}

func init() {
	SchemeBuilder.Register(&DistributionGroup{}, &DistributionGroupList{})
}
