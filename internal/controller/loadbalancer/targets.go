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
	"cmp"
	"context"
	"errors"
	"maps"
	"slices"
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

	// Sort EndpointSlices by name for deterministic processing order (cache List
	// does not guarantee order). Produces stable accumulated IP lists across reconciles,
	// preventing flapping during transients when the same identifier appears in multiple
	// slices of the same IP family (e.g., Pod replacement with >100 endpoints).
	slices.SortFunc(endpointSliceList.Items, func(a, b discoveryv1.EndpointSlice) int {
		return cmp.Compare(a.Name, b.Name)
	})

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
			newTargets[identifier] = append(newTargets[identifier], endpoint.Addresses...)
		}
	}

	// Deactivate removed targets (nfqlb handles policy route cleanup internally)
	var errFinal error
	failedDeletes := make(map[int][]string)
	for identifier, ips := range currentTargets {
		if _, exists := newTargets[identifier]; !exists {
			if err := service.DeleteTarget(ctx, ips, identifier); err != nil {
				logr.Error(err, "Failed to deactivate target", "identifier", identifier)
				errFinal = errors.Join(errFinal, err)
				failedDeletes[identifier] = ips
			} else {
				logr.Info("Deactivated target", "distGroup", distGroup.Name, "identifier", identifier)
			}
		}
	}

	// Clean up broken targets that are no longer desired.
	for id := range service.BrokenTargets() {
		if _, wanted := newTargets[id]; !wanted {
			if _, alreadyHandled := failedDeletes[id]; alreadyHandled {
				continue
			}
			logr.Info("Cleaning up broken target", "distGroup", distGroup.Name, "identifier", id)
			if ips, inState := service.Targets()[id]; inState {
				if err := service.DeleteTarget(ctx, ips, id); err != nil {
					errFinal = errors.Join(errFinal, err)
					failedDeletes[id] = ips
				}
			}
		}
	}

	// Activate all desired targets unconditionally — nfqlb layer handles idempotency
	// and drift recovery internally.
	for identifier, ips := range newTargets {
		// Sort IPs for deterministic comparison in AddTarget (avoids false
		// "IPs changed" triggers when IPv4/IPv6 slices are processed in different order)
		slices.Sort(ips)
		if err := service.AddTarget(ctx, ips, identifier); err != nil {
			logr.Error(err, "Failed to activate target", "identifier", identifier, "ips", ips)
			errFinal = errors.Join(errFinal, err)
		} else {
			logr.Info("Activated target", "distGroup", distGroup.Name, "identifier", identifier, "ips", ips)
		}
	}

	// Manage readiness file based on desired endpoint count (before error check,
	// so VIP advertisement reflects actual target availability regardless of
	// partial deletion failures)
	if len(newTargets) > 0 {
		if err := c.Readiness.Set(distGroup.Name); err != nil {
			logr.Error(err, "Failed to create readiness file", "distGroup", distGroup.Name)
		}
	} else {
		if err := c.Readiness.Remove(distGroup.Name); err != nil {
			logr.Error(err, "Failed to remove readiness file", "distGroup", distGroup.Name)
		}
	}

	if errFinal != nil {
		// Commit c.targets = newTargets ∪ failed-delete IDs.
		// This ensures next reconcile retries DeleteTarget for failed deletions
		// and retries AddTarget for failed additions (idempotent).
		committed := make(map[int][]string, len(newTargets)+len(failedDeletes))
		maps.Copy(committed, newTargets)
		maps.Copy(committed, failedDeletes)
		c.targets[distGroup.Name] = committed
		return errFinal
	}

	// Update tracked targets on full success
	c.targets[distGroup.Name] = newTargets

	logr.Info("Reconciled targets", "distGroup", distGroup.Name, "count", len(newTargets))
	return nil
}
