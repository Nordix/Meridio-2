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

package sidecar

import (
	"fmt"
	"maps"
)

// tableIDAllocator manages stable mapping from gateway names to routing table IDs.
// Sequential allocation from a configurable range. Freed IDs go to a reuse pool.
type tableIDAllocator struct {
	minID    int
	maxID    int
	assigned map[string]int // gateway name → table ID
	freedIDs []int
	nextID   int
}

func newTableIDAllocator(minID, maxID int) *tableIDAllocator {
	return &tableIDAllocator{
		minID:    minID,
		maxID:    maxID,
		assigned: make(map[string]int),
		nextID:   minID,
	}
}

// allocate returns the table ID for a gateway, assigning a new one if needed.
func (a *tableIDAllocator) allocate(gatewayName string) (int, error) {
	if id, exists := a.assigned[gatewayName]; exists {
		return id, nil
	}

	var id int
	if len(a.freedIDs) > 0 {
		id = a.freedIDs[0]
		a.freedIDs = a.freedIDs[1:]
	} else {
		if a.nextID > a.maxID {
			return 0, fmt.Errorf("table ID range exhausted (%d-%d)", a.minID, a.maxID)
		}
		id = a.nextID
		a.nextID++
	}

	a.assigned[gatewayName] = id
	return id, nil
}

// release frees the table ID for a gateway, returning it to the reuse pool.
func (a *tableIDAllocator) release(gatewayName string) {
	if id, exists := a.assigned[gatewayName]; exists {
		a.freedIDs = append(a.freedIDs, id)
		delete(a.assigned, gatewayName)
	}
}

// lookup returns the table ID for a gateway without allocating.
func (a *tableIDAllocator) lookup(gatewayName string) (int, bool) {
	id, exists := a.assigned[gatewayName]
	return id, exists
}

// activeGateways returns the set of gateway names with allocated table IDs.
func (a *tableIDAllocator) activeGateways() map[string]int {
	result := make(map[string]int, len(a.assigned))
	maps.Copy(result, a.assigned)
	return result
}
