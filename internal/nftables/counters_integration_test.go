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
	"testing"

	"github.com/google/nftables"
	"github.com/google/nftables/expr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestManager creates a Manager backed by a real kernel (new netns) with Setup() already
// called. Requires CAP_NET_ADMIN.
func newTestManager(t *testing.T) *Manager {
	t.Helper()
	conn := newTestConn(t)
	mgr := &Manager{
		tableName:  sharedTableName,
		queueNum:   0,
		queueTotal: 1,
		conn:       conn,
	}
	require.NoError(t, mgr.Setup())
	return mgr
}

// countRulesInChain returns the number of rules in the given chain.
func countRulesInChain(t *testing.T, conn *nftables.Conn, table *nftables.Table, chain *nftables.Chain) int {
	t.Helper()
	rules, err := conn.GetRules(table, chain)
	require.NoError(t, err)
	return len(rules)
}

// --- Per-DG range rule tests ---

func TestIntegration_ReconcileDGCounters_RangeRule(t *testing.T) {
	mgr := newTestManager(t)
	rngs := map[string]DGRange{}

	dg := DGCounters{DG: "dg-a", FwmarkBase: 5002, MaxTargets: 32}
	rngs["dg-a"] = DGRange{Base: 5002, Count: 32}

	require.NoError(t, mgr.ReconcileDGCounters(dg, false))

	rules, err := mgr.conn.GetRules(mgr.table, mgr.targetChain)
	require.NoError(t, err)
	assert.Len(t, rules, 1, "per-DG mode installs exactly one range rule")

	// The single rule should match a range (Gte + Lt), not an exact fwmark.
	assert.False(t, isExactMarkRule(rules[0]), "per-DG range rule must not be an exact-mark rule")

	// PacketByteCounts returns the DG with zero counters (no traffic).
	perDG, perTarget, err := mgr.PacketByteCounts(rngs)
	require.NoError(t, err)
	assert.Contains(t, perDG, "dg-a")
	assert.Equal(t, uint64(0), perDG["dg-a"].Packets)
	assert.Equal(t, uint64(0), perDG["dg-a"].Bytes)
	assert.Empty(t, perTarget, "per-target map empty in range-rule mode")
}

// R4: adjacent DGs with contiguous fwmark ranges must not cross-count. DG-A owns [5002, 5034)
// and DG-B owns [5034, 5050). Each DG's range rule must not overlap the other's range.
func TestIntegration_ReconcileDGCounters_AdjacentDGs_NoCrossCount(t *testing.T) {
	mgr := newTestManager(t)
	rngs := map[string]DGRange{}

	dgA := DGCounters{DG: "dg-a", FwmarkBase: 5002, MaxTargets: 32}
	dgB := DGCounters{DG: "dg-b", FwmarkBase: 5034, MaxTargets: 16}
	rngs["dg-a"] = DGRange{Base: 5002, Count: 32}
	rngs["dg-b"] = DGRange{Base: 5034, Count: 16}

	require.NoError(t, mgr.ReconcileDGCounters(dgA, false))
	require.NoError(t, mgr.ReconcileDGCounters(dgB, false))

	rules, err := mgr.conn.GetRules(mgr.table, mgr.targetChain)
	require.NoError(t, err)
	assert.Len(t, rules, 2, "two DGs = two range rules")

	// Both should read as separate DGs with zero counters.
	perDG, _, err := mgr.PacketByteCounts(rngs)
	require.NoError(t, err)
	assert.Contains(t, perDG, "dg-a")
	assert.Contains(t, perDG, "dg-b")
}

func TestIntegration_ReconcileDGCounters_Idempotent(t *testing.T) {
	mgr := newTestManager(t)
	rngs := map[string]DGRange{}

	dg := DGCounters{DG: "dg-a", FwmarkBase: 5002, MaxTargets: 32}
	rngs["dg-a"] = DGRange{Base: 5002, Count: 32}

	require.NoError(t, mgr.ReconcileDGCounters(dg, false))
	require.NoError(t, mgr.ReconcileDGCounters(dg, false))

	// Must still be exactly one rule (no duplicates).
	assert.Equal(t, 1, countRulesInChain(t, mgr.conn, mgr.table, mgr.targetChain))
}

