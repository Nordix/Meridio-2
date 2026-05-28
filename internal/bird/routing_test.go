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

package bird

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

const (
	testTableID      = 4096
	testRulePriority = 100
)

// netlinkMock implements abstractNetlink for testing.
type netlinkMock struct {
	rules           []netlink.Rule
	routes          []netlink.Route
	ruleAddErr      error
	ruleDelErr      error
	ruleListErr     error
	routeReplaceErr error
}

func (m *netlinkMock) RuleListFiltered(_ int, filter *netlink.Rule, _ uint64) ([]netlink.Rule, error) {
	if m.ruleListErr != nil {
		return nil, m.ruleListErr
	}
	var result []netlink.Rule
	for _, r := range m.rules {
		if r.Table == filter.Table {
			result = append(result, r)
		}
	}
	return result, nil
}

func (m *netlinkMock) RuleAdd(rule *netlink.Rule) error {
	if m.ruleAddErr != nil {
		return m.ruleAddErr
	}
	m.rules = append(m.rules, *rule)
	return nil
}

func (m *netlinkMock) RuleDel(rule *netlink.Rule) error {
	if m.ruleDelErr != nil {
		return m.ruleDelErr
	}
	filtered := m.rules[:0]
	for _, r := range m.rules {
		if r.Table == rule.Table && r.Src != nil && rule.Src != nil && r.Src.String() == rule.Src.String() {
			continue
		}
		filtered = append(filtered, r)
	}
	m.rules = filtered
	return nil
}

func (m *netlinkMock) RouteReplace(route *netlink.Route) error {
	if m.routeReplaceErr != nil {
		return m.routeReplaceErr
	}
	// Replace existing route for same table+type, or append
	for i, r := range m.routes {
		if r.Table == route.Table && r.Type == route.Type && r.Dst.String() == route.Dst.String() {
			m.routes[i] = *route
			return nil
		}
	}
	m.routes = append(m.routes, *route)
	return nil
}

func (m *netlinkMock) rulesForTable(table int) []netlink.Rule {
	var result []netlink.Rule
	for _, r := range m.rules {
		if r.Table == table {
			result = append(result, r)
		}
	}
	return result
}

func (m *netlinkMock) blackholeRoutes() []netlink.Route {
	var result []netlink.Route
	for _, r := range m.routes {
		if r.Type == unix.RTN_BLACKHOLE {
			result = append(result, r)
		}
	}
	return result
}

// --- setupBlackholeRoutes tests ---

func TestSetupBlackholeRoutes(t *testing.T) {
	m := &netlinkMock{}
	blackholeTableID := testTableID + 1

	err := setupBlackholeRoutes(m, blackholeTableID)
	assert.NoError(t, err)

	routes := m.blackholeRoutes()
	assert.Len(t, routes, 2)
	assert.Equal(t, blackholeTableID, routes[0].Table)
	assert.Equal(t, blackholeTableID, routes[1].Table)
}

func TestSetupBlackholeRoutes_RouteReplaceFails(t *testing.T) {
	m := &netlinkMock{routeReplaceErr: fmt.Errorf("EPERM")}

	err := setupBlackholeRoutes(m, testTableID+1)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "EPERM")
}

// --- setPolicyRoutes tests ---

func TestSetPolicyRoutes_EmptyVIPs(t *testing.T) {
	m := &netlinkMock{}

	err := setPolicyRoutes(m, []string{}, testTableID, testRulePriority)
	assert.NoError(t, err)

	assert.Empty(t, m.rulesForTable(testTableID))
	assert.Empty(t, m.rulesForTable(testTableID+1))
	// Blackhole routes should still be created
	assert.Len(t, m.blackholeRoutes(), 2)
}

func TestSetPolicyRoutes_AddNewVIPs(t *testing.T) {
	m := &netlinkMock{}

	err := setPolicyRoutes(m, []string{"20.0.0.1/32", "20.0.0.2/32"}, testTableID, testRulePriority)
	assert.NoError(t, err)

	bgpRules := m.rulesForTable(testTableID)
	bhRules := m.rulesForTable(testTableID + 1)
	assert.Len(t, bgpRules, 2)
	assert.Len(t, bhRules, 2)

	// Verify priorities
	assert.Equal(t, testRulePriority, bgpRules[0].Priority)
	assert.Equal(t, testRulePriority+1, bhRules[0].Priority)
}

func TestSetPolicyRoutes_Idempotent(t *testing.T) {
	m := &netlinkMock{}
	vips := []string{"20.0.0.1/32"}

	err := setPolicyRoutes(m, vips, testTableID, testRulePriority)
	assert.NoError(t, err)

	err = setPolicyRoutes(m, vips, testTableID, testRulePriority)
	assert.NoError(t, err)

	// Should still have exactly 1 rule per table, not duplicated
	assert.Len(t, m.rulesForTable(testTableID), 1)
	assert.Len(t, m.rulesForTable(testTableID+1), 1)
}

