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
	"fmt"
	"strconv"

	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
)

// Counts holds a forwarded packet/byte total read from an nftables counter.
type Counts struct {
	Packets uint64
	Bytes   uint64
}

// DGTarget identifies a per-target counter series.
type DGTarget struct {
	DG     string
	Target string
}

// DGCounters is the desired per-DistributionGroup accounting state.
//
// The DG owns a contiguous fwmark range [FwmarkBase, FwmarkBase+MaxTargets): the fwmark for
// target identifier i is FwmarkBase+i. nfqlb sets this fwmark on a forwarded packet, so counting
// packets by fwmark attributes them to a DG (by range) and to a target (by exact fwmark).
type DGCounters struct {
	DG         string           // DistributionGroup name (metric label value)
	FwmarkBase int              // fwmark of identifier 0 (= the nfqlb instance offset)
	MaxTargets int              // range is [FwmarkBase, FwmarkBase+MaxTargets)
	Targets    map[int]struct{} // set of live target identifiers; only used when perTarget is true
}

// createTargetAccountingChain creates the chain that counts forwarded VIP packets by fwmark.
//
// Like drop-accounting, it is a SEPARATE prerouting chain that runs on the packet RETURNING from
// nfqlb (NF_ACCEPT with a target fwmark set). Its priority is filter+2 — after both the main
// prerouting chain (filter) and drop-accounting (filter+1) — so drop-marked packets are already
// dropped and only real target fwmarks (>= fwmarkBase+2) reach it. The counter rules are
// non-terminal (no verdict): the packet continues to policy routing and is forwarded.
func (m *Manager) createTargetAccountingChain() error {
	m.targetChain = m.conn.AddChain(&nftables.Chain{
		Name:     targetAccountingChainName,
		Table:    m.table,
		Type:     nftables.ChainTypeFilter,
		Hooknum:  nftables.ChainHookPrerouting,
		Priority: nftables.ChainPriorityRef(*nftables.ChainPriorityFilter + 2),
	})
	return m.conn.Flush()
}

