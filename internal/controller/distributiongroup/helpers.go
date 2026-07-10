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
	"fmt"
	"hash/fnv"
	"net"
	"sort"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// sortPodsByCreationTime sorts Pods by CreationTimestamp, tiebreak by namespace/name
func sortPodsByCreationTime(pods []corev1.Pod) {
	sort.Slice(pods, func(i, j int) bool {
		if pods[i].CreationTimestamp.Equal(&pods[j].CreationTimestamp) {
			return pods[i].Namespace+"/"+pods[i].Name < pods[j].Namespace+"/"+pods[j].Name
		}
		return pods[i].CreationTimestamp.Before(&pods[j].CreationTimestamp)
	})
}

// normalizeCIDR returns the canonical form of a CIDR (network address with prefix)
// Example: "192.168.1.5/24" → "192.168.1.0/24"
// Example: "2001:db8:0:0::/32" → "2001:db8::/32"
func normalizeCIDR(cidr string) (string, error) {
	_, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return "", err
	}
	return ipnet.String(), nil
}

// truncateLabelValue truncates a string to the Kubernetes label value limit (63 chars).
func truncateLabelValue(value string) string {
	if len(value) <= 63 {
		return value
	}
	return value[:63]
}

// sliceBaseName returns a deterministic base name for LoadBalancerEndpointSlices.
// Format: "<dg-name>-<hash>" where hash is an FNV-64a digest of the full DG+Gateway identity.
// If the combined name exceeds 240 chars, the DG name is truncated to fit.
// The DG name is included in the hash input (alongside Gateway namespace/name) to avoid
// truncation-induced collisions: two DGs whose names share a long prefix (e.g., "my-app-dg"
// and "my-app-dg-extended") targeting the same Gateway would otherwise produce identical
// truncated prefixes and thus colliding slice names.
func sliceBaseName(dgName string, gateway client.ObjectKey) string {
	hasher := fnv.New64a()
	hasher.Write([]byte(dgName + "/" + gateway.Namespace + "/" + gateway.Name))
	hash := fmt.Sprintf("%016x", hasher.Sum64())

	// base = "<dg-name>-<hash>" (dg + "-" + 16 hex chars = dg + 17)
	base := dgName + "-" + hash
	const maxBaseLen = 240 // leave room for "-" + index (up to 4 digits) within 253 limit
	if len(base) <= maxBaseLen {
		return base
	}
	// Truncate DG name to fit
	return dgName[:maxBaseLen-17] + "-" + hash
}