func TestSetPolicyRoutes_RemoveStaleVIPs(t *testing.T) {
	m := &netlinkMock{}

	err := setPolicyRoutes(m, []string{"20.0.0.1/32", "20.0.0.2/32"}, testTableID, testRulePriority)
	assert.NoError(t, err)
	assert.Len(t, m.rulesForTable(testTableID), 2)

	// Remove one VIP
	err = setPolicyRoutes(m, []string{"20.0.0.1/32"}, testTableID, testRulePriority)
	assert.NoError(t, err)

	bgpRules := m.rulesForTable(testTableID)
	assert.Len(t, bgpRules, 1)
	assert.Equal(t, "20.0.0.1/32", bgpRules[0].Src.String())

	bhRules := m.rulesForTable(testTableID + 1)
	assert.Len(t, bhRules, 1)
	assert.Equal(t, "20.0.0.1/32", bhRules[0].Src.String())
}

func TestSetPolicyRoutes_MixedAddRemove(t *testing.T) {
	m := &netlinkMock{}

	err := setPolicyRoutes(m, []string{"20.0.0.1/32", "20.0.0.2/32"}, testTableID, testRulePriority)
	assert.NoError(t, err)

	// Remove .2, keep .1, add .3
	err = setPolicyRoutes(m, []string{"20.0.0.1/32", "20.0.0.3/32"}, testTableID, testRulePriority)
	assert.NoError(t, err)

	bgpRules := m.rulesForTable(testTableID)
	assert.Len(t, bgpRules, 2)

	srcs := make(map[string]bool)
	for _, r := range bgpRules {
		srcs[r.Src.String()] = true
	}
	assert.True(t, srcs["20.0.0.1/32"])
	assert.True(t, srcs["20.0.0.3/32"])
	assert.False(t, srcs["20.0.0.2/32"])
}

func TestSetPolicyRoutes_DualStack(t *testing.T) {
	m := &netlinkMock{}

	err := setPolicyRoutes(m, []string{"20.0.0.1/32", "2001:db8::1/128"}, testTableID, testRulePriority)
	assert.NoError(t, err)

	bgpRules := m.rulesForTable(testTableID)
	assert.Len(t, bgpRules, 2)

	srcs := make(map[string]bool)
	for _, r := range bgpRules {
		srcs[r.Src.String()] = true
	}
	assert.True(t, srcs["20.0.0.1/32"])
	assert.True(t, srcs["2001:db8::1/128"])
}

func TestSetPolicyRoutes_InvalidCIDR(t *testing.T) {
	m := &netlinkMock{}

	err := setPolicyRoutes(m, []string{"not-a-cidr"}, testTableID, testRulePriority)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse VIP CIDR")
}

func TestSetPolicyRoutes_RuleListFails(t *testing.T) {
	m := &netlinkMock{ruleListErr: fmt.Errorf("ENOMEM")}

	err := setPolicyRoutes(m, []string{"20.0.0.1/32"}, testTableID, testRulePriority)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to list")
}

func TestSetPolicyRoutes_RuleAddFails_BestEffort(t *testing.T) {
	m := &netlinkMock{ruleAddErr: fmt.Errorf("EPERM")}

	err := setPolicyRoutes(m, []string{"20.0.0.1/32", "20.0.0.2/32"}, testTableID, testRulePriority)
	assert.Error(t, err)
	// Both BGP and blackhole adds fail, but all are attempted (best-effort)
	assert.Contains(t, err.Error(), "EPERM")
}

func TestSetPolicyRoutes_RuleDelFails_BestEffort(t *testing.T) {
	m := &netlinkMock{}

	// Add two VIPs
	err := setPolicyRoutes(m, []string{"20.0.0.1/32", "20.0.0.2/32"}, testTableID, testRulePriority)
	assert.NoError(t, err)

	// Inject delete error, then remove one VIP
	m.ruleDelErr = fmt.Errorf("EBUSY")
	err = setPolicyRoutes(m, []string{"20.0.0.1/32"}, testTableID, testRulePriority)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "EBUSY")
}

func TestSetPolicyRoutes_RouteReplaceFails(t *testing.T) {
	m := &netlinkMock{routeReplaceErr: fmt.Errorf("EPERM")}

	err := setPolicyRoutes(m, []string{"20.0.0.1/32"}, testTableID, testRulePriority)
	assert.Error(t, err)
	// Fails at blackhole setup, before rule processing
	assert.Empty(t, m.rulesForTable(testTableID))
}