// A repeated per-DG reconcile (as happens on every target churn, since reconcileTargets calls
// ReconcileDGCounters) must PRESERVE the existing range rule rather than delete+re-add it —
// otherwise the nftables counter (which lives on the rule object) would reset to zero, breaking
// _lb_packets_total / _lb_bytes_total across routine churn. The kernel assigns each rule a Handle;
// a preserved rule keeps its handle, a recreated one gets a new (higher) handle. We assert the
// handle is unchanged across reconciles.
func TestIntegration_ReconcileDGCounters_PerDGPreservesRuleAcrossChurn(t *testing.T) {
	mgr := newTestManager(t)
	rngs := map[string]DGRange{}

	dg := DGCounters{DG: "dg-a", FwmarkBase: 5002, MaxTargets: 32}
	rngs["dg-a"] = DGRange{Base: 5002, Count: 32}

	require.NoError(t, mgr.ReconcileDGCounters(dg, false))
	first, err := mgr.conn.GetRules(mgr.table, mgr.targetChain)
	require.NoError(t, err)
	require.Len(t, first, 1)
	handle0 := first[0].Handle

	// Simulate several churn-driven reconciles (same range, DG unchanged).
	for range 3 {
		require.NoError(t, mgr.ReconcileDGCounters(dg, false))
	}

	after, err := mgr.conn.GetRules(mgr.table, mgr.targetChain)
	require.NoError(t, err)
	require.Len(t, after, 1, "still exactly one range rule")
	assert.Equal(t, handle0, after[0].Handle,
		"range rule handle must be unchanged (rule preserved, counter not reset)")
}

// When the DG's range actually changes (would only happen if maxEndpoints changed — immutable in
// practice, but the reconcile must still converge), the old rule is replaced.
func TestIntegration_ReconcileDGCounters_PerDGRebuildsOnRangeChange(t *testing.T) {
	mgr := newTestManager(t)
	rngs := map[string]DGRange{}

	rngs["dg-a"] = DGRange{Base: 5002, Count: 32}
	require.NoError(t, mgr.ReconcileDGCounters(DGCounters{DG: "dg-a", FwmarkBase: 5002, MaxTargets: 32}, false))
	first, err := mgr.conn.GetRules(mgr.table, mgr.targetChain)
	require.NoError(t, err)
	require.Len(t, first, 1)

	// Different MaxTargets => different range => rule must be rebuilt.
	rngs["dg-a"] = DGRange{Base: 5002, Count: 64}
	require.NoError(t, mgr.ReconcileDGCounters(DGCounters{DG: "dg-a", FwmarkBase: 5002, MaxTargets: 64}, false))
	after, err := mgr.conn.GetRules(mgr.table, mgr.targetChain)
	require.NoError(t, err)
	require.Len(t, after, 1, "still one range rule")
	assert.NotEqual(t, first[0].Handle, after[0].Handle,
		"range rule must be rebuilt when the range changes")
}

// --- Per-target exact-fwmark rule tests ---

func TestIntegration_ReconcileDGCounters_PerTarget(t *testing.T) {
	mgr := newTestManager(t)
	rngs := map[string]DGRange{}

	dg := DGCounters{
		DG: "dg-a", FwmarkBase: 5002, MaxTargets: 32,
		Targets: map[int]struct{}{0: {}, 3: {}, 7: {}},
	}
	rngs["dg-a"] = DGRange{Base: 5002, Count: 32}

	require.NoError(t, mgr.ReconcileDGCounters(dg, true))

	rules, err := mgr.conn.GetRules(mgr.table, mgr.targetChain)
	require.NoError(t, err)
	assert.Len(t, rules, 3, "3 live targets = 3 exact-fwmark rules")

	for _, r := range rules {
		assert.True(t, isExactMarkRule(r), "per-target rule must be exact-mark (CmpOpEq)")
	}

	// PacketByteCounts returns per-DG (sum) and per-target entries (all zero — no traffic).
	perDG, perTarget, err := mgr.PacketByteCounts(rngs)
	require.NoError(t, err)
	assert.Contains(t, perDG, "dg-a")
	assert.Equal(t, uint64(0), perDG["dg-a"].Packets)
	assert.Len(t, perTarget, 3)
	assert.Contains(t, perTarget, DGTarget{DG: "dg-a", Target: "0"})
	assert.Contains(t, perTarget, DGTarget{DG: "dg-a", Target: "3"})
	assert.Contains(t, perTarget, DGTarget{DG: "dg-a", Target: "7"})
}

