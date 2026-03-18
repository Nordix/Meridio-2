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

package gateway

import (
	"context"
	"fmt"
	"net"
	"strings"

	netdefv1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	meridio2v1alpha1 "github.com/nordix/meridio-2/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// validationError represents a validation failure (not an API error)
type validationError struct {
	message string
}

func (e *validationError) Error() string {
	return e.message
}

// validateGateway fetches GatewayConfiguration, loads template, and validates both
// Returns validationError for invalid config, templateError for template load failures, or API errors
func (r *GatewayReconciler) validateGateway(ctx context.Context, gw *gatewayv1.Gateway) (*meridio2v1alpha1.GatewayConfiguration, *appsv1.Deployment, error) {
	// Validate and fetch GatewayConfiguration
	if gw.Spec.Infrastructure == nil || gw.Spec.Infrastructure.ParametersRef == nil {
		return nil, nil, &validationError{message: "GatewayConfiguration reference is required"}
	}
	ref := gw.Spec.Infrastructure.ParametersRef
	if string(ref.Group) != meridio2v1alpha1.GroupVersion.Group || string(ref.Kind) != kindGatewayConfiguration {
		return nil, nil, &validationError{
			message: fmt.Sprintf("unsupported parametersRef: group=%q kind=%q (expected %s/%s)",
				ref.Group, ref.Kind, meridio2v1alpha1.GroupVersion.Group, kindGatewayConfiguration),
		}
	}
	var gwConfig meridio2v1alpha1.GatewayConfiguration
	if err := r.Get(ctx, client.ObjectKey{Namespace: gw.Namespace, Name: ref.Name}, &gwConfig); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil, &validationError{
				message: fmt.Sprintf("GatewayConfiguration %q not found", ref.Name),
			}
		}
		return nil, nil, err // API error, retry
	}

	// Load template
	template, err := r.loadTemplate()
	if err != nil {
		return nil, nil, err
	}

	// Validate configuration fields
	if err := r.validateConfigurationFields(&gwConfig, template); err != nil {
		return nil, nil, err
	}

	return &gwConfig, template, nil
}

// validateConfigurationFields validates GatewayConfiguration fields and merged network attachments
// Returns validationError for invalid config (caller should set Accepted=False)
func (r *GatewayReconciler) validateConfigurationFields(gwConfig *meridio2v1alpha1.GatewayConfiguration, template *appsv1.Deployment) error {
	// Validate GatewayConfiguration fields
	if err := validateNetworkSubnets(gwConfig.Spec.NetworkSubnets); err != nil {
		return err
	}
	if err := validateNetworkAttachments(gwConfig.Spec.NetworkAttachments); err != nil {
		return err
	}
	if err := validateVerticalScaling(gwConfig.Spec.VerticalScaling); err != nil {
		return err
	}

	// Validate merged network attachments (template + GatewayConfiguration)
	if err := validateMergedNetworkAttachments(gwConfig, template); err != nil {
		return err
	}

	return nil
}

// validateMergedNetworkAttachments validates the final merged NAD list (template + GatewayConfiguration)
// Checks for duplicate interface names in the merged result
// Follows same merge logic as applyNetworkAttachments for consistency
// Assumes gwConfig.Spec.NetworkAttachments has already been validated (no duplicate interfaces within GatewayConfiguration)
func validateMergedNetworkAttachments(gwConfig *meridio2v1alpha1.GatewayConfiguration, template *appsv1.Deployment) error {
	// If no template annotations, GatewayConfiguration NADs are already validated - nothing to check
	if template.Spec.Template.Annotations == nil {
		return nil
	}

	// Extract template NAD annotation
	templateNADAnnotation := template.Spec.Template.Annotations[netdefv1.NetworkAttachmentAnnot]
	if templateNADAnnotation == "" {
		return nil
	}

	// Parse template NADs
	templateNADs := parseNetworkAnnotation(templateNADAnnotation)
	if len(templateNADs) == 0 {
		return nil
	}

	defaultNs := gwConfig.Namespace // NADs default to GatewayConfiguration's namespace

	// Build GatewayConfiguration NAD map and track interfaces
	gwConfigMap := make(map[string]*netdefv1.NetworkSelectionElement)
	interfaceNames := make(map[string]bool)

	for _, attachment := range gwConfig.Spec.NetworkAttachments {
		if attachment.Type == attachmentTypeNAD && attachment.NAD != nil {
			ns := attachment.NAD.Namespace
			if ns == "" {
				ns = defaultNs
			}
			elem := &netdefv1.NetworkSelectionElement{
				Name:             attachment.NAD.Name,
				Namespace:        ns,
				InterfaceRequest: attachment.NAD.Interface,
			}
			key := ns + "/" + elem.Name + ":" + elem.InterfaceRequest
			gwConfigMap[key] = elem
			interfaceNames[elem.InterfaceRequest] = true
		}
	}

	// Check template NADs not overridden by GatewayConfiguration
	for _, templateNAD := range templateNADs {
		ns := templateNAD.Namespace
		if ns == "" {
			ns = defaultNs
		}
		key := ns + "/" + templateNAD.Name + ":" + templateNAD.InterfaceRequest
		if _, exists := gwConfigMap[key]; !exists {
			// Template NAD not overridden - check interface
			if interfaceNames[templateNAD.InterfaceRequest] {
				return &validationError{
					message: fmt.Sprintf("duplicate interface name %q in merged network attachments", templateNAD.InterfaceRequest),
				}
			}
			interfaceNames[templateNAD.InterfaceRequest] = true
		}
	}

	return nil
}

