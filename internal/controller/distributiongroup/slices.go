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
	"maps"
	"net"
	"sort"
	"strconv"

	meridio2v1alpha1 "github.com/nordix/meridio-2/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// listOwnedSlices returns EndpointSlices owned by the DistributionGroup
func (r *DistributionGroupReconciler) listOwnedSlices(ctx context.Context, dg *meridio2v1alpha1.DistributionGroup) ([]discoveryv1.EndpointSlice, error) {
	var sliceList discoveryv1.EndpointSliceList
	if err := r.List(ctx, &sliceList, client.InNamespace(dg.Namespace)); err != nil {
		return nil, err
	}

	var owned []discoveryv1.EndpointSlice
	for _, slice := range sliceList.Items {
		if metav1.IsControlledBy(&slice, dg) {
			owned = append(owned, slice)
		}
	}

	// Sort by name for deterministic processing order (cache List does not guarantee order)
	sort.Slice(owned, func(i, j int) bool {
		return owned[i].Name < owned[j].Name
	})

	return owned, nil
}

// deleteAllOwnedSlices deletes all EndpointSlices owned by the DistributionGroup
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

// calculateDesiredSlices computes the desired EndpointSlices
func (r *DistributionGroupReconciler) calculateDesiredSlices(ctx context.Context, dg *meridio2v1alpha1.DistributionGroup, pods []corev1.Pod, gwContexts []gatewayNetworkContext, existingSlices []discoveryv1.EndpointSlice) ([]discoveryv1.EndpointSlice, *maglevCapacityInfo) {
	if len(gwContexts) == 0 {
		return nil, nil
	}

	slicesByNetwork := groupSlicesByNetwork(ctx, existingSlices)

	var allDesiredSlices []discoveryv1.EndpointSlice
	var capacityInfo *maglevCapacityInfo

	// Process each Gateway independently (scopes Maglev allocation per Gateway).
	// Currently iterates once due to single-Gateway enforcement; the loop structure
	// is intentional to keep multi-Gateway extensibility open. Supporting multiple
	// Gateways per DG with the current EndpointSlice model would be problematic:
	// Gateways sharing the same internal network would reference the same slices,
	// causing per-Gateway iterations to interfere (conflicting ID assignments and
	// updates on shared input). Clean endpoint representation separation between
	// Gateways is required first.
	for _, gwCtx := range gwContexts {
		// Filter Pods per network for this Gateway
		podsByNetwork := make(map[string][]podWithNetworkIP)
		for cidr, attachmentType := range gwCtx.networks {
			podsWithIP := r.filterPodsWithNetworkContextIP(pods, cidr, attachmentType)
			if len(podsWithIP) > 0 {
				podsByNetwork[cidr] = podsWithIP
			}
		}
		if len(podsByNetwork) == 0 {
			continue
		}

		if dg.Spec.Type == meridio2v1alpha1.DistributionGroupTypeMaglev {
			slices, cap := r.calculateMaglevSlices(dg, podsByNetwork, slicesByNetwork)
			allDesiredSlices = append(allDesiredSlices, slices...)
			capacityInfo = cap
		} else {
			// Non-Maglev: no capacity restrictions, no stable IDs
			for cidr, podsWithIP := range podsByNetwork {
				slices := createSlicesForNetwork(dg, podsWithIP, nil, cidr, slicesByNetwork[cidr])
				allDesiredSlices = append(allDesiredSlices, slices...)
			}
		}
	}

	return allDesiredSlices, capacityInfo
}