// ReconcileDGCounters makes the target-accounting chain's rules for ONE DistributionGroup match
// the desired state, within that DG's fwmark range only (so it never disturbs other DGs' rules).
//
//   - perTarget == false (default): install a single per-DG range-match counter rule
//     (meta mark >= base && meta mark < base+count counter). _lb_packets_total /
//     _lb_bytes_total read it.
//   - perTarget == true (opt-in): install one exact-fwmark counter rule per live target
//     (meta mark == base+id counter). _lb_target_packets_total reads these, and the per-DG
//     totals are derived by summing them. Only one rule-set is active at a time per DG.
//
// It is reconcile-style/idempotent: it converges the target-accounting chain to the desired rule
// set for this DG's range.
//
// Counter preservation: nftables counters live on the rule object, so a delete+re-add zeros them.
// Since reconcileTargets calls this on every target change, the per-DG path leaves an existing,
// identical range rule in place (its range depends only on FwmarkBase/MaxTargets, unchanged by
// churn) to keep _lb_packets_total / _lb_bytes_total stable. The per-target path rebuilds (its rule
// set tracks live targets), so churn-driven resets are inherent and accepted there.
func (m *Manager) ReconcileDGCounters(dg DGCounters, perTarget bool) error {
	if m.targetChain == nil {
		return fmt.Errorf("target-accounting chain not initialized (call Setup first)")
	}

	if perTarget {
		// Per-target mode (opt-in _lb_target_packets_total): one exact-fwmark counter rule per
		// live target. Per-DG totals (_lb_packets_total / _lb_bytes_total) are derived by SUMMING
		// these — no range rule is installed (one active rule-set at a time). Uses N exact-match
		// rules mirroring the drop-accounting pattern; a counter-map keyed by fwmark is a
		// documented future optimization (O(1) per packet vs O(N) linear rule eval).
		//
		// Rebuild unconditionally: the desired rule set tracks the live target set, so a diff would
		// be needed to preserve unchanged targets' counters — see the counter-map optimization. For
		// the opt-in path the churn-driven reset is accepted (document + rate()).
		if err := m.removeDGRules(dg.FwmarkBase, dg.MaxTargets); err != nil {
			return err
		}
		for id := range dg.Targets {
			fwmark := uint32(dg.FwmarkBase + id)
			m.conn.AddRule(&nftables.Rule{
				Table: m.table,
				Chain: m.targetChain,
				Exprs: []expr.Any{
					&expr.Meta{Key: expr.MetaKeyMARK, Register: 1},
					&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: binaryutil.NativeEndian.PutUint32(fwmark)},
					&expr.Counter{},
				},
			})
		}
		return m.conn.Flush()
	}

	// Per-DG mode (default): one range-match counter rule per DG.
	base := uint32(dg.FwmarkBase)
	end := uint32(dg.FwmarkBase + dg.MaxTargets) // exclusive upper bound

	// If this DG's exact range rule is already installed, leave it in place so its counter is
	// preserved across target churn. Other DGs' rules (and leftover per-target rules in this DG's
	// range after a mode switch) are handled below: we only skip when THIS DG's range already has
	// exactly its single range rule and nothing else in [base, end).
	existing, err := m.conn.GetRules(m.table, m.targetChain)
	if err != nil {
		return fmt.Errorf("failed to list target-accounting rules: %w", err)
	}
	inRange := 0
	rangeRuleFound := false
	for _, r := range existing {
		mark, ok := ruleMarkLowerBound(r)
		if !ok || mark < base || mark >= end {
			continue // not in this DG's range (belongs to another DG, or unparseable)
		}
		inRange++
		if isRangeRule(r, base, end) {
			rangeRuleFound = true
		}
	}
	if inRange == 1 && rangeRuleFound {
		// Exactly this DG's range rule is present (no stray per-target rules): nothing to do,
		// preserve its counter.
		return nil
	}

	if err := m.removeDGRules(dg.FwmarkBase, dg.MaxTargets); err != nil {
		return err
	}
	m.conn.AddRule(&nftables.Rule{
		Table: m.table,
		Chain: m.targetChain,
		Exprs: []expr.Any{
			// meta mark >= base
			&expr.Meta{Key: expr.MetaKeyMARK, Register: 1},
			&expr.Cmp{Op: expr.CmpOpGte, Register: 1, Data: binaryutil.NativeEndian.PutUint32(base)},
			// meta mark < base+count
			&expr.Meta{Key: expr.MetaKeyMARK, Register: 1},
			&expr.Cmp{Op: expr.CmpOpLt, Register: 1, Data: binaryutil.NativeEndian.PutUint32(end)},
			&expr.Counter{},
		},
	})
	return m.conn.Flush()
}

// RemoveDGCounters removes all target-accounting rules for a DG's fwmark range (used on DG
// deletion). Safe to call for a range with no rules.
func (m *Manager) RemoveDGCounters(_ string, base, count int) error {
	if m.targetChain == nil {
		return nil
	}
	if err := m.removeDGRules(base, count); err != nil {
		return err
	}
	return m.conn.Flush()
}

// removeDGRules deletes any rule in the target-accounting chain whose mark-match falls within
// [base, base+count). Does not flush (caller flushes).
func (m *Manager) removeDGRules(base, count int) error {
	rules, err := m.conn.GetRules(m.table, m.targetChain)
	if err != nil {
		return fmt.Errorf("failed to list target-accounting rules: %w", err)
	}
	lo, hi := uint32(base), uint32(base+count)
	for _, rule := range rules {
		mark, ok := ruleMarkLowerBound(rule)
		if ok && mark >= lo && mark < hi {
			if err := m.conn.DelRule(rule); err != nil {
				return fmt.Errorf("failed to delete target-accounting rule: %w", err)
			}
		}
	}
	return nil
}

// DGRange is a DistributionGroup's fwmark range, supplied to PacketByteCounts so the read can
// label counters by DG name. The caller (the controller) owns this mapping and its locking; the
// Manager keeps no DG<->range state of its own.
type DGRange struct {
	Base, Count int
}

