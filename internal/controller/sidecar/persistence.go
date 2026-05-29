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

// Package sidecar implements the network sidecar controller.
//
// Restart Recovery:
// The controller persists table ID mappings to /var/run/meridio/table-id-mapping.json
// (emptyDir volume) to prevent table ID shifts across container restarts. On startup,
// it also scans the kernel for existing VIPs (/32 and /128 addresses on secondary
// interfaces) to enable cleanup of stale VIPs even if the mapping file is lost.
package sidecar

import (
	"encoding/json"
	"fmt"
	"os"
)

const mappingFile = "/var/run/meridio/table-id-mapping.json"

// loadMapping reads the persisted table ID mapping and seeds the allocator.
// Returns nil if file doesn't exist (first run).
// Returns error if file is corrupted or contains invalid data.
func loadMapping(allocator *tableIDAllocator) error {
	data, err := os.ReadFile(mappingFile)
	if os.IsNotExist(err) {
		return nil // First run, no file yet
	}
	if err != nil {
		return fmt.Errorf("failed to read mapping file: %w", err)
	}

	var mapping map[string]int
	if err := json.Unmarshal(data, &mapping); err != nil {
		return fmt.Errorf("failed to parse mapping file: %w", err)
	}

	// Restore each gateway mapping with validation
	for gateway, tableID := range mapping {
		if err := allocator.restore(gateway, tableID); err != nil {
			return fmt.Errorf("failed to restore mapping for gateway %s: %w", gateway, err)
		}
	}

	return nil
}

// saveMapping writes the current allocator state to disk atomically.
// Non-fatal errors are returned but should not stop reconciliation.
func saveMapping(allocator *tableIDAllocator) error {
	mapping := allocator.snapshot()
	data, err := json.Marshal(mapping)
	if err != nil {
		return fmt.Errorf("failed to marshal mapping: %w", err)
	}

	// Atomic write: write to temp file, then rename
	tmpFile := mappingFile + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0600); err != nil {
		return fmt.Errorf("failed to write temp file: %w", err)
	}

	if err := os.Rename(tmpFile, mappingFile); err != nil {
		return fmt.Errorf("failed to rename temp file: %w", err)
	}

	return nil
}
