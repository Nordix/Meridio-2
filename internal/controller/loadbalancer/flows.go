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

package loadbalancer

import (
	"context"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	meridio2v1alpha1 "github.com/nordix/meridio-2/api/v1alpha1"
)

// reconcileFlows configures NFQLB flows from L34Routes.
func (c *Controller) reconcileFlows(ctx context.Context, distGroup *meridio2v1alpha1.DistributionGroup) error {
	logr := log.FromContext(ctx)

	c.mu.Lock()
	defer c.mu.Unlock()

	instance, exists := c.instances[distGroup.Name]
	if !exists {
		return nil
	}

	// Initialize flows map if needed
	if c.flows == nil {
		c.flows = make(map[string]map[string]*meridio2v1alpha1.L34Route)
	}
	if c.flows[distGroup.Name] == nil {
		c.flows[distGroup.Name] = make(map[string]*meridio2v1alpha1.L34Route)
	}

	// Check if DistributionGroup has endpoints before configuring flows
	hasEndpoints, err := c.hasReadyEndpoints(ctx, distGroup)
	if err != nil {
		return err
	}

	if !hasEndpoints {
		logr.Info("No ready endpoints, deleting flows", "distGroup", distGroup.Name)
		return c.deleteAllFlows(ctx, instance, distGroup.Name)
	}

	// Get L34Routes for this Gateway and DistributionGroup
	newFlows, err := c.listMatchingL34Routes(ctx, distGroup)
	if err != nil {
		return err
	}

	// BUG FIX #2: Handle empty L34Route list
	if len(newFlows) == 0 {
		logr.Info("No L34Routes found, deleting all flows", "distGroup", distGroup.Name)
		if err := c.deleteAllFlows(ctx, instance, distGroup.Name); err != nil {
			logr.Error(err, "Failed to delete all flows")
		}
		// Clear nftables VIPs
		if err := c.configureNftables(ctx, distGroup.Name, []string{}); err != nil {
			logr.Error(err, "Failed to clear nftables VIPs")
		}
		return nil
	}

	// Delete removed flows first
	currentFlows := c.flows[distGroup.Name]
	var errFinal error
	for flowName := range currentFlows {
		if _, exists := newFlows[flowName]; !exists {
			if err := instance.DeleteFlow(ctx, &nameOnlyFlow{name: flowName}); err != nil {
				logr.Error(err, "Failed to delete flow", "flow", flowName)
				errFinal = fmt.Errorf("%w; failed to delete flow %s: %w", errFinal, flowName, err)
			} else {
				logr.Info("Deleted flow", "distGroup", distGroup.Name, "flow", flowName)
			}
		}
	}

	// IMPROVEMENT #5: Add/update flows BEFORE configuring nftables
	successfulFlows := make(map[string]*meridio2v1alpha1.L34Route)
	for flowName, route := range newFlows {
		flow := newL34RouteFlow(flowName, route)
		if err := instance.AddFlow(ctx, flow); err != nil {
			logr.Error(err, "Failed to set flow", "flow", flowName)
			errFinal = fmt.Errorf("%w; failed to set flow %s: %w", errFinal, flowName, err)
		} else {
			logr.Info("Configured flow", "distGroup", distGroup.Name, "flow", flowName)
			successfulFlows[flowName] = route
		}
	}

	// Configure nftables with VIPs from Gateway status
	vips, err := c.getGatewayVIPs(ctx)
	if err != nil {
		logr.Error(err, "Failed to get Gateway VIPs", "distGroup", distGroup.Name)
		return fmt.Errorf("failed to get Gateway VIPs: %w", err)
	}
	if err := c.configureNftables(ctx, distGroup.Name, vips); err != nil {
		logr.Error(err, "Failed to configure nftables", "distGroup", distGroup.Name)
		return fmt.Errorf("failed to configure nftables: %w", err)
	}

	// BUG FIX #3: Update tracked flows with only successful ones
	c.flows[distGroup.Name] = successfulFlows

	logr.Info("Reconciled flows", "distGroup", distGroup.Name, "count", len(successfulFlows))
	return errFinal
}

// hasReadyEndpoints checks if DistributionGroup has any ready endpoints.
func (c *Controller) hasReadyEndpoints(ctx context.Context, distGroup *meridio2v1alpha1.DistributionGroup) (bool, error) {
	sliceList := &meridio2v1alpha1.LoadBalancerEndpointSliceList{}
	if err := c.List(ctx, sliceList,
		client.InNamespace(c.GatewayNamespace),
		client.MatchingFields{
			"spec.distributionGroupName": distGroup.Name,
			"spec.gatewayRef.name":       c.GatewayName,
		},
	); err != nil {
		return false, err
	}

	for i := range sliceList.Items {
		if !metav1.IsControlledBy(&sliceList.Items[i], distGroup) {
			continue
		}
		for _, endpoint := range sliceList.Items[i].Spec.Endpoints {
			if endpoint.Ready {
				return true, nil
			}
		}
	}
	return false, nil
}

// deleteAllFlows deletes all flows for a DistributionGroup.
func (c *Controller) deleteAllFlows(ctx context.Context, instance nfqlbInstance, distGroupName string) error {
	logr := log.FromContext(ctx)
	currentFlows := c.flows[distGroupName]
	var errFinal error
	for flowName := range currentFlows {
		if err := instance.DeleteFlow(ctx, &nameOnlyFlow{name: flowName}); err != nil {
			logr.Error(err, "Failed to delete flow", "flow", flowName)
			errFinal = fmt.Errorf("%w; failed to delete flow %s: %w", errFinal, flowName, err)
		} else {
			logr.Info("Deleted flow", "distGroup", distGroupName, "flow", flowName)
		}
	}
	c.flows[distGroupName] = make(map[string]*meridio2v1alpha1.L34Route)
	return errFinal
}