// R4 per-target variant: adjacent DGs each get their own exact-fwmark rules; targets at the
// boundary (DG-A's last target vs DG-B's first) don't cross-count.
func TestIntegration_ReconcileDGCounters_PerTarget_AdjacentDGs(t *testing.T) {
	mgr := newTestManager(t)
	rngs := map[string]DGRange{}

	// DG-A owns [5002, 5034), target at id=31 → fwmark 5033 (last in range).
	// DG-B owns [5034, 5050), target at id=0  → fwmark 5034 (first in range).
	dgA := DGCounters{
		DG: "dg-a", FwmarkBase: 5002, MaxTargets: 32,
		Targets: map[int]struct{}{31: {}},
	}
	dgB := DGCounters{
		DG: "dg-b", FwmarkBase: 5034, MaxTargets: 16,
		Targets: map[int]struct{}{0: {}},
	}
	rngs["dg-a"] = DGRange{Base: 5002, Count: 32}
	rngs["dg-b"] = DGRange{Base: 5034, Count: 16}

	require.NoError(t, mgr.ReconcileDGCounters(dgA, true))
	require.NoError(t, mgr.ReconcileDGCounters(dgB, true))

	rules, err := mgr.conn.GetRules(mgr.table, mgr.targetChain)
	require.NoError(t, err)
	assert.Len(t, rules, 2, "one rule per live target across both DGs")

	perDG, perTarget, err := mgr.PacketByteCounts(rngs)
	require.NoError(t, err)
	assert.Contains(t, perDG, "dg-a")
	assert.Contains(t, perDG, "dg-b")
	assert.Contains(t, perTarget, DGTarget{DG: "dg-a", Target: "31"})
	assert.Contains(t, perTarget, DGTarget{DG: "dg-b", Target: "0"})
}

// --- Mode switch tests ---

func TestIntegration_ReconcileDGCounters_SwitchRangeToPerTarget(t *testing.T) {
	mgr := newTestManager(t)
	rngs := map[string]DGRange{}

	dg := DGCounters{
		DG: "dg-a", FwmarkBase: 5002, MaxTargets: 32,
		Targets: map[int]struct{}{0: {}, 1: {}},
	}
	rngs["dg-a"] = DGRange{Base: 5002, Count: 32}

	// Start with range rule.
	require.NoError(t, mgr.ReconcileDGCounters(dg, false))
	assert.Equal(t, 1, countRulesInChain(t, mgr.conn, mgr.table, mgr.targetChain))

	// Switch to per-target.
	require.NoError(t, mgr.ReconcileDGCounters(dg, true))
	n := countRulesInChain(t, mgr.conn, mgr.table, mgr.targetChain)
	assert.Equal(t, 2, n, "range rule replaced by 2 per-target rules")

	_, perTarget, err := mgr.PacketByteCounts(rngs)
	require.NoError(t, err)
	assert.Len(t, perTarget, 2, "per-target read produces entries after mode switch")
}

func TestIntegration_ReconcileDGCounters_SwitchPerTargetToRange(t *testing.T) {
	mgr := newTestManager(t)
	rngs := map[string]DGRange{}

	dg := DGCounters{
		DG: "dg-a", FwmarkBase: 5002, MaxTargets: 32,
		Targets: map[int]struct{}{0: {}, 1: {}},
	}
	rngs["dg-a"] = DGRange{Base: 5002, Count: 32}

	// Start with per-target rules.
	require.NoError(t, mgr.ReconcileDGCounters(dg, true))
	assert.Equal(t, 2, countRulesInChain(t, mgr.conn, mgr.table, mgr.targetChain))

	// Switch to range rule.
	require.NoError(t, mgr.ReconcileDGCounters(dg, false))
	n := countRulesInChain(t, mgr.conn, mgr.table, mgr.targetChain)
	assert.Equal(t, 1, n, "per-target rules replaced by single range rule")

	_, perTarget, err := mgr.PacketByteCounts(rngs)
	require.NoError(t, err)
	assert.Empty(t, perTarget, "per-target read is empty after switching back to range mode")
}

// Edge case for the per-DG skip optimization: switching to per-DG mode from a per-target set with
// exactly ONE target. The chain then holds a single exact-fwmark rule, so inRange == 1 — but the
// skip must NOT fire, because that rule is an exact-mark rule, not the desired range rule. The
// two-part guard (inRange == 1 AND rangeRuleFound) must detect this and rebuild into a range rule.
func TestIntegration_ReconcileDGCounters_SwitchSinglePerTargetToRange(t *testing.T) {
	mgr := newTestManager(t)
	rngs := map[string]DGRange{}

	rngs["dg-a"] = DGRange{Base: 5002, Count: 32}

	// Start with exactly ONE per-target rule.
	require.NoError(t, mgr.ReconcileDGCounters(
		DGCounters{DG: "dg-a", FwmarkBase: 5002, MaxTargets: 32, Targets: map[int]struct{}{0: {}}}, true))
	rules, err := mgr.conn.GetRules(mgr.table, mgr.targetChain)
	require.NoError(t, err)
	require.Len(t, rules, 1)
	require.True(t, isExactMarkRule(rules[0]), "precondition: single exact-mark rule present")

	// Switch to per-DG mode: must rebuild into a range rule, not skip on the inRange==1 count alone.
	require.NoError(t, mgr.ReconcileDGCounters(
		DGCounters{DG: "dg-a", FwmarkBase: 5002, MaxTargets: 32}, false))

	after, err := mgr.conn.GetRules(mgr.table, mgr.targetChain)
	require.NoError(t, err)
	require.Len(t, after, 1, "one rule after switch")
	assert.True(t, isRangeRule(after[0], 5002, 5002+32),
		"the exact-mark rule must be replaced by the DG range rule, not preserved")
	assert.False(t, isExactMarkRule(after[0]), "must no longer be an exact-mark rule")

	_, perTarget, err := mgr.PacketByteCounts(rngs)
	require.NoError(t, err)
	assert.Empty(t, perTarget, "per-target read empty after switching to range mode")
}

