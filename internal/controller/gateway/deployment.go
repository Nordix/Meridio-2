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
	"encoding/json"
	"fmt"
	"maps"
	"strings"

	netdefv1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	meridio2v1alpha1 "github.com/nordix/meridio-2/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// permanentDeploymentError indicates a permanent failure that should be exposed in Gateway status
type permanentDeploymentError struct {
	message string
}

func (e *permanentDeploymentError) Error() string {
	return e.message
}

// reconcileLBDeployment creates or updates the LB Deployment for the Gateway
func (r *GatewayReconciler) reconcileLBDeployment(ctx context.Context, gw *gatewayv1.Gateway, gwConfig *meridio2v1alpha1.GatewayConfiguration, template *appsv1.Deployment) error {
	// Generate deployment name
	deploymentName := lbDeploymentName(gw)

	// Check if Deployment exists (name-based lookup with ownership verification)
	var existing appsv1.Deployment
	err := r.Get(ctx, client.ObjectKey{Namespace: gw.Namespace, Name: deploymentName}, &existing)

	var found bool
	if err == nil {
		// Deployment with desired name exists - verify ownership
		if !metav1.IsControlledBy(&existing, gw) {
			owner := getControllerOwner(&existing)
			return &permanentDeploymentError{
				message: fmt.Sprintf("deployment %s exists but is not owned by gateway %s (owned by %s, cannot proceed due to name collision)", existing.Name, gw.Name, owner),
			}
		}
		found = true
	} else if !apierrors.IsNotFound(err) {
		// API server error - return to trigger retry
		return fmt.Errorf("failed to get LB deployment: %w", err)
	}

	// Build desired state
	var base *appsv1.Deployment
	if found {
		base = &existing
	}

	desired := reconcileDeploymentSpec(base, template, gw, gwConfig, deploymentName, r.LBServiceAccount)
	if !found {
		if err := ctrl.SetControllerReference(gw, desired, r.Scheme); err != nil {
			return fmt.Errorf("failed to set owner reference: %w", err)
		}
	}

	// Create or update
	if !found {
		if err := r.Create(ctx, desired); err != nil {
			// AlreadyExists means another reconciliation created it - not a permanent error
			if apierrors.IsAlreadyExists(err) {
				return fmt.Errorf("failed to create LB deployment: %w", err)
			}
			return &permanentDeploymentError{
				message: fmt.Sprintf("failed to create LB deployment: %v", err),
			}
		}
	} else if deploymentNeedsUpdate(&existing, desired) {
		if err := r.Update(ctx, desired); err != nil {
			return fmt.Errorf("failed to update LB deployment: %w", err)
		}
	}

	return nil
}

// getControllerOwner returns the controller owner of a Deployment as "Kind/Name"
func getControllerOwner(deployment *appsv1.Deployment) string {
	for _, ref := range deployment.OwnerReferences {
		if ref.Controller != nil && *ref.Controller {
			return fmt.Sprintf("%s/%s", ref.Kind, ref.Name)
		}
	}
	return "unknown"
}

