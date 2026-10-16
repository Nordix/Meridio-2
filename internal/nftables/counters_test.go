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
	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
	"github.com/stretchr/testify/assert"
)

// rangeRule builds a per-DG range-match counter rule like ReconcileDGCounters installs:
// meta mark >= base, meta mark < end, counter(packets,bytes).
func rangeRule(base, end uint32, packets, bytes uint64) *nftables.Rule {
	return &nftables.Rule{
		Exprs: []expr.Any{
			&expr.Meta{Key: expr.MetaKeyMARK, Register: 1},
			&expr.Cmp{Op: expr.CmpOpGte, Register: 1, Data: binaryutil.NativeEndian.PutUint32(base)},
			&expr.Meta{Key: expr.MetaKeyMARK, Register: 1},
			&expr.Cmp{Op: expr.CmpOpLt, Register: 1, Data: binaryutil.NativeEndian.PutUint32(end)},
			&expr.Counter{Packets: packets, Bytes: bytes},
		},
	}
}

func TestRuleMarkLowerBound(t *testing.T) {
	mark, ok := ruleMarkLowerBound(rangeRule(5002, 5034, 0, 0))
	assert.True(t, ok)
	assert.Equal(t, uint32(5002), mark, "lower bound is the first 4-byte Cmp (>= base)")

	_, ok = ruleMarkLowerBound(&nftables.Rule{Exprs: []expr.Any{&expr.Counter{}}})
	assert.False(t, ok, "a rule with no mark Cmp is not recognized")
}

func TestRuleCounter(t *testing.T) {
	c := ruleCounter(rangeRule(5002, 5034, 7, 420))
	assert.NotNil(t, c)
	assert.Equal(t, uint64(7), c.Packets)
	assert.Equal(t, uint64(420), c.Bytes)

	assert.Nil(t, ruleCounter(&nftables.Rule{Exprs: []expr.Any{&expr.Meta{Key: expr.MetaKeyMARK}}}))
}

func TestDGNameForMark(t *testing.T) {
	m := &Manager{}
	m.SetDGRange("dg-a", 5002, 32) // [5002, 5034)
	m.SetDGRange("dg-b", 5034, 32) // [5034, 5066) — contiguous neighbor

	// Boundary cases pin the exclusive-upper range contract (R4: no cross-counting).
	cases := []struct {
		mark uint32
		want string
		ok   bool
	}{
		{5002, "dg-a", true}, // base, inclusive
		{5033, "dg-a", true}, // last of dg-a
		{5034, "dg-b", true}, // dg-b base — NOT dg-a (exclusive upper)
		{5065, "dg-b", true}, // last of dg-b
		{5066, "", false},    // past dg-b
		{5001, "", false},    // below dg-a (e.g. a drop fwmark)
	}
	for _, tc := range cases {
		got, ok := m.dgNameForMark(tc.mark)
		assert.Equal(t, tc.ok, ok, "mark=%d", tc.mark)
		assert.Equal(t, tc.want, got, "mark=%d", tc.mark)
	}

	m.ClearDGRange("dg-a")
	_, ok := m.dgNameForMark(5002)
	assert.False(t, ok, "cleared DG no longer matches")
}

// ReconcileDGCounters rejects the not-yet-implemented per-target path.
func TestIsExactMarkRule(t *testing.T) {
	exact := &nftables.Rule{
		Exprs: []expr.Any{
			&expr.Meta{Key: expr.MetaKeyMARK, Register: 1},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: binaryutil.NativeEndian.PutUint32(5002)},
			&expr.Counter{Packets: 10, Bytes: 200},
		},
	}
	assert.True(t, isExactMarkRule(exact), "single CmpOpEq should be exact")

	rangeRule := rangeRule(5002, 5034, 100, 2000)
	assert.False(t, isExactMarkRule(rangeRule), "range rule (Gte+Lt) should not be exact")

	noExprs := &nftables.Rule{Exprs: []expr.Any{&expr.Counter{Packets: 1}}}
	assert.False(t, isExactMarkRule(noExprs), "rule without Cmp should not be exact")
}

func TestDGForMark(t *testing.T) {
	m := &Manager{
		dgRanges: map[string]dgRange{
			"dg-a": {base: 5002, count: 32},
			"dg-b": {base: 5034, count: 16},
		},
	}
	name, base, found := m.dgForMark(5010)
	assert.True(t, found)
	assert.Equal(t, "dg-a", name)
	assert.Equal(t, 5002, base)

	name, base, found = m.dgForMark(5034)
	assert.True(t, found)
	assert.Equal(t, "dg-b", name)
	assert.Equal(t, 5034, base)

	_, _, found = m.dgForMark(9999)
	assert.False(t, found, "mark outside all ranges")
}

func TestIsRangeRule(t *testing.T) {
	// Exact desired range rule.
	assert.True(t, isRangeRule(rangeRule(5002, 5034, 0, 0), 5002, 5034),
		"Gte base + Lt end should match the desired range")

	// A different base (another DG's range) still matches its own bounds.
	assert.True(t, isRangeRule(rangeRule(5034, 5050, 0, 0), 5034, 5050),
		"range rule for a second DG matches its own bounds")

	// Wrong bounds.
	assert.False(t, isRangeRule(rangeRule(5002, 5034, 0, 0), 5002, 5099),
		"different end must not match")
	assert.False(t, isRangeRule(rangeRule(5002, 5034, 0, 0), 5000, 5034),
		"different base must not match")

	// An exact per-target rule (single CmpOpEq) is not a range rule.
	exact := &nftables.Rule{
		Exprs: []expr.Any{
			&expr.Meta{Key: expr.MetaKeyMARK, Register: 1},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: binaryutil.NativeEndian.PutUint32(5002)},
			&expr.Counter{},
		},
	}
	assert.False(t, isRangeRule(exact, 5002, 5034), "exact-mark rule is not a range rule")

	// Defensive branches: a Cmp with non-4-byte data, and an unexpected comparison op.
	badWidth := &nftables.Rule{
		Exprs: []expr.Any{&expr.Cmp{Op: expr.CmpOpGte, Register: 1, Data: []byte{0x1, 0x2}}},
	}
	assert.False(t, isRangeRule(badWidth, 5002, 5034), "non-4-byte Cmp data must reject")

	unexpectedOp := &nftables.Rule{
		Exprs: []expr.Any{
			&expr.Cmp{Op: expr.CmpOpNeq, Register: 1, Data: binaryutil.NativeEndian.PutUint32(5002)},
		},
	}
	assert.False(t, isRangeRule(unexpectedOp, 5002, 5034), "non Gte/Lt comparison op must reject")
}
