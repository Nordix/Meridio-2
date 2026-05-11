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
	"net"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/vishvananda/netlink"
)

// mockNetlinkOps simulates kernel routing state for testing
type mockNetlinkOps struct {
	rules  []netlink.Rule
	routes map[int]*netlink.Route // tableID → route
}

func newMockNetlinkOps() *mockNetlinkOps {
	return &mockNetlinkOps{
		routes: make(map[int]*netlink.Route),
	}
}

func newRoutingManagerWithOps(nl netlinkOps) *RoutingManager {
	return &RoutingManager{
		configuredFwmarks: make(map[int]routeInfo),
		nl:                nl,
	}
}

func (m *mockNetlinkOps) RuleAdd(rule *netlink.Rule) error {
	// Linux kernel allows duplicate rules (no EEXIST)
	m.rules = append(m.rules, *rule)
	return nil
}

func (m *mockNetlinkOps) RuleDel(rule *netlink.Rule) error {
	for i, r := range m.rules {
		if r.Mark == rule.Mark && r.Table == rule.Table && r.Family == rule.Family && r.Priority == rule.Priority {
			m.rules = append(m.rules[:i], m.rules[i+1:]...)
			return nil
		}
	}
	return syscall.ENOENT
}

func (m *mockNetlinkOps) RuleList(family int) ([]netlink.Rule, error) {
	var result []netlink.Rule
	for _, r := range m.rules {
		if r.Family == family {
			result = append(result, r)
		}
	}
	return result, nil
}

func (m *mockNetlinkOps) RouteReplace(route *netlink.Route) error {
	m.routes[route.Table] = route
	return nil
}

func (m *mockNetlinkOps) RouteDel(route *netlink.Route) error {
	existing, exists := m.routes[route.Table]
	if !exists {
		return syscall.ESRCH
	}
	// Kernel only deletes if gateway matches
	if !existing.Gw.Equal(route.Gw) {
		return syscall.ESRCH
	}
	delete(m.routes, route.Table)
	return nil
}

func (m *mockNetlinkOps) NeighList(link, family int) ([]netlink.Neigh, error) {
	return nil, nil
}

func (m *mockNetlinkOps) NeighDel(neigh *netlink.Neigh) error {
	return nil
}

// TestAddRoute_SucceedsWhenStaleRouteExists verifies that when a fwmark's
// target IP changes (e.g., Pod replacement), AddRoute succeeds by using
// RouteReplace to atomically overwrite the stale route.
func TestAddRoute_SucceedsWhenStaleRouteExists(t *testing.T) {
	mock := newMockNetlinkOps()
	rm := newRoutingManagerWithOps(mock)

	// Simulate state: fwmark 5000 was previously configured with old IP .4
	oldIP := net.ParseIP("192.168.100.4")
	rm.configuredFwmarks[5000] = routeInfo{tableID: 5000, gateway: oldIP}
	mock.routes[5000] = &netlink.Route{Gw: oldIP, Table: 5000}
	mock.rules = append(mock.rules, netlink.Rule{
		Mark: 5000, Table: 5000, Family: netlink.FAMILY_V4, Priority: 32000,
	})

	// LB controller tries to add route for same fwmark but new IP .3
	newIP := net.ParseIP("192.168.100.3")
	err := rm.AddRoute(5000, "192.168.100.3")

	assert.NoError(t, err)

	// Route now points to new IP
	assert.Equal(t, newIP, mock.routes[5000].Gw, "Route should point to new IP")

	// Fwmark rule still exists
	hasRule := false
	for _, r := range mock.rules {
		if r.Mark == 5000 {
			hasRule = true
		}
	}
	assert.True(t, hasRule, "Fwmark rule should exist")
}

// TestAddRoute_SucceedsAfterContainerRestart verifies that after a container restart
// (empty configuredFwmarks), AddRoute succeeds even when the kernel has a stale route
// with a different gateway.
func TestAddRoute_SucceedsAfterContainerRestart(t *testing.T) {
	mock := newMockNetlinkOps()
	rm := newRoutingManagerWithOps(mock)

	// Kernel state survives restart: route exists with old IP
	oldIP := net.ParseIP("192.168.100.4")
	mock.routes[5000] = &netlink.Route{Gw: oldIP, Table: 5000}
	// Rule also survives in kernel
	mock.rules = append(mock.rules, netlink.Rule{
		Mark: 5000, Table: 5000, Family: netlink.FAMILY_V4, Priority: 32000,
	})
	// configuredFwmarks is empty (process restarted)

	// Reconcile with same fwmark, different IP
	newIP := net.ParseIP("192.168.100.3")
	err := rm.AddRoute(5000, "192.168.100.3")

	assert.NoError(t, err)

	// Route updated to new IP
	assert.Equal(t, newIP, mock.routes[5000].Gw, "Route should point to new IP")

	// Rule exists (EEXIST from RuleAdd is tolerated)
	hasRule := false
	for _, r := range mock.rules {
		if r.Mark == 5000 {
			hasRule = true
		}
	}
	assert.True(t, hasRule, "Fwmark rule should exist")
}