// PacketByteCounts reads forwarded packet/byte totals from the target-accounting chain.
// Per-DG totals are keyed by DG name (always). perTarget, keyed by {DG, target-id}, is populated
// only when per-target (exact-fwmark) rules are installed; in per-DG (range-rule) mode it is empty.
//
// In per-target mode each rule matches one exact fwmark, so per-DG totals are the SUM of the DG's
// target counters. The fwmark->DG(+base) mapping is passed in by the caller (the controller knows
// DG<->range), so the Manager holds no mutable controller-derived state and this stays a pure
// kernel read; the target id is fwmark-base.
func (m *Manager) PacketByteCounts(ranges map[string]DGRange) (perDG map[string]Counts, perTarget map[DGTarget]Counts, err error) {
	perDG = map[string]Counts{}
	perTarget = map[DGTarget]Counts{}
	if m.targetChain == nil {
		return perDG, perTarget, nil
	}
	rules, err := m.conn.GetRules(m.table, m.targetChain)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read target-accounting rules: %w", err)
	}
	for _, rule := range rules {
		mark, ok := ruleMarkLowerBound(rule)
		if !ok {
			continue
		}
		c := ruleCounter(rule)
		if c == nil {
			continue
		}
		dg, base, found := dgForMark(ranges, mark)
		if !found {
			continue
		}
		// Sum into per-DG (works for both the single range rule and multiple per-target rules).
		agg := perDG[dg]
		agg.Packets += c.Packets
		agg.Bytes += c.Bytes
		perDG[dg] = agg

		// A per-target rule matches an exact fwmark ABOVE the DG base (base+id). The range rule's
		// lower-bound Cmp equals the base itself; treat only mark>base, or an exact match that is
		// not the range lower bound, as a per-target entry. Simpler and unambiguous: a per-target
		// rule has a single exact Cmp (==), the range rule has two (>=,<). Distinguish by op.
		if isExactMarkRule(rule) {
			id := int(mark) - base
			perTarget[DGTarget{DG: dg, Target: strconv.Itoa(id)}] = Counts{Packets: c.Packets, Bytes: c.Bytes}
		}
	}
	return perDG, perTarget, nil
}

// ruleMarkLowerBound returns the lower-bound fwmark a target-accounting rule matches (the first
// 4-byte Cmp, which for a range rule is the >= base compare). ok=false if the rule is not a
// recognized mark-matched rule.
func ruleMarkLowerBound(rule *nftables.Rule) (uint32, bool) {
	for _, e := range rule.Exprs {
		if cmp, ok := e.(*expr.Cmp); ok && len(cmp.Data) == 4 {
			return binaryutil.NativeEndian.Uint32(cmp.Data), true
		}
	}
	return 0, false
}

// ruleCounter returns the *expr.Counter in a rule, or nil.
func ruleCounter(rule *nftables.Rule) *expr.Counter {
	for _, e := range rule.Exprs {
		if c, ok := e.(*expr.Counter); ok {
			return c
		}
	}
	return nil
}

// dgForMark returns the DG name and its fwmark base whose range contains fwmark, if any, from the
// supplied range mapping. Pure helper (no Manager state).
func dgForMark(ranges map[string]DGRange, mark uint32) (name string, base int, found bool) {
	for n, r := range ranges {
		if mark >= uint32(r.Base) && mark < uint32(r.Base+r.Count) {
			return n, r.Base, true
		}
	}
	return "", 0, false
}

// isExactMarkRule reports whether a target-accounting rule matches a single exact fwmark
// (per-target rule: one CmpOpEq) rather than a range (per-DG rule: CmpOpGte + CmpOpLt).
func isExactMarkRule(rule *nftables.Rule) bool {
	cmps := 0
	exact := false
	for _, e := range rule.Exprs {
		if cmp, ok := e.(*expr.Cmp); ok {
			cmps++
			if cmp.Op == expr.CmpOpEq {
				exact = true
			}
		}
	}
	return cmps == 1 && exact
}

// isRangeRule reports whether rule is exactly a per-DG range-match rule for [base, end): a
// CmpOpGte against base and a CmpOpLt against end (two Cmps, no others). Used to detect an
// already-installed, unchanged range rule so ReconcileDGCounters can leave it (and its counter)
// in place across target churn.
func isRangeRule(rule *nftables.Rule, base, end uint32) bool {
	var gteOK, ltOK bool
	cmps := 0
	for _, e := range rule.Exprs {
		cmp, ok := e.(*expr.Cmp)
		if !ok {
			continue
		}
		cmps++
		if len(cmp.Data) != 4 {
			return false
		}
		val := binaryutil.NativeEndian.Uint32(cmp.Data)
		switch cmp.Op {
		case expr.CmpOpGte:
			if val == base {
				gteOK = true
			}
		case expr.CmpOpLt:
			if val == end {
				ltOK = true
			}
		default:
			return false // any other comparison op means it is not a plain range rule
		}
	}
	return cmps == 2 && gteOK && ltOK
}