func validateNetworkSubnets(subnets []meridio2v1alpha1.NetworkSubnet) error {
	if len(subnets) == 0 {
		return &validationError{message: "GatewayConfiguration must have at least one networkSubnet"}
	}

	allNets := make([]*net.IPNet, 0, 2) // typically one IPv4 + one IPv6
	for _, subnet := range subnets {
		if subnet.AttachmentType != attachmentTypeNAD {
			return &validationError{
				message: fmt.Sprintf("subnet %s: only NAD attachment type is supported (got %q)",
					strings.Join(subnet.CIDRs, ", "), subnet.AttachmentType),
			}
		}

		for _, cidr := range subnet.CIDRs {
			if err := validateCIDR(cidr); err != nil {
				return err
			}
			_, ipnet, _ := net.ParseCIDR(cidr) // safe: validateCIDR passed
			for _, existing := range allNets {
				if existing.Contains(ipnet.IP) || ipnet.Contains(existing.IP) {
					return &validationError{
						message: fmt.Sprintf("overlapping networkSubnet CIDRs: %s and %s", existing, ipnet),
					}
				}
			}
			allNets = append(allNets, ipnet)
		}
	}
	return nil
}

func validateCIDR(cidr string) error {
	_, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return &validationError{message: fmt.Sprintf("invalid CIDR %q", cidr)}
	}

	ones, bits := ipnet.Mask.Size()
	if ones == 0 && bits > 0 {
		return &validationError{
			message: fmt.Sprintf("CIDR %q: default route CIDRs (0.0.0.0/0 or ::/0) are not allowed", cidr),
		}
	}

	if ipnet.IP.To4() == nil && ipnet.IP.IsLinkLocalUnicast() {
		return &validationError{
			message: fmt.Sprintf("CIDR %q: IPv6 link-local addresses (fe80::/10) are not allowed", cidr),
		}
	}

	return nil
}

func validateNetworkAttachments(attachments []meridio2v1alpha1.NetworkAttachment) error {
	interfaceNames := make(map[string]bool)

	for i, attachment := range attachments {
		if attachment.Type != attachmentTypeNAD {
			return &validationError{
				message: fmt.Sprintf("networkAttachment %d: only NAD type is supported (got %q)", i, attachment.Type),
			}
		}
		if attachment.NAD == nil {
			return &validationError{
				message: fmt.Sprintf("networkAttachment %d: NAD configuration is required when type is NAD", i),
			}
		}
		if attachment.NAD.Name == "" {
			return &validationError{message: "networkAttachment: NAD name is required"}
		}
		if attachment.NAD.Interface == "" {
			return &validationError{message: "networkAttachment: interface name is required"}
		}
		if interfaceNames[attachment.NAD.Interface] {
			return &validationError{
				message: fmt.Sprintf("networkAttachment: duplicate interface name %q", attachment.NAD.Interface),
			}
		}
		interfaceNames[attachment.NAD.Interface] = true
	}
	return nil
}

func validateVerticalScaling(vs *meridio2v1alpha1.VerticalScaling) error {
	if vs == nil {
		return nil
	}

	for _, containerConfig := range vs.Containers {
		if len(containerConfig.Resources.Claims) > 0 {
			return &validationError{
				message: fmt.Sprintf("verticalScaling container %q: ResourceClaims are not supported (DRA not implemented)",
					containerConfig.Name),
			}
		}
	}
	return nil
}