// reconcileDeploymentSpec builds desired deployment state (works for both create and update)
// If base is nil, template is used as starting point (create scenario)
// If base is provided, it's used as starting point and template fields are merged (update scenario)
func reconcileDeploymentSpec(base, template *appsv1.Deployment, gw *gatewayv1.Gateway, gwConfig *meridio2v1alpha1.GatewayConfiguration, deploymentName, serviceAccount string) *appsv1.Deployment {
	var desired *appsv1.Deployment

	if base == nil {
		// Create scenario: start from template
		desired = template.DeepCopy()
	} else {
		// Update scenario: start from existing, merge template fields
		desired = base.DeepCopy()

		desired.Spec.Template.Spec.InitContainers = template.Spec.Template.Spec.InitContainers
		desired.Spec.Template.Spec.Containers = template.Spec.Template.Spec.Containers
		desired.Spec.Template.Spec.Volumes = template.Spec.Template.Spec.Volumes

		// Preserve existing container resources after template overwrite.
		// Resources may be managed by external tools (VPA, manual edits) or by
		// applyVerticalScaling (enforceResources=true). Either way, the existing
		// values are the correct baseline; applyVerticalScaling will overwrite
		// only the containers it explicitly enforces.
		restoreContainerResources(desired.Spec.Template.Spec.Containers, base.Spec.Template.Spec.Containers)

		// Merge labels/annotations (preserves external additions, overwrites controller-managed)
		desired.Labels = mergeMaps(desired.Labels, template.Labels)
		desired.Annotations = mergeMaps(desired.Annotations, template.Annotations)
		desired.Spec.Template.Labels = mergeMaps(desired.Spec.Template.Labels, template.Spec.Template.Labels)
		desired.Spec.Template.Annotations = mergeMaps(desired.Spec.Template.Annotations, template.Spec.Template.Annotations)
	}

	// Apply Gateway infrastructure metadata (overwrites template/existing)
	mergeInfrastructureMetadata(&desired.ObjectMeta, gw)
	mergeInfrastructureMetadata(&desired.Spec.Template.ObjectMeta, gw)

	// Set deployment-specific values (always enforced)
	desired.Name = deploymentName
	desired.Namespace = gw.Namespace
	desired.Spec.Template.Spec.ServiceAccountName = serviceAccount

	// Set controller-managed labels
	setControllerLabels(&desired.ObjectMeta, deploymentName, gw.Name)
	setControllerLabels(&desired.Spec.Template.ObjectMeta, deploymentName, gw.Name)

	// Set selector (immutable, but safe to set on every reconcile)
	desired.Spec.Selector = &metav1.LabelSelector{
		MatchLabels: map[string]string{"app": deploymentName},
	}

	// Update pod anti-affinity to match deployment name
	updateAntiAffinityLabels(desired, deploymentName)

	// Inject Gateway name into container environment variables
	injectGatewayEnvVars(desired, gw.Name)

	// Apply GatewayConfiguration (replicas, resources, etc.) - after labels/annotations
	if gwConfig != nil {
		// Extract template NAD annotation (from loaded YAML file)
		templateNADAnnotation := ""
		if template.Spec.Template.Annotations != nil {
			templateNADAnnotation = template.Spec.Template.Annotations[netdefv1.NetworkAttachmentAnnot]
		}
		applyGatewayConfiguration(desired, gwConfig, templateNADAnnotation, base == nil)
	}

	return desired
}

// deploymentNeedsUpdate checks if Deployment needs update using semantic equality
func deploymentNeedsUpdate(existing, desired *appsv1.Deployment) bool {
	return !apiequality.Semantic.DeepEqual(existing.Spec, desired.Spec) ||
		!maps.Equal(existing.Labels, desired.Labels) ||
		!maps.Equal(existing.Annotations, desired.Annotations)
}

// mergeMaps merges two maps, with values from 'overwrite' taking precedence
func mergeMaps(base, overwrite map[string]string) map[string]string {
	result := make(map[string]string, len(base)+len(overwrite))
	maps.Copy(result, base)
	maps.Copy(result, overwrite)
	return result
}

// lbDeploymentName generates the LB Deployment name for a Gateway
func lbDeploymentName(gw *gatewayv1.Gateway) string {
	return lbDeploymentPrefix + gw.Name
}

// setControllerLabels sets labels managed by the controller
func setControllerLabels(meta *metav1.ObjectMeta, deploymentName, gatewayName string) {
	if meta.Labels == nil {
		meta.Labels = make(map[string]string)
	}
	meta.Labels["app"] = deploymentName
	meta.Labels[labelGatewayName] = gatewayName
	meta.Labels[labelManagedBy] = managedByValue
}

// mergeInfrastructureMetadata merges Gateway.spec.infrastructure labels/annotations
func mergeInfrastructureMetadata(meta *metav1.ObjectMeta, gw *gatewayv1.Gateway) {
	if gw.Spec.Infrastructure == nil {
		return
	}

	if meta.Labels == nil && len(gw.Spec.Infrastructure.Labels) > 0 {
		meta.Labels = make(map[string]string)
	}
	if meta.Annotations == nil && len(gw.Spec.Infrastructure.Annotations) > 0 {
		meta.Annotations = make(map[string]string)
	}

	for k, v := range gw.Spec.Infrastructure.Labels {
		meta.Labels[string(k)] = string(v)
	}
	for k, v := range gw.Spec.Infrastructure.Annotations {
		meta.Annotations[string(k)] = string(v)
	}
}

// updateAntiAffinityLabels updates pod anti-affinity to match deployment-specific labels
func updateAntiAffinityLabels(deployment *appsv1.Deployment, deploymentName string) {
	if deployment.Spec.Template.Spec.Affinity == nil ||
		deployment.Spec.Template.Spec.Affinity.PodAntiAffinity == nil {
		return
	}

	for i := range deployment.Spec.Template.Spec.Affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution {
		term := &deployment.Spec.Template.Spec.Affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution[i]
		if term.LabelSelector != nil {
			for j := range term.LabelSelector.MatchExpressions {
				if term.LabelSelector.MatchExpressions[j].Key == "app" {
					term.LabelSelector.MatchExpressions[j].Values = []string{deploymentName}
				}
			}
		}
	}
}