// calculateMaglevSlices handles Maglev-specific endpoint assignment with stable IDs.
// IDs are assigned per Pod UID across all networks within a single Gateway (not per-network).
func (r *DistributionGroupReconciler) calculateMaglevSlices(dg *meridio2v1alpha1.DistributionGroup, podsByNetwork map[string][]podWithNetworkIP, slicesByNetwork map[string][]discoveryv1.EndpointSlice) ([]discoveryv1.EndpointSlice, *maglevCapacityInfo) {
	maxEndpoints := meridio2v1alpha1.DefaultMaglevMaxEndpoints
	if dg.Spec.Maglev != nil && dg.Spec.Maglev.MaxEndpoints > 0 {
		maxEndpoints = dg.Spec.Maglev.MaxEndpoints
	}

	// Merge Pods across this Gateway's networks by UID. A Pod appearing in
	// multiple subnets (dual-stack) is counted once.
	// Use the largest network's Pod count as capacity hint (in dual-stack,
	// most Pods appear in all networks so the largest is a good estimate).
	var estimatedPods int
	for _, podsWithIP := range podsByNetwork {
		if len(podsWithIP) > estimatedPods {
			estimatedPods = len(podsWithIP)
		}
	}
	seen := make(map[string]bool, estimatedPods)
	allPods := make([]corev1.Pod, 0, estimatedPods)
	for _, podsWithIP := range podsByNetwork {
		for _, p := range podsWithIP {
			if uid := string(p.pod.UID); !seen[uid] {
				seen[uid] = true
				allPods = append(allPods, p.pod)
			}
		}
	}

	// Extract existing Pod→ID assignments from all network slices (merged view).
	// All slices are expected to have consistent IDs for the same Pod UID,
	// so iteration order over slicesByNetwork does not affect the result.
	existingAssignments := make(map[string]int32)
	for _, slices := range slicesByNetwork {
		maps.Copy(existingAssignments, extractMaglevAssignments(slices))
	}

	// Assign Maglev IDs once on the merged Pod set
	podToID := assignMaglevIDs(allPods, existingAssignments, maxEndpoints)

	// Track capacity based on merged set
	capacityInfo := &maglevCapacityInfo{
		networkIssues: make(map[string]struct{ excluded, total int32 }),
	}
	if total := int32(len(allPods)); int32(len(podToID)) < total {
		capacityInfo.networkIssues["all"] = struct{ excluded, total int32 }{
			excluded: total - int32(len(podToID)),
			total:    total,
		}
	}

	// Distribute shared IDs to per-network slices
	desiredSlices := make([]discoveryv1.EndpointSlice, 0, len(podsByNetwork))
	for cidr, podsWithIP := range podsByNetwork {
		slices := createSlicesForNetwork(dg, podsWithIP, podToID, cidr, slicesByNetwork[cidr])
		desiredSlices = append(desiredSlices, slices...)
	}

	return desiredSlices, capacityInfo
}

