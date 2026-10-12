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

import nftablesmanager "github.com/nordix/meridio-2/internal/nftables"

// mockNftablesManager is a mock implementation for testing
type mockNftablesManager struct {
	setupCalled   bool
	setVIPsCalled bool
	cleanupCalled bool
	vips          []string
	setVIPsErr    error // when set, SetVIPs returns this error (and does not record vips)

	// counter-rule tracking
	reconciledDGs   map[string]nftablesmanager.DGCounters // last ReconcileDGCounters per DG
	reconcilePerTgt bool                                  // last perTarget flag seen
	reconcileErr    error                                 // when set, ReconcileDGCounters returns it
	removedDGs      []string                              // DGs passed to RemoveDGCounters
	dgRanges        map[string][2]int                     // DG -> {base,count} from SetDGRange
}

func newMockNftablesManager() *mockNftablesManager {
	return &mockNftablesManager{
		reconciledDGs: map[string]nftablesmanager.DGCounters{},
		dgRanges:      map[string][2]int{},
	}
}

func (m *mockNftablesManager) Setup() error {
	m.setupCalled = true
	return nil
}

func (m *mockNftablesManager) SetVIPs(cidrs []string) error {
	m.setVIPsCalled = true
	if m.setVIPsErr != nil {
		return m.setVIPsErr
	}
	m.vips = cidrs
	return nil
}

func (m *mockNftablesManager) Cleanup() error {
	m.cleanupCalled = true
	return nil
}

func (m *mockNftablesManager) ReconcileDGCounters(dg nftablesmanager.DGCounters, perTarget bool) error {
	if m.reconciledDGs == nil {
		m.reconciledDGs = map[string]nftablesmanager.DGCounters{}
	}
	m.reconciledDGs[dg.DG] = dg
	m.reconcilePerTgt = perTarget
	return m.reconcileErr
}

func (m *mockNftablesManager) RemoveDGCounters(dg string, _, _ int) error {
	m.removedDGs = append(m.removedDGs, dg)
	return nil
}

func (m *mockNftablesManager) SetDGRange(dg string, base, count int) {
	if m.dgRanges == nil {
		m.dgRanges = map[string][2]int{}
	}
	m.dgRanges[dg] = [2]int{base, count}
}

func (m *mockNftablesManager) ClearDGRange(dg string) {
	delete(m.dgRanges, dg)
}
