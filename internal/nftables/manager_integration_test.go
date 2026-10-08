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

package nftables

import (
	"runtime"
	"testing"

	"github.com/google/nftables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vishvananda/netns"
)

// newTestConn creates an nftables.Conn in a new network namespace for isolated testing.
// Requires CAP_NET_ADMIN.
func newTestConn(t *testing.T) *nftables.Conn {
	t.Helper()
	runtime.LockOSThread()
	t.Cleanup(runtime.UnlockOSThread)

	orig, err := netns.Get()
	require.NoError(t, err)
	t.Cleanup(func() { _ = netns.Set(orig); _ = orig.Close() })

	ns, err := netns.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = ns.Close() })

	return &nftables.Conn{NetNS: int(ns)}
}

func TestIntegration_SetupLoadsAllChains(t *testing.T) {
	conn := newTestConn(t)
	mgr := &Manager{
		tableName:  sharedTableName,
		queueNum:   0,
		queueTotal: 4,
		conn:       conn,
	}

	require.NoError(t, mgr.Setup())

	chains, err := conn.ListChains()
	require.NoError(t, err)

	chainNames := map[string]bool{}
	for _, c := range chains {
		chainNames[c.Name] = true
	}

	assert.True(t, chainNames[preroutingChainName], "prerouting chain should exist")
	assert.True(t, chainNames[outputChainName], "output chain should exist")
	assert.True(t, chainNames[pmtudChainName], "snat-local (PMTUD) chain should exist")
}

func TestIntegration_PMTUDChainType(t *testing.T) {
	conn := newTestConn(t)
	mgr := &Manager{
		tableName:  sharedTableName,
		queueNum:   0,
		queueTotal: 1,
		conn:       conn,
	}

	require.NoError(t, mgr.Setup())

	chains, err := conn.ListChains()
	require.NoError(t, err)

	for _, c := range chains {
		if c.Name == pmtudChainName {
			assert.Equal(t, nftables.ChainTypeRoute, c.Type,
				"PMTUD chain must be route type (not filter) for source address rewrite")
			assert.Equal(t, *nftables.ChainHookOutput, *c.Hooknum)
			return
		}
	}
	t.Fatal("snat-local chain not found")
}

func TestIntegration_PMTUDChainRuleCount(t *testing.T) {
	conn := newTestConn(t)
	mgr := &Manager{
		tableName:  sharedTableName,
		queueNum:   0,
		queueTotal: 1,
		conn:       conn,
	}

	require.NoError(t, mgr.Setup())

	rules, err := conn.GetRules(mgr.table, mgr.pmtudChain)
	require.NoError(t, err)
	assert.Len(t, rules, 2, "PMTUD chain should have 2 rules (IPv4 + IPv6)")
}

func TestIntegration_SetVIPsAndCleanup(t *testing.T) {
	conn := newTestConn(t)
	mgr := &Manager{
		tableName:  sharedTableName,
		queueNum:   0,
		queueTotal: 1,
		conn:       conn,
	}

	require.NoError(t, mgr.Setup())
	require.NoError(t, mgr.SetVIPs([]string{"10.0.0.1/32", "2001:db8::1/128"}))

	elems, err := conn.GetSetElements(mgr.ipv4Set)
	require.NoError(t, err)
	assert.NotEmpty(t, elems, "IPv4 set should have elements")

	elems, err = conn.GetSetElements(mgr.ipv6Set)
	require.NoError(t, err)
	assert.NotEmpty(t, elems, "IPv6 set should have elements")

	require.NoError(t, mgr.Cleanup())

	tables, err := conn.ListTables()
	require.NoError(t, err)
	assert.Empty(t, tables, "all tables should be removed after cleanup")
}

// TestIntegration_VIPSetSize verifies the live VIPSetSize read against a real kernel: it must
// count VIP ENTRIES (one per CIDR), not raw interval-set elements (which include end sentinels).
func TestIntegration_VIPSetSize(t *testing.T) {
	conn := newTestConn(t)
	mgr := &Manager{
		tableName:  sharedTableName,
		queueNum:   0,
		queueTotal: 1,
		conn:       conn,
	}
	require.NoError(t, mgr.Setup())

	// Empty sets read as 0/0.
	ipv4, ipv6, err := mgr.VIPSetSize()
	require.NoError(t, err)
	assert.Equal(t, 0, ipv4)
	assert.Equal(t, 0, ipv6)

	// 2 IPv4 + 1 IPv6 VIPs.
	require.NoError(t, mgr.SetVIPs([]string{"10.0.0.1/32", "10.0.0.2/32", "2001:db8::1/128"}))
	ipv4, ipv6, err = mgr.VIPSetSize()
	require.NoError(t, err)
	assert.Equal(t, 2, ipv4, "must count VIP entries, not interval-set end sentinels")
	assert.Equal(t, 1, ipv6)

	// Shrinking is reflected.
	require.NoError(t, mgr.SetVIPs([]string{"10.0.0.1/32"}))
	ipv4, ipv6, err = mgr.VIPSetSize()
	require.NoError(t, err)
	assert.Equal(t, 1, ipv4)
	assert.Equal(t, 0, ipv6)
}

// TestIntegration_DropCounts verifies the live DropCounts read against a real kernel: with drop
// accounting enabled (non-zero fwmarks) both reasons appear at 0 (freshly created counters).
func TestIntegration_DropCounts(t *testing.T) {
	conn := newTestConn(t)
	const nolb, notargets = 5000, 5001
	mgr := &Manager{
		tableName:       sharedTableName,
		queueNum:        0,
		queueTotal:      1,
		nolbFwmark:      nolb,
		notargetsFwmark: notargets,
		conn:            conn,
	}
	require.NoError(t, mgr.Setup())

	counts, err := mgr.DropCounts()
	require.NoError(t, err)
	// Both reasons present (chain has both rules), freshly zeroed.
	assert.Equal(t, map[string]uint64{DropReasonNoFlow: 0, DropReasonNoTargets: 0}, counts)
}

// TestIntegration_DropCounts_Disabled verifies that with drop accounting disabled (both fwmarks
// 0) the chain is not created and DropCounts returns an empty map (no series for the collector).
func TestIntegration_DropCounts_Disabled(t *testing.T) {
	conn := newTestConn(t)
	mgr := &Manager{
		tableName:  sharedTableName,
		queueNum:   0,
		queueTotal: 1,
		conn:       conn,
	}
	require.NoError(t, mgr.Setup())

	counts, err := mgr.DropCounts()
	require.NoError(t, err)
	assert.Empty(t, counts)
}
