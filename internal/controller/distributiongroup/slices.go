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

package distributiongroup

import (
	"context"
	"net"
	"sort"
	"strconv"

	meridio2v1alpha1 "github.com/nordix/meridio-2/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// listOwnedSlices returns LoadBalancerEndpointSlices owned by the DistributionGroup.
// Results are sorted by name for deterministic processing order.
func (r *DistributionGroupReconciler) listOwnedSlices(ctx context.Context, dg *meridio2v1alpha1.DistributionGroup) ([]meridio2v1alpha1.LoadBalancerEndpointSlice, error) {
	var sliceList meridio2v1alpha1.LoadBalancerEndpointSliceList
	if err := r.List(ctx, &sliceList, client.InNamespace(dg.Namespace)); err != nil {
		return nil, err
	}

	var owned []meridio2v1alpha1.LoadBalancerEndpointSlice
	for _, slice := range sliceList.Items {
		if metav1.IsControlledBy(&slice, dg) {
			owned = append(owned, slice)
		}
	}

	sort.Slice(owned, func(i, j int) bool {
		return owned[i].Name < owned[j].Name
	})

	return owned, nil
}

// deleteAllOwnedSlices deletes all LoadBalancerEndpointSlices owned by the DistributionGroup
func (r *DistributionGroupReconciler) deleteAllOwnedSlices(ctx context.Context, dg *meridio2v1alpha1.DistributionGroup) error {
	slices, err := r.listOwnedSlices(ctx, dg)
	if err != nil {
		return err
	}

	for i := range slices {
		if err := r.Delete(ctx, &slices[i]); err != nil {
			return client.IgnoreNotFound(err)
		}
	}

	return nil
}

// calculateDesiredSlices computes the desired LoadBalancerEndpointSlices.
// One set of slices per Gateway (currently single-Gateway enforced).
func (r *DistributionGroupReconciler) calculateDesiredSlices(ctx context.Context, dg *meridio2v1alpha1.DistributionGroup, pods []corev1.Pod, gwContexts []gatewayNetworkContext, existingSlices []meridio2v1alpha1.LoadBalancerEndpointSlice) ([]meridio2v1alpha1.LoadBalancerEndpointSlice, *maglevCapacityInfo) {
	if len(gwContexts) == 0 {
		return nil, nil
	}

	// Group existing slices by Gateway (only for Gateways we're processing).
	// Order within each group is preserved from listOwnedSlices (sorted by name),
	// which ensures deterministic ID extraction and structure preservation.
	slicesByGateway := make(map[client.ObjectKey][]meridio2v1alpha1.LoadBalancerEndpointSlice, len(gwContexts))
	for _, gwContext := range gwContexts {
		slicesByGateway[gwContext.gateway] = nil // initialize keys for active Gateways
	}
	for _, s := range existingSlices {
		key := client.ObjectKey{Name: s.Spec.GatewayRef.Name, Namespace: s.Spec.GatewayRef.Namespace}
		if _, active := slicesByGateway[key]; active {
			slicesByGateway[key] = append(slicesByGateway[key], s)
		}
	}

	logger := log.FromContext(ctx)
	var allDesiredSlices []meridio2v1alpha1.LoadBalancerEndpointSlice
	var capacityInfo *maglevCapacityInfo

	for _, gwContext := range gwContexts {
		// Scrape all IPs for each Pod across this Gateway's networks
		podsWithAddrs := r.scrapePodsForGateway(pods, gwContext)
		if len(podsWithAddrs) == 0 {
			logger.V(1).Info("Skipping Gateway as no Pods in DistributionGroup have matching IP addresses", "distributionGroup", dg.Name, "gateway", gwContext.gateway.Name)
			continue
		}

		existingForGW := slicesByGateway[gwContext.gateway]

		if dg.Spec.Type == meridio2v1alpha1.DistributionGroupTypeMaglev {
			slices, cap := r.calculateMaglevSlices(dg, gwContext, podsWithAddrs, existingForGW)
			allDesiredSlices = append(allDesiredSlices, slices...)
			capacityInfo = cap
		} else {
			slices := r.createSlices(dg, gwContext, podsWithAddrs, nil, existingForGW)
			allDesiredSlices = append(allDesiredSlices, slices...)
		}
	}

	return allDesiredSlices, capacityInfo
}

// calculateMaglevSlices handles Maglev-specific endpoint assignment with stable IDs.
func (r *DistributionGroupReconciler) calculateMaglevSlices(dg *meridio2v1alpha1.DistributionGroup, gwCtx gatewayNetworkContext, podsWithAddrs []podWithAddresses, existingSlices []meridio2v1alpha1.LoadBalancerEndpointSlice) ([]meridio2v1alpha1.LoadBalancerEndpointSlice, *maglevCapacityInfo) {
	maxEndpoints := meridio2v1alpha1.DefaultMaglevMaxEndpoints
	if dg.Spec.Maglev != nil && dg.Spec.Maglev.MaxEndpoints > 0 {
		maxEndpoints = dg.Spec.Maglev.MaxEndpoints
	}

	// Extract existing Pod→ID assignments from existing slices.
	// All slices for a Gateway are expected to have consistent IDs for the same Pod UID,
	// so iteration order does not affect the result (slices sorted by name for determinism).
	existingAssignments := extractMaglevAssignments(existingSlices)

	// Extract Pods for ID assignment
	pods := make([]corev1.Pod, len(podsWithAddrs))
	for i := range podsWithAddrs {
		pods[i] = podsWithAddrs[i].pod
	}

	// Assign Maglev IDs
	podToID := assignMaglevIDs(pods, existingAssignments, maxEndpoints)

	// Track capacity
	var capacityInfo *maglevCapacityInfo
	if total := int32(len(podsWithAddrs)); int32(len(podToID)) < total {
		capacityInfo = &maglevCapacityInfo{
			excluded: total - int32(len(podToID)),
			total:    total,
		}
	}

	slices := r.createSlices(dg, gwCtx, podsWithAddrs, podToID, existingSlices)

	return slices, capacityInfo
}

// reconcileSlices creates, updates, or deletes LoadBalancerEndpointSlices to match desired state
func (r *DistributionGroupReconciler) reconcileSlices(ctx context.Context, dg *meridio2v1alpha1.DistributionGroup, desired, existing []meridio2v1alpha1.LoadBalancerEndpointSlice) error {
	desiredByName := make(map[string]*meridio2v1alpha1.LoadBalancerEndpointSlice)
	for i := range desired {
		desiredByName[desired[i].Name] = &desired[i]
	}

	existingByName := make(map[string]*meridio2v1alpha1.LoadBalancerEndpointSlice)
	for i := range existing {
		existingByName[existing[i].Name] = &existing[i]
	}

	// Create or update slices
	for name, desiredSlice := range desiredByName {
		existingSlice, exists := existingByName[name]

		if !exists {
			slice := desiredSlice.DeepCopy()
			if err := ctrl.SetControllerReference(dg, slice, r.Scheme); err != nil {
				return err
			}
			if err := r.Create(ctx, slice); err != nil {
				return err
			}
		} else {
			if !sliceNeedsUpdate(existingSlice, desiredSlice) {
				continue
			}

			slice := existingSlice.DeepCopy()
			slice.Spec = desiredSlice.Spec
			slice.Labels = desiredSlice.Labels
			if err := r.Update(ctx, slice); err != nil {
				return err
			}
		}
	}

	// Delete orphaned slices
	for name, existingSlice := range existingByName {
		if _, desired := desiredByName[name]; !desired {
			if err := r.Delete(ctx, existingSlice); err != nil {
				return client.IgnoreNotFound(err)
			}
		}
	}

	return nil
}

// sliceNeedsUpdate checks if a LoadBalancerEndpointSlice needs update
func sliceNeedsUpdate(existing, desired *meridio2v1alpha1.LoadBalancerEndpointSlice) bool {
	return !apiequality.Semantic.DeepEqual(existing.Spec, desired.Spec) ||
		!apiequality.Semantic.DeepEqual(existing.Labels, desired.Labels)
}

// scrapePodsForGateway extracts all secondary IPs for each Pod across the Gateway's networks.
// Returns only Pods that have at least one IP in any of the Gateway's networks.
func (r *DistributionGroupReconciler) scrapePodsForGateway(pods []corev1.Pod, gwCtx gatewayNetworkContext) []podWithAddresses {
	scraper := r.IPScraper
	if scraper == nil {
		scraper = defaultIPScraper
	}

	var result []podWithAddresses
	for _, pod := range pods {
		// Require primary PodIP (matches K8s EndpointSlice controller behavior)
		// TODO: Remove?
		if pod.Status.PodIP == "" {
			continue
		}

		var addresses []meridio2v1alpha1.EndpointAddress
		for cidr, attachmentType := range gwCtx.networks {
			ip := scraper(&pod, cidr, attachmentType)
			if ip == "" {
				continue
			}
			family := meridio2v1alpha1.IPv4
			if net.ParseIP(ip).To4() == nil {
				family = meridio2v1alpha1.IPv6
			}
			addresses = append(addresses, meridio2v1alpha1.EndpointAddress{
				IP:     ip,
				Family: family,
			})
		}

		if len(addresses) > 0 {
			// Sort addresses by family for deterministic ordering (IPv4 before IPv6)
			sort.Slice(addresses, func(i, j int) bool {
				return addresses[i].Family < addresses[j].Family
			})
			result = append(result, podWithAddresses{pod: pod, addresses: addresses})
		}
	}

	return result
}

// createSlices builds LoadBalancerEndpointSlice objects from Pods with addresses.
// podToID is optional (nil for non-Maglev). Existing slices are used for structure preservation.
func (r *DistributionGroupReconciler) createSlices(dg *meridio2v1alpha1.DistributionGroup, gwCtx gatewayNetworkContext, podsWithAddrs []podWithAddresses, podToID map[string]int32, existingSlices []meridio2v1alpha1.LoadBalancerEndpointSlice) []meridio2v1alpha1.LoadBalancerEndpointSlice {
	// Build endpoint map: Pod UID → LoadBalancerEndpoint
	endpointMap := make(map[string]meridio2v1alpha1.LoadBalancerEndpoint)
	for _, pwa := range podsWithAddrs {
		uid := string(pwa.pod.UID)

		// For Maglev: skip Pods without IDs (capacity exceeded)
		if podToID != nil {
			if _, hasID := podToID[uid]; !hasID {
				continue
			}
		}

		endpoint := meridio2v1alpha1.LoadBalancerEndpoint{
			Target: meridio2v1alpha1.EndpointTarget{
				Name: pwa.pod.Name,
				UID:  uid,
			},
			Addresses: pwa.addresses,
			Ready:     isPodReady(&pwa.pod),
		}

		if podToID != nil {
			if id, exists := podToID[uid]; exists {
				endpoint.Identifier = &id
			}
		}

		endpointMap[uid] = endpoint
	}

	// Structure preservation: fill existing slices with their surviving endpoints
	type sliceWithEndpoints struct {
		slice     meridio2v1alpha1.LoadBalancerEndpointSlice
		endpoints []meridio2v1alpha1.LoadBalancerEndpoint
	}
	var slices []sliceWithEndpoints

	for _, existingSlice := range existingSlices {
		var endpoints []meridio2v1alpha1.LoadBalancerEndpoint
		for _, ep := range existingSlice.Spec.Endpoints {
			if newEp, exists := endpointMap[ep.Target.UID]; exists {
				endpoints = append(endpoints, newEp)
				delete(endpointMap, ep.Target.UID)
			}
		}
		if len(endpoints) > 0 {
			slices = append(slices, sliceWithEndpoints{
				slice:     existingSlice,
				endpoints: endpoints,
			})
		}
	}

	// Collect remaining endpoints, sorted by Pod UID for determinism
	remainingEndpoints := make([]meridio2v1alpha1.LoadBalancerEndpoint, 0, len(endpointMap))
	for _, ep := range endpointMap {
		remainingEndpoints = append(remainingEndpoints, ep)
	}
	sort.Slice(remainingEndpoints, func(i, j int) bool {
		return remainingEndpoints[i].Target.UID < remainingEndpoints[j].Target.UID
	})

	// Fill remaining capacity in existing slices with new endpoints
	for i := range slices {
		capacity := r.MaxEndpointsPerSlice - len(slices[i].endpoints)
		if capacity > 0 && len(remainingEndpoints) > 0 {
			toAdd := min(capacity, len(remainingEndpoints))
			slices[i].endpoints = append(slices[i].endpoints, remainingEndpoints[:toAdd]...)
			remainingEndpoints = remainingEndpoints[toAdd:]
		}
	}

	// Compact: move endpoints from later slices into earlier slices with free capacity.
	// This prevents fragmentation after scale-in (mirrors core EndpointSlice controller behavior).
	for i := 0; i < len(slices)-1; i++ {
		capacity := r.MaxEndpointsPerSlice - len(slices[i].endpoints)
		if capacity <= 0 {
			continue
		}
		for j := len(slices) - 1; j > i; j-- {
			if len(slices[j].endpoints) == 0 {
				continue
			}
			toMove := min(capacity, len(slices[j].endpoints))
			slices[i].endpoints = append(slices[i].endpoints, slices[j].endpoints[:toMove]...)
			slices[j].endpoints = slices[j].endpoints[toMove:]
			capacity -= toMove
			if capacity <= 0 {
				break
			}
		}
	}

	// Remove empty slices after compaction
	compacted := slices[:0]
	for _, s := range slices {
		if len(s.endpoints) > 0 {
			compacted = append(compacted, s)
		}
	}
	slices = compacted

	// Build new slices for remaining endpoints.
	// Note: new slices and compaction-emptied slices are mutually exclusive —
	// remaining endpoints only exist when existing slices were full (no compaction possible),
	// and compaction only empties slices when existing slices had spare capacity (no remaining).
	for len(remainingEndpoints) > 0 {
		toAdd := min(r.MaxEndpointsPerSlice, len(remainingEndpoints))
		slices = append(slices, sliceWithEndpoints{
			endpoints: remainingEndpoints[:toAdd],
		})
		remainingEndpoints = remainingEndpoints[toAdd:]
	}

	// Build final slices with labels and metadata
	result := make([]meridio2v1alpha1.LoadBalancerEndpointSlice, 0, len(slices))
	baseName := sliceBaseName(dg.Name, gwCtx.gateway)
	for i, s := range slices {
		slice := s.slice
		if slice.Name == "" {
			slice.Name = baseName + "-" + strconv.Itoa(i)
			slice.Namespace = dg.Namespace
		}

		slice.Spec = meridio2v1alpha1.LoadBalancerEndpointSliceSpec{
			DistributionGroupName: dg.Name,
			GatewayRef: meridio2v1alpha1.SliceGatewayRef{
				Name:      gwCtx.gateway.Name,
				Namespace: gwCtx.gateway.Namespace,
			},
			Endpoints: s.endpoints,
		}
		slice.Labels = map[string]string{
			labelManagedBy:         managedByValue,
			labelDistributionGroup: truncateLabelValue(dg.Name),
		}

		result = append(result, slice)
	}

	return result
}