// --- Removal tests ---

func TestIntegration_RemoveDGCounters(t *testing.T) {
	mgr := newTestManager(t)
	rngs := map[string]DGRange{}

	dg := DGCounters{DG: "dg-a", FwmarkBase: 5002, MaxTargets: 32}
	rngs["dg-a"] = DGRange{Base: 5002, Count: 32}

	require.NoError(t, mgr.ReconcileDGCounters(dg, false))
	assert.Equal(t, 1, countRulesInChain(t, mgr.conn, mgr.table, mgr.targetChain))

	require.NoError(t, mgr.RemoveDGCounters("dg-a", 5002, 32))
	assert.Equal(t, 0, countRulesInChain(t, mgr.conn, mgr.table, mgr.targetChain))
}

func TestIntegration_RemoveDGCounters_LeavesOtherDG(t *testing.T) {
	mgr := newTestManager(t)
	rngs := map[string]DGRange{}

	dgA := DGCounters{DG: "dg-a", FwmarkBase: 5002, MaxTargets: 32}
	dgB := DGCounters{DG: "dg-b", FwmarkBase: 5034, MaxTargets: 16}
	rngs["dg-a"] = DGRange{Base: 5002, Count: 32}
	rngs["dg-b"] = DGRange{Base: 5034, Count: 16}

	require.NoError(t, mgr.ReconcileDGCounters(dgA, false))
	require.NoError(t, mgr.ReconcileDGCounters(dgB, false))
	assert.Equal(t, 2, countRulesInChain(t, mgr.conn, mgr.table, mgr.targetChain))

	// Remove only dg-a.
	require.NoError(t, mgr.RemoveDGCounters("dg-a", 5002, 32))
	assert.Equal(t, 1, countRulesInChain(t, mgr.conn, mgr.table, mgr.targetChain))

	perDG, _, err := mgr.PacketByteCounts(rngs)
	require.NoError(t, err)
	assert.NotContains(t, perDG, "dg-a", "removed DG must not appear")
	assert.Contains(t, perDG, "dg-b", "surviving DG must still appear")
}

// --- PacketByteCounts with no chain ---

func TestIntegration_PacketByteCounts_NoChain(t *testing.T) {
	conn := newTestConn(t)
	mgr := &Manager{conn: conn}
	// targetChain is nil (Setup not called).
	perDG, perTarget, err := mgr.PacketByteCounts(map[string]DGRange{})
	require.NoError(t, err)
	assert.Empty(t, perDG)
	assert.Empty(t, perTarget)
}

// --- Verify rule expressions against real kernel ---

// TestIntegration_PerTargetRuleExprs verifies that the kernel returns the rule expressions we
// installed (Meta+CmpEq+Counter), confirming the encoding round-trips correctly.
func TestIntegration_PerTargetRuleExprs(t *testing.T) {
	mgr := newTestManager(t)
	rngs := map[string]DGRange{}
	rngs["dg-a"] = DGRange{Base: 5002, Count: 32}

	dg := DGCounters{
		DG: "dg-a", FwmarkBase: 5002, MaxTargets: 32,
		Targets: map[int]struct{}{5: {}},
	}
	require.NoError(t, mgr.ReconcileDGCounters(dg, true))

	rules, err := mgr.conn.GetRules(mgr.table, mgr.targetChain)
	require.NoError(t, err)
	require.Len(t, rules, 1)

	rule := rules[0]
	// Expect: Meta(MARK) → Cmp(==5007) → Counter
	var hasMeta, hasCmpEq, hasCounter bool
	for _, e := range rule.Exprs {
		switch v := e.(type) {
		case *expr.Meta:
			hasMeta = v.Key == expr.MetaKeyMARK
		case *expr.Cmp:
			hasCmpEq = v.Op == expr.CmpOpEq
		case *expr.Counter:
			hasCounter = true
		}
	}
	assert.True(t, hasMeta, "rule must contain Meta(MARK)")
	assert.True(t, hasCmpEq, "rule must contain Cmp(==)")
	assert.True(t, hasCounter, "rule must contain Counter")
}