// reconcileSlices creates, updates, or deletes EndpointSlices to match desired state
func (r *DistributionGroupReconciler) reconcileSlices(ctx context.Context, dg *meridio2v1alpha1.DistributionGroup, desired, existing []discoveryv1.EndpointSlice) error {
	// Build maps for efficient lookup
	desiredByName := make(map[string]*discoveryv1.EndpointSlice)
	for i := range desired {
		desiredByName[desired[i].Name] = &desired[i]
	}

	existingByName := make(map[string]*discoveryv1.EndpointSlice)
	for i := range existing {
		existingByName[existing[i].Name] = &existing[i]
	}

	// Create or update slices
	for name, desiredSlice := range desiredByName {
		existingSlice, exists := existingByName[name]

		if !exists {
			// Create new slice
			slice := desiredSlice.DeepCopy()
			if err := ctrl.SetControllerReference(dg, slice, r.Scheme); err != nil {
				return err
			}
			if err := r.Create(ctx, slice); err != nil {
				return err
			}
		} else {
			// Update if endpoints, labels, or addressType changed
			if !endpointSliceNeedsUpdate(existingSlice, desiredSlice) {
				continue
			}

			slice := existingSlice.DeepCopy()
			slice.AddressType = desiredSlice.AddressType
			slice.Endpoints = desiredSlice.Endpoints
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

// endpointSliceNeedsUpdate checks if EndpointSlice needs update using semantic equality
func endpointSliceNeedsUpdate(existing, desired *discoveryv1.EndpointSlice) bool {
	return existing.AddressType != desired.AddressType ||
		!apiequality.Semantic.DeepEqual(existing.Endpoints, desired.Endpoints) ||
		!apiequality.Semantic.DeepEqual(existing.Labels, desired.Labels)
}

// groupSlicesByNetwork groups EndpointSlices by network-subnet label
// Returns map keyed by actual CIDR (decoded from label)
func groupSlicesByNetwork(ctx context.Context, slices []discoveryv1.EndpointSlice) map[string][]discoveryv1.EndpointSlice {
	logger := log.FromContext(ctx)
	grouped := make(map[string][]discoveryv1.EndpointSlice)
	for _, slice := range slices {
		if slice.Labels != nil {
			if encodedCIDR, ok := slice.Labels[labelNetworkSubnet]; ok {
				cidr := decodeCIDRFromLabel(encodedCIDR)
				// Normalize to handle tampered/weird labels
				normalized, err := normalizeCIDR(cidr)
				if err != nil {
					logger.Info("Skipping EndpointSlice with invalid network-subnet label", "slice", slice.Name, "label", cidr, "error", err)
					continue
				}
				grouped[normalized] = append(grouped[normalized], slice)
			}
		}
	}
	return grouped
}

// createSlicesForNetwork creates EndpointSlices for a specific network context
func createSlicesForNetwork(dg *meridio2v1alpha1.DistributionGroup, podsWithIP []podWithNetworkIP, podToID map[string]int32, cidr string, existingSlicesForNetwork []discoveryv1.EndpointSlice) []discoveryv1.EndpointSlice {
	// Detect address type from CIDR
	_, ipnet, err := net.ParseCIDR(cidr)
	addressType := discoveryv1.AddressTypeIPv4
	if err == nil && ipnet.IP.To4() == nil {
		addressType = discoveryv1.AddressTypeIPv6
	}

	// Build endpoint map: Pod UID → Endpoint
	// For Maglev: only include Pods with assigned IDs (capacity enforcement)
	endpointMap := make(map[string]discoveryv1.Endpoint)
	for _, pwip := range podsWithIP {
		if podToID != nil {
			// Skip Pods without Maglev IDs (capacity exceeded)
			if _, hasID := podToID[string(pwip.pod.UID)]; !hasID {
				continue
			}
		}

		endpoint := discoveryv1.Endpoint{
			Addresses: []string{pwip.ip},
			TargetRef: &corev1.ObjectReference{
				Kind:      kindPod,
				Namespace: pwip.pod.Namespace,
				Name:      pwip.pod.Name,
				UID:       pwip.pod.UID,
			},
			Conditions: discoveryv1.EndpointConditions{
				Ready: ptr(isPodReady(&pwip.pod)),
			},
		}

		// Set zone field for Maglev
		if podToID != nil {
			if id, exists := podToID[string(pwip.pod.UID)]; exists {
				zone := maglevIDPrefix + strconv.FormatInt(int64(id), 10)
				endpoint.Zone = &zone
			}
		}

		endpointMap[string(pwip.pod.UID)] = endpoint
	}

	// Preserve structure: map existing slices to their endpoints
	type sliceWithEndpoints struct {
		slice     discoveryv1.EndpointSlice
		endpoints []discoveryv1.Endpoint
	}
	var slices []sliceWithEndpoints

	// Fill existing slices first (structure preservation)
	for _, existingSlice := range existingSlicesForNetwork {
		var endpoints []discoveryv1.Endpoint
		for _, ep := range existingSlice.Endpoints {
			if ep.TargetRef == nil {
				continue
			}
			if newEp, exists := endpointMap[string(ep.TargetRef.UID)]; exists {
				endpoints = append(endpoints, newEp)
				delete(endpointMap, string(ep.TargetRef.UID))
			}
		}
		if len(endpoints) > 0 {
			slices = append(slices, sliceWithEndpoints{
				slice:     existingSlice,
				endpoints: endpoints,
			})
		}
	}

	// Collect remaining endpoints and sort deterministically by Pod UID
	remainingEndpoints := make([]discoveryv1.Endpoint, 0, len(endpointMap))
	for _, ep := range endpointMap {
		remainingEndpoints = append(remainingEndpoints, ep)
	}
	sort.Slice(remainingEndpoints, func(i, j int) bool {
		return remainingEndpoints[i].TargetRef.UID < remainingEndpoints[j].TargetRef.UID
	})

	// Fill remaining capacity in existing slices with new endpoints.
	for i := range slices {
		capacity := maxEndpointsPerSlice - len(slices[i].endpoints)
		if capacity > 0 && len(remainingEndpoints) > 0 {
			toAdd := min(capacity, len(remainingEndpoints))
			slices[i].endpoints = append(slices[i].endpoints, remainingEndpoints[:toAdd]...)
			remainingEndpoints = remainingEndpoints[toAdd:]
		}
	}

	// Compact: move endpoints from later slices into earlier slices with free capacity.
	// This prevents fragmentation after scale-in (mirrors core EndpointSlice controller behavior).
	for i := 0; i < len(slices)-1; i++ {
		capacity := maxEndpointsPerSlice - len(slices[i].endpoints)
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

	// Create new slices for remaining endpoints
	for len(remainingEndpoints) > 0 {
		toAdd := min(maxEndpointsPerSlice, len(remainingEndpoints))
		slices = append(slices, sliceWithEndpoints{
			endpoints: remainingEndpoints[:toAdd],
		})
		remainingEndpoints = remainingEndpoints[toAdd:]
	}

	// Build final slices with labels and metadata
	result := make([]discoveryv1.EndpointSlice, 0, len(slices))
	for i, s := range slices {
		slice := s.slice
		if slice.Name == "" {
			// New slice - generate name
			slice.Name = dg.Name + "-" + hashCIDR(cidr) + "-" + strconv.Itoa(i)
			slice.Namespace = dg.Namespace
		}

		slice.AddressType = addressType
		slice.Endpoints = s.endpoints
		slice.Labels = map[string]string{
			labelManagedBy:         managedByValue,
			labelDistributionGroup: dg.Name,
			labelNetworkSubnet:     encodeCIDRForLabel(cidr),
		}

		result = append(result, slice)
	}

	return result
}
