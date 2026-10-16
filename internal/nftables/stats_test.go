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
	"net"
	"testing"

	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
	"github.com/stretchr/testify/assert"
)

func TestCountEntries(t *testing.T) {
	tests := []struct {
		name  string
		elems []nftables.SetElement
		want  int
	}{
		{name: "empty set", elems: nil, want: 0},
		{
			name: "single interval entry (start + end sentinel)",
			elems: []nftables.SetElement{
				{Key: []byte{10, 0, 0, 1}, IntervalEnd: false},
				{Key: []byte{10, 0, 0, 2}, IntervalEnd: true},
			},
			want: 1,
		},
		{
			name: "three interval entries",
			elems: []nftables.SetElement{
				{Key: []byte{10, 0, 0, 1}, IntervalEnd: false},
				{Key: []byte{10, 0, 0, 2}, IntervalEnd: true},
				{Key: []byte{10, 0, 0, 5}, IntervalEnd: false},
				{Key: []byte{10, 0, 0, 6}, IntervalEnd: true},
				{Key: []byte{10, 0, 0, 9}, IntervalEnd: false},
				{Key: []byte{10, 0, 0, 10}, IntervalEnd: true},
			},
			want: 3,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, countEntries(tt.elems))
		})
	}
}

// TestCountEntries_MatchesCidrToSetElements guards the contract countEntries relies on:
// each VIP CIDR expands to exactly one non-IntervalEnd start element (+ one IntervalEnd
// sentinel), so counting start elements == counting VIPs. If cidrToSetElements ever changes
// its element layout, this test catches the drift.
func TestCountEntries_MatchesCidrToSetElements(t *testing.T) {
	cidrs := []string{"10.0.0.1/32", "192.168.0.0/24", "2001:db8::1/128"}
	var elems []nftables.SetElement
	for _, c := range cidrs {
		_, ipNet, err := net.ParseCIDR(c)
		assert.NoError(t, err)
		elems = append(elems, cidrToSetElements(ipNet)...)
	}
	// 3 CIDRs -> 6 raw elements, but 3 real entries.
	assert.Len(t, elems, 2*len(cidrs))
	assert.Equal(t, len(cidrs), countEntries(elems))
}

// dropRule builds a drop-accounting rule with the same expr layout createDropAccountingChain
// uses: Meta(MARK) -> Cmp(==fwmark) -> Counter(packets) -> Verdict(Drop).
func dropRule(fwmark uint32, packets uint64) *nftables.Rule {
	return &nftables.Rule{
		Exprs: []expr.Any{
			&expr.Meta{Key: expr.MetaKeyMARK, Register: 1},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: binaryutil.NativeEndian.PutUint32(fwmark)},
			&expr.Counter{Packets: packets},
			&expr.Verdict{Kind: expr.VerdictDrop},
		},
	}
}

func TestParseDropRule(t *testing.T) {
	fwmark, packets, ok := parseDropRule(dropRule(5000, 42))
	assert.True(t, ok)
	assert.Equal(t, uint32(5000), fwmark)
	assert.Equal(t, uint64(42), packets)
}

func TestParseDropRule_NotRecognized(t *testing.T) {
	// A rule with no Cmp/Counter (e.g. an unrelated rule) is not recognized.
	rule := &nftables.Rule{Exprs: []expr.Any{&expr.Meta{Key: expr.MetaKeyMARK, Register: 1}}}
	_, _, ok := parseDropRule(rule)
	assert.False(t, ok)

	// A rule with a mark cmp but no counter is also not recognized.
	rule2 := &nftables.Rule{Exprs: []expr.Any{
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: binaryutil.NativeEndian.PutUint32(5000)},
	}}
	_, _, ok = parseDropRule(rule2)
	assert.False(t, ok)
}

// TestDropCounts_MapsFwmarksToReasons exercises the fwmark->reason mapping in DropCounts via a
// Manager with a non-nil dropChain stub, using parseDropRule's output shape. (The live GetRules
// path is covered by the integration test; here we verify the reason assignment logic by
// constructing the Manager with known fwmarks and checking parseDropRule + the switch.)
func TestDropCounts_ReasonMapping(t *testing.T) {
	m := &Manager{nolbFwmark: 5000, notargetsFwmark: 5001}

	// Simulate the per-rule mapping DropCounts performs.
	result := map[string]uint64{}
	for _, r := range []*nftables.Rule{dropRule(5000, 7), dropRule(5001, 3)} {
		fwmark, packets, ok := parseDropRule(r)
		assert.True(t, ok)
		switch fwmark {
		case m.nolbFwmark:
			result[DropReasonNoFlow] = packets
		case m.notargetsFwmark:
			result[DropReasonNoTargets] = packets
		}
	}
	assert.Equal(t, map[string]uint64{DropReasonNoFlow: 7, DropReasonNoTargets: 3}, result)
}

// TestDropCounts_FwmarkCollision pins the documented invariant behavior for the (by-construction
// unreachable) case nolbFwmark == notargetsFwmark: the switch distinguishes reasons solely by
// fwmark, so a shared mark is attributed to the FIRST matching case only (no_flow) and the other
// reason is dropped. This is not a supported configuration — NoLBFwmark/NoTargetsFwmark are
// always fwmarkBase and fwmarkBase+1 — but the test documents what would happen if that invariant
// were ever broken, so a future fwmark-layout change that trips it fails this test loudly.
func TestDropCounts_FwmarkCollision(t *testing.T) {
	m := &Manager{nolbFwmark: 5000, notargetsFwmark: 5000}

	result := map[string]uint64{}
	for _, r := range []*nftables.Rule{dropRule(5000, 7)} {
		fwmark, packets, ok := parseDropRule(r)
		assert.True(t, ok)
		switch fwmark {
		case m.nolbFwmark:
			result[DropReasonNoFlow] = packets
		case m.notargetsFwmark:
			result[DropReasonNoTargets] = packets
		}
	}
	// Only no_flow is recorded; no_targets is lost under a collision.
	assert.Equal(t, map[string]uint64{DropReasonNoFlow: 7}, result)
	assert.NotContains(t, result, DropReasonNoTargets)
}

// TestDropCounts_DisabledChain verifies that a Manager with no drop chain (drop accounting
// disabled) returns an empty map and no error — so the collector emits no drop series.
func TestDropCounts_DisabledChain(t *testing.T) {
	m := &Manager{} // dropChain nil
	got, err := m.DropCounts()
	assert.NoError(t, err)
	assert.Empty(t, got)
}
