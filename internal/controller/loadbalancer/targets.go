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
	"errors"
	"strconv"

	discoveryv1 "k8s.io/api/discovery/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	meridio2v1alpha1 "github.com/nordix/meridio-2/api/v1alpha1"
)

// reconcileTargets synchronizes NFQLB targets from EndpointSlices.
func (c *Controller) reconcileTargets(ctx context.Context, distGroup *meridio2v1alpha1.DistributionGroup) error {
	logr := log.FromContext(ctx)

	c.mu.Lock()
	defer c.mu.Unlock()

	service, exists := c.instances[distGroup.Name]
	if !exists {
		return nil
	}

	// Get EndpointSlices for this DistributionGroup
	endpointSliceList := &discoveryv1.EndpointSliceList{}
	if err := c.List(ctx, endpointSliceList,
		client.InNamespace(c.GatewayNamespace),
		client.MatchingLabels{
			"meridio-2.nordix.org/distribution-group": distGroup.Name,
		}); err != nil {
		return err
	}

	// Get current targets
	currentTargets := c.targets[distGroup.Name]
	if currentTargets == nil {
		currentTargets = make(map[int][]string)
		c.targets[distGroup.Name] = currentTargets
	}

	// Build new targets map from EndpointSlices
	newTargets := make(map[int][]string)
	for _, eps := range endpointSliceList.Items {
		for _, endpoint := range eps.Endpoints {
			if endpoint.Conditions.Ready == nil || !*endpoint.Conditions.Ready {
				continue
			}
			if endpoint.Zone == nil {
				logr.V(1).Info("Endpoint missing identifier (Zone field)", "addresses", endpoint.Addresses)
				continue
			}

			// Parse Zone field - expected format: "maglev:N"
			zoneStr := *endpoint.Zone
			if len(zoneStr) < 8 || zoneStr[:7] != "maglev:" {
				logr.Error(nil, "Invalid Zone format, expected 'maglev:N'", "zone", zoneStr)
				continue
			}

			identifier, err := strconv.Atoi(zoneStr[7:])
			if err != nil {
				logr.Error(err, "Invalid identifier in Zone field", "zone", *endpoint.Zone)
				continue
			}
			newTargets[identifier] = endpoint.Addresses
		}
	}

	// Deactivate removed targets (nfqlb handles policy route cleanup internally)
	var errFinal error
	for identifier, ips := range currentTargets {
		if _, exists := newTargets[identifier]; !exists {
			if err := service.DeleteTarget(ctx, ips, identifier); err != nil {
				logr.Error(err, "Failed to deactivate target", "identifier", identifier)
				errFinal = errors.Join(errFinal, err)
			} else {
				logr.Info("Deactivated target", "distGroup", distGroup.Name, "identifier", identifier)
			}
		}
	}

	// Activate new/updated targets (nfqlb handles policy route creation internally)
	for identifier, ips := range newTargets {
		if err := service.AddTarget(ctx, ips, identifier); err != nil {
			logr.Error(err, "Failed to activate target", "identifier", identifier, "ips", ips)
			errFinal = errors.Join(errFinal, err)
		} else {
			logr.Info("Activated target", "distGroup", distGroup.Name, "identifier", identifier, "ips", ips)
		}
	}

	// Update tracked targets
	c.targets[distGroup.Name] = newTargets

	// Manage readiness file based on endpoint count
	if len(newTargets) > 0 {
		if err := c.createReadinessFile(distGroup.Name); err != nil {
			logr.Error(err, "Failed to create readiness file", "distGroup", distGroup.Name)
		}
	} else {
		if err := c.removeReadinessFile(distGroup.Name); err != nil {
			logr.Error(err, "Failed to remove readiness file", "distGroup", distGroup.Name)
		}
	}

	logr.Info("Reconciled targets", "distGroup", distGroup.Name, "count", len(newTargets))
	return errFinal
}