// listMatchingL34Routes finds L34Routes that reference this Gateway and DistributionGroup.
func (c *Controller) listMatchingL34Routes(ctx context.Context, distGroup *meridio2v1alpha1.DistributionGroup) (map[string]*meridio2v1alpha1.L34Route, error) {
	logr := log.FromContext(ctx)

	l34routeList := &meridio2v1alpha1.L34RouteList{}
	if err := c.List(ctx, l34routeList, client.InNamespace(c.GatewayNamespace)); err != nil {
		return nil, err
	}

	newFlows := make(map[string]*meridio2v1alpha1.L34Route)
	for i := range l34routeList.Items {
		route := &l34routeList.Items[i]

		// Check if route references this Gateway
		if !c.referencesGateway(route) {
			continue
		}

		// Check if route references this DistributionGroup
		dgName, ok := c.referencesDistributionGroup(route, distGroup.Name)
		if !ok {
			continue
		}

		// Validate that referenced DistributionGroup exists
		var dg meridio2v1alpha1.DistributionGroup
		if err := c.Get(ctx, client.ObjectKey{Name: dgName, Namespace: c.GatewayNamespace}, &dg); err != nil {
			if apierrors.IsNotFound(err) {
				logr.Info("Skipping L34Route: DistributionGroup not found", "route", route.Name, "distributionGroup", dgName)
				continue
			}
			return nil, err
		}

		newFlows[route.Name] = route
	}

	return newFlows, nil
}

// referencesGateway checks if L34Route references this Gateway.
func (c *Controller) referencesGateway(route *meridio2v1alpha1.L34Route) bool {
	for _, parentRef := range route.Spec.ParentRefs {
		// Default Group to "gateway.networking.k8s.io" when unspecified
		group := groupGatewayAPI
		if parentRef.Group != nil {
			group = string(*parentRef.Group)
		}

		// Default Kind to "Gateway" when unspecified
		kind := kindGateway
		if parentRef.Kind != nil {
			kind = string(*parentRef.Kind)
		}

		// Default Namespace to Route's namespace when unspecified
		namespace := route.Namespace
		if parentRef.Namespace != nil {
			namespace = string(*parentRef.Namespace)
		}

		// Check if this parentRef matches our Gateway
		if group == gatewayv1.GroupVersion.Group &&
			kind == kindGateway &&
			string(parentRef.Name) == c.GatewayName &&
			namespace == c.GatewayNamespace {
			return true
		}
	}
	return false
}

// referencesDistributionGroup checks if L34Route references the given DistributionGroup.
// Returns the DistributionGroup name and true if found.
func (c *Controller) referencesDistributionGroup(route *meridio2v1alpha1.L34Route, distGroupName string) (string, bool) {
	for _, backendRef := range route.Spec.BackendRefs {
		if backendRef.Group != nil && string(*backendRef.Group) == meridio2v1alpha1.GroupVersion.Group &&
			backendRef.Kind != nil && string(*backendRef.Kind) == "DistributionGroup" &&
			string(backendRef.Name) == distGroupName {
			return string(backendRef.Name), true
		}
	}
	return "", false
}

// getGatewayVIPs extracts VIP addresses from Gateway status
func (c *Controller) getGatewayVIPs(ctx context.Context) ([]string, error) {
	gateway := &gatewayv1.Gateway{}
	if err := c.Get(ctx, client.ObjectKey{
		Name:      c.GatewayName,
		Namespace: c.GatewayNamespace,
	}, gateway); err != nil {
		return nil, fmt.Errorf("failed to get Gateway: %w", err)
	}

	vips := []string{}
	for _, addr := range gateway.Status.Addresses {
		if addr.Type != nil && *addr.Type == gatewayv1.IPAddressType {
			// Convert IP to CIDR format for nftables
			cidr := addr.Value
			if !strings.Contains(cidr, "/") {
				// Detect IPv4 vs IPv6 and add appropriate prefix
				if strings.Contains(cidr, ":") {
					cidr += "/128" // IPv6
				} else {
					cidr += "/32" // IPv4
				}
			}
			vips = append(vips, cidr)
		}
	}

	return vips, nil
}

// configureNftables configures nftables rules for VIPs.
// Only updates nftables if VIPs have changed to avoid unnecessary flushes.
func (c *Controller) configureNftables(ctx context.Context, distGroupName string, vips []string) error {
	logr := log.FromContext(ctx)

	if c.nftManager == nil {
		return fmt.Errorf("shared nftables manager not initialized")
	}

	// Check if VIPs have changed
	if vipsEqual(c.currentVIPs, vips) {
		return nil
	}

	if err := c.nftManager.SetVIPs(vips); err != nil {
		return fmt.Errorf("failed to set VIPs in nftables: %w", err)
	}

	c.currentVIPs = append([]string{}, vips...) // Store copy
	logr.Info("Configured nftables VIP rules", "distGroup", distGroupName, "vipCount", len(vips))
	return nil
}

// vipsEqual compares two VIP slices for equality
func vipsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	// Create maps for comparison (order-independent)
	aMap := make(map[string]bool, len(a))
	for _, v := range a {
		aMap[v] = true
	}
	for _, v := range b {
		if !aMap[v] {
			return false
		}
	}
	return true
}
