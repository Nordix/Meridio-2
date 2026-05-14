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
	"crypto/sha256"
	"encoding/hex"
	"net"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/rand"
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

// encodeCIDRForLabel converts CIDR to valid Kubernetes label value
// IPv4: "192.168.100.0/24" → "192.168.100.0-24"
// IPv6: "2001:db8:100::/64" → "2001_db8_100__-64"
func encodeCIDRForLabel(cidr string) string {
	encoded := strings.ReplaceAll(cidr, ":", "_")
	encoded = strings.ReplaceAll(encoded, "/", "-")
	return encoded
}

// decodeCIDRFromLabel converts label value back to CIDR
// IPv4: "192.168.100.0-24" → "192.168.100.0/24"
// IPv6: "2001_db8_100__-64" → "2001:db8:100::/64"
func decodeCIDRFromLabel(encoded string) string {
	// Find last dash and replace with /
	lastDash := strings.LastIndex(encoded, "-")
	if lastDash == -1 {
		return encoded
	}
	decoded := encoded[:lastDash] + "/" + encoded[lastDash+1:]
	// Restore colons for IPv6
	decoded = strings.ReplaceAll(decoded, "_", ":")
	return decoded
}

// hashCIDR creates a short hash suffix for CIDR to use in EndpointSlice names
// Uses K8s SafeEncodeString to avoid bad words and ensure consistency
// Examples: "192.168.1.0/24" → "x8f2a", "2001:db8::/32" → "k9b3c"
func hashCIDR(cidr string) string {
	h := sha256.Sum256([]byte(cidr))
	return rand.SafeEncodeString(hex.EncodeToString(h[:])[:8])
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

// ptr returns a pointer to the given value
func ptr[T any](v T) *T {
	return &v
}