// injectGatewayEnvVars injects MERIDIO_GATEWAY_NAME into container environment variables
// TODO: Consider checking for specific container names (loadbalancer, router) and ensuring
// MERIDIO_GATEWAY_NAME env var exists before attempting to set it. Current implementation
// silently skips containers without the env var, which could hide template issues.
func injectGatewayEnvVars(deployment *appsv1.Deployment, gatewayName string) {
	for i := range deployment.Spec.Template.Spec.Containers {
		container := &deployment.Spec.Template.Spec.Containers[i]
		for j := range container.Env {
			if container.Env[j].Name == "MERIDIO_GATEWAY_NAME" {
				container.Env[j].Value = gatewayName
				break
			}
		}
	}
}

// applyGatewayConfiguration applies GatewayConfiguration values to the Deployment
func applyGatewayConfiguration(deployment *appsv1.Deployment, gwConfig *meridio2v1alpha1.GatewayConfiguration, templateNADAnnotation string, isInitialCreation bool) {
	// Horizontal scaling
	if isInitialCreation || gwConfig.Spec.HorizontalScaling.EnforceReplicas {
		replicas := int32(gwConfig.Spec.HorizontalScaling.Replicas)
		deployment.Spec.Replicas = &replicas
	}

	// Vertical scaling
	if gwConfig.Spec.VerticalScaling != nil {
		applyVerticalScaling(deployment, gwConfig.Spec.VerticalScaling, isInitialCreation)
	}

	// Network attachments
	applyNetworkAttachments(deployment, templateNADAnnotation, gwConfig.Spec.NetworkAttachments)
}

// applyVerticalScaling applies container resources from VerticalScaling config.
// Only overwrites resources for containers with enforceResources=true.
// Existing resources are already preserved by reconcileDeploymentSpec, so
// containers with enforceResources=false or unlisted containers keep their values.
func applyVerticalScaling(deployment *appsv1.Deployment, vs *meridio2v1alpha1.VerticalScaling, isInitialCreation bool) {
	for _, containerConfig := range vs.Containers {
		if !isInitialCreation && !containerConfig.EnforceResources {
			continue
		}
		for i := range deployment.Spec.Template.Spec.Containers {
			container := &deployment.Spec.Template.Spec.Containers[i]
			if container.Name == containerConfig.Name {
				container.Resources.Requests = containerConfig.Resources.Requests
				container.Resources.Limits = containerConfig.Resources.Limits
				// Claims not supported (DRA not implemented)
				container.ResizePolicy = containerConfig.ResizePolicy
				break
			}
		}
	}
}

// restoreContainerResources preserves resources and resizePolicy from existing
// containers after template overwrites them. Matches by container name.
func restoreContainerResources(containers, existing []corev1.Container) {
	for i := range containers {
		for j := range existing {
			if containers[i].Name == existing[j].Name {
				containers[i].Resources = existing[j].Resources
				containers[i].ResizePolicy = existing[j].ResizePolicy
				break
			}
		}
	}
}

