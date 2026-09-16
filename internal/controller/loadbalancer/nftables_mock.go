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

// mockNftablesManager is a mock implementation for testing
type mockNftablesManager struct {
	setupCalled   bool
	setVIPsCalled bool
	cleanupCalled bool
	vips          []string
	setVIPsErr    error // when set, SetVIPs returns this error (and does not record vips)
}

func newMockNftablesManager() *mockNftablesManager {
	return &mockNftablesManager{}
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
