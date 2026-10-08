//go:build integration

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

package nfqlb

import (
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
)

// enterTestNetns moves the current OS thread into a fresh network namespace for isolated
// policy-rule testing, restoring the original on cleanup. Requires CAP_NET_ADMIN.
func enterTestNetns(t *testing.T) {
	t.Helper()
	runtime.LockOSThread()
	t.Cleanup(runtime.UnlockOSThread)

	orig, err := netns.Get()
	require.NoError(t, err)
	t.Cleanup(func() { _ = netns.Set(orig); _ = orig.Close() })

	ns, err := netns.New() // also switches the current thread into ns
	require.NoError(t, err)
	t.Cleanup(func() { _ = ns.Close() })
}

// addFwmarkRule installs a policy rule (fwmark -> table) in the current netns, mirroring what
// createPolicyRoute's getRule produces.
func addFwmarkRule(t *testing.T, family, fwmark int) {
	t.Helper()
	rule := netlink.NewRule()
	rule.Priority = rulePriority
	rule.Table = fwmark
	rule.Mark = uint32(fwmark)
	rule.Family = family
	require.NoError(t, netlink.RuleAdd(rule))
}

// TestIntegration_PolicyRouteCounts verifies the live PolicyRouteCounts read against a real
// kernel: it must count only LB-owned rules (Mark >= startingOffset), bucketed by family, and
// ignore the netns default rules (which have Mark 0).
func TestIntegration_PolicyRouteCounts(t *testing.T) {
	enterTestNetns(t)

	const startingOffset = 5002

	// Baseline: a fresh netns has only default rules (Mark 0) -> both families 0.
	counts, err := PolicyRouteCounts(startingOffset)
	require.NoError(t, err)
	assert.Equal(t, map[string]int{ipFamilyV4: 0, ipFamilyV6: 0}, counts)

	// Install 2 IPv4 + 1 IPv6 LB-owned rules, plus one below-offset rule that must be ignored.
	addFwmarkRule(t, netlink.FAMILY_V4, 5002)
	addFwmarkRule(t, netlink.FAMILY_V4, 5003)
	addFwmarkRule(t, netlink.FAMILY_V6, 5004)
	addFwmarkRule(t, netlink.FAMILY_V4, 100) // below startingOffset: ignored

	counts, err = PolicyRouteCounts(startingOffset)
	require.NoError(t, err)
	assert.Equal(t, map[string]int{ipFamilyV4: 2, ipFamilyV6: 1}, counts)
}

// TestIntegration_PolicyRouteCounts_InstanceMethod verifies the exported instance method reads
// using the LB's own starting offset.
func TestIntegration_PolicyRouteCounts_InstanceMethod(t *testing.T) {
	enterTestNetns(t)

	lb, err := New(WithFwmarkBase(5000))
	require.NoError(t, err)
	offset := lb.startingOffset() // 5002

	addFwmarkRule(t, netlink.FAMILY_V4, offset)
	addFwmarkRule(t, netlink.FAMILY_V6, offset+1)

	counts, err := lb.PolicyRouteCounts()
	require.NoError(t, err)
	assert.Equal(t, map[string]int{ipFamilyV4: 1, ipFamilyV6: 1}, counts)
}