// applyNetworkAttachments applies NAD annotations to the Deployment pod template
// Uses template + GatewayConfiguration as authoritative sources (not existing Deployment)
// GatewayConfiguration NADs override template NADs with same namespace/name/interface
// Supports both JSON and shorthand formats when reading, always writes JSON
// Performs semantic (order-independent) comparison to avoid unnecessary rolling updates
func applyNetworkAttachments(deployment *appsv1.Deployment, templateNADAnnotation string, attachments []meridio2v1alpha1.NetworkAttachment) {
	// Parse template NADs first to estimate capacity
	var templateNADs []*netdefv1.NetworkSelectionElement
	if templateNADAnnotation != "" {
		templateNADs = parseNetworkAnnotation(templateNADAnnotation)
	}

	// Pre-allocate desired slice (upper bound: template + GatewayConfiguration NADs)
	desired := make([]*netdefv1.NetworkSelectionElement, 0, len(templateNADs)+len(attachments))

	// 1. Parse GatewayConfiguration NADs and build map (these take precedence)
	gwConfigMap := make(map[string]*netdefv1.NetworkSelectionElement)
	for _, attachment := range attachments {
		if attachment.Type == attachmentTypeNAD && attachment.NAD != nil {
			elem := &netdefv1.NetworkSelectionElement{
				Name:             attachment.NAD.Name,
				Namespace:        attachment.NAD.Namespace,
				InterfaceRequest: attachment.NAD.Interface,
			}
			key := elem.Namespace + "/" + elem.Name + ":" + elem.InterfaceRequest
			gwConfigMap[key] = elem
		}
	}

	// 2. Add template NADs that are NOT overridden by GatewayConfiguration
	for _, templateNAD := range templateNADs {
		ns := templateNAD.Namespace
		if ns == "" {
			ns = deployment.Namespace
		}
		key := ns + "/" + templateNAD.Name + ":" + templateNAD.InterfaceRequest
		if _, exists := gwConfigMap[key]; !exists {
			desired = append(desired, templateNAD)
		}
	}

	// 3. Add all GatewayConfiguration NADs
	for _, elem := range gwConfigMap {
		desired = append(desired, elem)
	}

	// 4. Compare semantically with current Deployment annotation
	var currentAnnotation string
	if deployment.Spec.Template.Annotations != nil {
		currentAnnotation = deployment.Spec.Template.Annotations[netdefv1.NetworkAttachmentAnnot]
	}
	current := parseNetworkAnnotation(currentAnnotation)
	if networkAttachmentsEqual(current, desired, deployment.Namespace) {
		return // No change needed
	}

	// 5. Update annotation (only mutate if there's a real change)
	if len(desired) > 0 {
		// Need to set annotation - initialize map if needed
		if deployment.Spec.Template.Annotations == nil {
			deployment.Spec.Template.Annotations = make(map[string]string)
		}
		if encoded, err := json.Marshal(desired); err == nil {
			deployment.Spec.Template.Annotations[netdefv1.NetworkAttachmentAnnot] = string(encoded)
		}
	} else {
		// Need to delete annotation - only if map exists
		if deployment.Spec.Template.Annotations != nil {
			delete(deployment.Spec.Template.Annotations, netdefv1.NetworkAttachmentAnnot)
		}
	}
}

// networkAttachmentsEqual checks if two NAD lists are semantically equal (order-independent)
// Only compares namespace/name/interface (ignores other fields like ips, mac, bandwidth)
// Note: assumes no duplicates within each list (guaranteed by validation layer).
func networkAttachmentsEqual(a, b []*netdefv1.NetworkSelectionElement, defaultNamespace string) bool {
	if len(a) != len(b) {
		return false
	}

	// Build set from 'a'
	setA := make(map[string]bool, len(a))
	for _, e := range a {
		ns := e.Namespace
		if ns == "" {
			ns = defaultNamespace
		}
		key := ns + "/" + e.Name + ":" + e.InterfaceRequest
		setA[key] = true
	}

	// Check if all elements in 'b' exist in 'a'
	for _, e := range b {
		ns := e.Namespace
		if ns == "" {
			ns = defaultNamespace
		}
		key := ns + "/" + e.Name + ":" + e.InterfaceRequest
		if !setA[key] {
			return false
		}
	}

	return true
}

// parseNetworkAnnotation parses k8s.v1.cni.cncf.io/networks annotation
// Supports both JSON and shorthand formats (ns/name@interface or name@interface)
// Does NOT modify the parsed elements (preserves original format)
func parseNetworkAnnotation(annotation string) []*netdefv1.NetworkSelectionElement {
	log := ctrl.Log.WithName("parseNetworkAnnotation")

	if annotation == "" {
		return nil
	}

	// Try JSON first
	elements := make([]*netdefv1.NetworkSelectionElement, 0, 2)
	if json.Unmarshal([]byte(annotation), &elements) == nil {
		return elements
	}

	// Parse shorthand format: ns/name@interface or name@interface
	for item := range strings.SplitSeq(annotation, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}

		elem := &netdefv1.NetworkSelectionElement{}

		// Split by @ for interface: expect at most one @
		parts := strings.SplitN(item, "@", 3)
		if len(parts) == 3 || (len(parts) == 2 && (parts[0] == "" || parts[1] == "")) {
			log.V(1).Info("skipping malformed network annotation item", "item", item)
			continue
		}
		namespacedName := parts[0]
		if len(parts) == 2 {
			elem.InterfaceRequest = parts[1]
		}

		// Split by / for namespace: expect at most one /
		nsParts := strings.SplitN(namespacedName, "/", 3)
		if len(nsParts) == 3 || (len(nsParts) == 2 && (nsParts[0] == "" || nsParts[1] == "")) {
			log.V(1).Info("skipping malformed network annotation item", "item", item)
			continue
		}
		if len(nsParts) == 2 {
			elem.Namespace = nsParts[0]
			elem.Name = nsParts[1]
		} else {
			elem.Name = nsParts[0]
			// Leave namespace empty - will be filled during duplicate check
		}

		elements = append(elements, elem)
	}

	return elements
}
