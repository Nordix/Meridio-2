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

	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
)

// Drop reason labels for the drop-accounting chain, matching the two drop rules
// createDropAccountingChain installs. These are the stable label values the LB metrics
// collector uses for <prefix>_lb_drops_total:
//   - no_flow: packet matched a VIP but no flow selector matched in nfqlb (nolbFwmark).
//   - no_targets: a flow matched but the instance had no active targets (notargetsFwmark).
const (
	DropReasonNoFlow    = "no_flow"
	DropReasonNoTargets = "no_targets"
)

// VIPSetSize returns the number of VIP entries currently in the IPv4 and IPv6 VIP sets,
// read fresh from the kernel. It is the read side of SetVIPs, used by the LB metrics
// collector to expose <prefix>_lb_nftables_vip_set_size.
//
// The VIP sets are interval sets: SetVIPs stores each VIP CIDR as TWO elements — a start
// element (IntervalEnd=false) and an end sentinel (IntervalEnd=true) — see cidrToSetElements.
// So the VIP count is the number of non-IntervalEnd elements, NOT len(elements).
//
// Returns zero counts (no error) before Setup has created the sets (ipv4Set/ipv6Set nil),
// so a not-yet-initialized manager reads as "0 VIPs" rather than failing a scrape.
func (m *Manager) VIPSetSize() (ipv4, ipv6 int, err error) {
	ipv4, err = countSetEntries(m.conn, m.ipv4Set)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to read IPv4 VIP set: %w", err)
	}
	ipv6, err = countSetEntries(m.conn, m.ipv6Set)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to read IPv6 VIP set: %w", err)
	}
	return ipv4, ipv6, nil
}

// DropCounts returns the per-reason dropped-packet counts from the drop-accounting chain,
// read fresh from the kernel. It is the read side of createDropAccountingChain, used by the LB
// metrics collector to expose <prefix>_lb_drops_total.
//
// The returned map is keyed by drop reason (DropReasonNoFlow / DropReasonNoTargets) and contains
// an entry only for a reason whose drop rule actually exists. When drop accounting is disabled
// (both fwmarks 0, the chain is never created) this returns an empty map (no error), so the
// collector emits no drop series for a disabled feature rather than a misleading 0.
//
// Each drop rule has the expr layout: Meta(MARK) -> Cmp(==fwmark) -> Counter -> Verdict(Drop).
// The rule's fwmark (from the Cmp data) maps it to a reason; the Counter gives the packet total.
func (m *Manager) DropCounts() (map[string]uint64, error) {
	result := map[string]uint64{}
	if m.dropChain == nil {
		return result, nil // drop accounting disabled: chain not created
	}

	rules, err := m.conn.GetRules(m.table, m.dropChain)
	if err != nil {
		return nil, fmt.Errorf("failed to read drop-accounting rules: %w", err)
	}

	for _, rule := range rules {
		fwmark, counter, ok := parseDropRule(rule)
		if !ok {
			continue // not a mark-matched counter rule we recognize
		}
		// Invariant: nolbFwmark != notargetsFwmark (they are fwmarkBase and fwmarkBase+1). The
		// switch distinguishes reasons solely by fwmark, so a future fwmark-layout change that
		// made them equal would silently attribute a shared mark to only one reason — keep them
		// distinct at the source (see NoLBFwmark/NoTargetsFwmark).
		switch fwmark {
		case m.nolbFwmark:
			result[DropReasonNoFlow] = counter
		case m.notargetsFwmark:
			result[DropReasonNoTargets] = counter
		}
	}
	return result, nil
}

// parseDropRule extracts the matched fwmark and the counter's packet total from a
// drop-accounting rule. Returns ok=false if the rule does not have the expected
// Cmp(mark)+Counter shape (e.g. an unrelated rule).
func parseDropRule(rule *nftables.Rule) (fwmark uint32, packets uint64, ok bool) {
	var haveMark, haveCounter bool
	for _, e := range rule.Exprs {
		switch ex := e.(type) {
		case *expr.Cmp:
			if ex.Op == expr.CmpOpEq && len(ex.Data) == 4 {
				fwmark = binaryutil.NativeEndian.Uint32(ex.Data)
				haveMark = true
			}
		case *expr.Counter:
			packets = ex.Packets
			haveCounter = true
		}
	}
	return fwmark, packets, haveMark && haveCounter
}

// countSetEntries returns the number of real entries (start elements) in an interval set,
// excluding the IntervalEnd sentinel that each entry pairs with. A nil set (not yet created
// by Setup) counts as 0.
func countSetEntries(conn *nftables.Conn, set *nftables.Set) (int, error) {
	if set == nil {
		return 0, nil
	}
	elems, err := conn.GetSetElements(set)
	if err != nil {
		return 0, err
	}
	return countEntries(elems), nil
}

// countEntries counts the real entries in an interval-set element slice, i.e. the start
// elements, excluding each entry's trailing IntervalEnd sentinel (see cidrToSetElements).
// Pure (no kernel) so it is unit-testable; the live GetSetElements path is covered by the
// integration test.
func countEntries(elems []nftables.SetElement) int {
	count := 0
	for _, e := range elems {
		if !e.IntervalEnd {
			count++
		}
	}
	return count
}
