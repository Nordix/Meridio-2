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
	"slices"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	meridio2v1alpha1 "github.com/nordix/meridio-2/api/v1alpha1"
)

// reconcileTargets synchronizes NFQLB targets from LoadBalancerEndpointSlices.
func (c *Controller) reconcileTargets(ctx context.Context, distGroup *meridio2v1alpha1.DistributionGroup) error {
	logr := log.FromContext(ctx)

	c.mu.Lock()
	defer c.mu.Unlock()

	service, exists := c.instances[distGroup.Name]
	if !exists {
		return nil
	}

	// Get LoadBalancerEndpointSlices for this DistributionGroup
	sliceList := &meridio2v1alpha1.LoadBalancerEndpointSliceList{}
	if err := c.List(ctx, sliceList,
		client.InNamespace(distGroup.Namespace),
		client.MatchingFields{
			"spec.distributionGroupName": distGroup.Name,
			"spec.gatewayRef.name":       c.GatewayName,
		},
	); err != nil {
		return err
	}

	// Filter to slices owned by this DistributionGroup (defense against
	// manually-created slices with matching distributionGroupName but no ownerRef).
	var ownedSlices []meridio2v1alpha1.LoadBalancerEndpointSlice
	for i := range sliceList.Items {
		if metav1.IsControlledBy(&sliceList.Items[i], distGroup) {
			ownedSlices = append(ownedSlices, sliceList.Items[i])
		} else {
			logr.Info("Ignoring LoadBalancerEndpointSlice not owned by DistributionGroup",
				"slice", sliceList.Items[i].Name, "distGroup", distGroup.Name)
		}
	}

	// Sort slices by name for deterministic processing order (cache List
	// does not guarantee order). Produces stable accumulated IP lists across reconciles,
	// preventing flapping during transients when the same identifier appears in
	// multiple slices (e.g., Pod replacement with >MaxEndpointsPerSlice endpoints).
	slices.SortFunc(ownedSlices, func(a, b meridio2v1alpha1.LoadBalancerEndpointSlice) int {
		return cmp.Compare(a.Name, b.Name)
	})

	// Get current targets
	currentTargets := c.targets[distGroup.Name]
	if currentTargets == nil {
		currentTargets = make(map[int]struct{})
		c.targets[distGroup.Name] = currentTargets
	}

	// Build new targets map from LoadBalancerEndpointSlices.
	// First occurrence of an identifier wins — during transients the same identifier
	// may briefly appear in multiple slices.
	newTargets := make(map[int][]string)
	for _, lbeps := range ownedSlices {
		for _, endpoint := range lbeps.Spec.Endpoints {
			if !endpoint.Ready {
				continue
			}
			if endpoint.Identifier == nil {
				logr.V(1).Info("Endpoint missing identifier", "target", endpoint.Target.Name, "addresses", endpoint.Addresses)
				continue
			}

			identifier := int(*endpoint.Identifier)
			if existingIPs, exists := newTargets[identifier]; exists {
				logr.V(1).Info("Duplicate identifier across slices (transient expected during reconciliation)",
					"identifier", identifier, "existingIPs", existingIPs,
					"ignoredTarget", endpoint.Target.Name, "ignoredSlice", lbeps.Name)
				continue
			}
			ips := make([]string, 0, len(endpoint.Addresses))
			for _, addr := range endpoint.Addresses {
				ips = append(ips, addr.IP)
			}
			newTargets[identifier] = ips
		}
	}

	// Deactivate removed targets (nfqlb handles policy route cleanup internally)
	var errFinal error
	failedDeletes := make(map[int]struct{})
	for identifier := range currentTargets {
		if _, exists := newTargets[identifier]; !exists {
			if err := service.DeleteTarget(ctx, identifier); err != nil {
				logr.Error(err, "Failed to deactivate target", "identifier", identifier)
				errFinal = errors.Join(errFinal, err)
				failedDeletes[identifier] = struct{}{}
			} else {
				logr.Info("Deactivated target", "distGroup", distGroup.Name, "identifier", identifier)
			}
		}
	}

	// Activate all desired targets unconditionally — nfqlb layer handles idempotency
	// and drift recovery internally.
	var anyTargetReady bool
	for identifier, ips := range newTargets {
		// Sort IPs for deterministic comparison in AddTarget (avoids false
		// "IPs changed" triggers when IPv4/IPv6 slices are processed in different order)
		slices.Sort(ips)
		if err := service.AddTarget(ctx, ips, identifier); err != nil {
			logr.Error(err, "Failed to activate target", "identifier", identifier, "ips", ips)
			errFinal = errors.Join(errFinal, err)
		} else {
			anyTargetReady = true
			logr.Info("Activated target", "distGroup", distGroup.Name, "identifier", identifier, "ips", ips)
		}
	}

	// Manage readiness file: advertise VIPs only when at least one target is
	// successfully activated (avoids blackhole when all AddTarget calls fail).
	if anyTargetReady {
		if err := c.Readiness.Set(distGroup.Name); err != nil {
			logr.Error(err, "Failed to create readiness file", "distGroup", distGroup.Name)
		}
	} else {
		if err := c.Readiness.Remove(distGroup.Name); err != nil {
			logr.Error(err, "Failed to remove readiness file", "distGroup", distGroup.Name)
		}
	}

	// Commit c.targets: identifiers from newTargets ∪ failed-delete IDs.
	// This ensures next reconcile retries DeleteTarget for failed deletions
	// and retries AddTarget for failed additions (idempotent).
	committed := make(map[int]struct{}, len(newTargets)+len(failedDeletes))
	for id := range newTargets {
		committed[id] = struct{}{}
	}
	for id := range failedDeletes {
		committed[id] = struct{}{}
	}
	c.targets[distGroup.Name] = committed

	if errFinal != nil {
		return errFinal
	}

	logr.Info("Reconciled targets", "distGroup", distGroup.Name, "count", len(newTargets))
	return nil
}
