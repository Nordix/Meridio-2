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
	t.Cleanup(func() { netns.Set(orig); orig.Close() })

	ns, err := netns.New()
	require.NoError(t, err)
	t.Cleanup(func() { ns.Close() })

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
