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
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestNewManager(t *testing.T) {
	mgr, err := NewManager(0, 4)
	assert.NoError(t, err)
	assert.NotNil(t, mgr)
	assert.Equal(t, "meridio-lb", mgr.tableName)
}

func TestNewManagerWithExcludedPrefixes(t *testing.T) {
	mgr, err := NewManager(0, 4, "net1", "net2")
	assert.NoError(t, err)
	assert.Equal(t, []string{"net1", "net2"}, mgr.excludedIfPrefixes)
}

func TestExtractVIPs(t *testing.T) {
	tests := []struct {
		name     string
		cidrs    []string
		expected []string
	}{
		{
			name:     "single IPv4",
			cidrs:    []string{"192.168.1.1/32"},
			expected: []string{"192.168.1.1/32"},
		},
		{
			name:     "single IPv6",
			cidrs:    []string{"2001:db8::1/128"},
			expected: []string{"2001:db8::1/128"},
		},
		{
			name:     "mixed IPv4 and IPv6",
			cidrs:    []string{"192.168.1.1/32", "2001:db8::1/128"},
			expected: []string{"192.168.1.1/32", "2001:db8::1/128"},
		},
		{
			name:     "duplicates removed",
			cidrs:    []string{"192.168.1.1/32", "192.168.1.1/32"},
			expected: []string{"192.168.1.1/32"},
		},
		{
			name:     "empty list",
			cidrs:    []string{},
			expected: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := deduplicateVIPs(tt.cidrs)
			assert.ElementsMatch(t, tt.expected, result)
		})
	}
}

func TestSplitIPv4AndIPv6(t *testing.T) {
	tests := []struct {
		name         string
		cidrs        []string
		expectedIPv4 []string
		expectedIPv6 []string
	}{
		{
			name:         "only IPv4",
			cidrs:        []string{"192.168.1.1/32", "10.0.0.1/32"},
			expectedIPv4: []string{"192.168.1.1/32", "10.0.0.1/32"},
			expectedIPv6: []string{},
		},
		{
			name:         "only IPv6",
			cidrs:        []string{"2001:db8::1/128", "2001:db8::2/128"},
			expectedIPv4: []string{},
			expectedIPv6: []string{"2001:db8::1/128", "2001:db8::2/128"},
		},
		{
			name:         "mixed",
			cidrs:        []string{"192.168.1.1/32", "2001:db8::1/128"},
			expectedIPv4: []string{"192.168.1.1/32"},
			expectedIPv6: []string{"2001:db8::1/128"},
		},
		{
			name:         "empty",
			cidrs:        []string{},
			expectedIPv4: []string{},
			expectedIPv6: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ipv4, ipv6 := splitIPv4AndIPv6(tt.cidrs)
			assert.ElementsMatch(t, tt.expectedIPv4, ipv4)
			assert.ElementsMatch(t, tt.expectedIPv6, ipv6)
		})
	}
}

// --- PMTU expression structure tests ---

func dummySet(name string) *nftables.Set {
	return &nftables.Set{Name: name, ID: 1}
}

func TestBuildPMTUDIPv4Exprs(t *testing.T) {
	set := dummySet("ipv4-vips")
	exprs := buildPMTUDIPv4Exprs(set)

	require.Len(t, exprs, 21, "IPv4 PMTU rule should have 21 expressions")

	// Match IPv4
	assertMeta(t, exprs[0], expr.MetaKeyNFPROTO)
	assertCmpEq(t, exprs[1], []byte{unix.AF_INET})

	// Match ICMP
	assertMeta(t, exprs[2], expr.MetaKeyL4PROTO)
	assertCmpEq(t, exprs[3], []byte{unix.IPPROTO_ICMP})

	// Non-zero fwmark
	assertMeta(t, exprs[4], expr.MetaKeyMARK)
	assertCmpNeq(t, exprs[5], []byte{0x0})

	// dst NOT in VIP set (IP dst at offset 16)
	assertPayloadLoad(t, exprs[6], expr.PayloadBaseNetworkHeader, 16, 4)
	assertLookupInvert(t, exprs[7], "ipv4-vips", true)

	// src NOT in VIP set (IP src at offset 12)
	assertPayloadLoad(t, exprs[8], expr.PayloadBaseNetworkHeader, 12, 4)
	assertLookupInvert(t, exprs[9], "ipv4-vips", true)

	// ICMP type == 3 (Destination Unreachable)
	assertPayloadLoad(t, exprs[10], expr.PayloadBaseTransportHeader, 0, 1)
	assertCmpEq(t, exprs[11], []byte{0x3})

	// ICMP code == 4 (Fragmentation Needed)
	assertPayloadLoad(t, exprs[12], expr.PayloadBaseTransportHeader, 1, 1)
	assertCmpEq(t, exprs[13], []byte{0x4})

	// Encapsulated dst IS VIP (transport header offset 24)
	assertPayloadLoad(t, exprs[14], expr.PayloadBaseTransportHeader, 24, 4)
	assertLookupInvert(t, exprs[15], "ipv4-vips", false)

	// Counter
	_, ok := exprs[16].(*expr.Counter)
	assert.True(t, ok, "expr[16] should be Counter")

	// Load encapsulated dst for write
	assertPayloadLoad(t, exprs[17], expr.PayloadBaseTransportHeader, 24, 4)

	// Payload write: src address at offset 12, checksum at offset 10
	pw, ok := exprs[18].(*expr.Payload)
	require.True(t, ok, "expr[18] should be Payload write")
	assert.Equal(t, expr.PayloadWrite, pw.OperationType)
	assert.Equal(t, uint32(12), pw.Offset, "should write to IP source offset")
	assert.Equal(t, uint32(4), pw.Len)
	assert.Equal(t, expr.CsumTypeInet, pw.CsumType)
	assert.Equal(t, uint32(10), pw.CsumOffset, "IP checksum at offset 10")
	assert.Equal(t, uint32(unix.NFT_PAYLOAD_L4CSUM_PSEUDOHDR), pw.CsumFlags)

	// Mark reset to 0
	imm, ok := exprs[19].(*expr.Immediate)
	require.True(t, ok, "expr[19] should be Immediate")
	assert.Equal(t, binaryutil.NativeEndian.PutUint32(0), imm.Data)

	meta, ok := exprs[20].(*expr.Meta)
	require.True(t, ok, "expr[20] should be Meta")
	assert.Equal(t, expr.MetaKeyMARK, meta.Key)
	assert.True(t, meta.SourceRegister)
}

func TestBuildPMTUDIPv6Exprs(t *testing.T) {
	set := dummySet("ipv6-vips")
	exprs := buildPMTUDIPv6Exprs(set)

	require.Len(t, exprs, 19, "IPv6 PMTU rule should have 19 expressions")

	// Match IPv6
	assertMeta(t, exprs[0], expr.MetaKeyNFPROTO)
	assertCmpEq(t, exprs[1], []byte{unix.AF_INET6})

	// Match ICMPv6
	assertMeta(t, exprs[2], expr.MetaKeyL4PROTO)
	assertCmpEq(t, exprs[3], []byte{unix.IPPROTO_ICMPV6})

	// Non-zero fwmark
	assertMeta(t, exprs[4], expr.MetaKeyMARK)
	assertCmpNeq(t, exprs[5], []byte{0x0})

	// dst NOT in VIP set (IPv6 dst at offset 24, 16 bytes)
	assertPayloadLoad(t, exprs[6], expr.PayloadBaseNetworkHeader, 24, 16)
	assertLookupInvert(t, exprs[7], "ipv6-vips", true)

	// src NOT in VIP set (IPv6 src at offset 8, 16 bytes)
	assertPayloadLoad(t, exprs[8], expr.PayloadBaseNetworkHeader, 8, 16)
	assertLookupInvert(t, exprs[9], "ipv6-vips", true)

	// ICMPv6 type == 2 (Packet Too Big)
	assertPayloadLoad(t, exprs[10], expr.PayloadBaseTransportHeader, 0, 1)
	assertCmpEq(t, exprs[11], []byte{0x2})

	// Encapsulated dst IS VIP (transport header offset 32, 16 bytes)
	assertPayloadLoad(t, exprs[12], expr.PayloadBaseTransportHeader, 32, 16)
	assertLookupInvert(t, exprs[13], "ipv6-vips", false)

	// Counter
	_, ok := exprs[14].(*expr.Counter)
	assert.True(t, ok, "expr[14] should be Counter")

	// Load encapsulated dst for write
	assertPayloadLoad(t, exprs[15], expr.PayloadBaseTransportHeader, 32, 16)

	// Payload write: IPv6 src at offset 8, no checksum (IPv6 has none)
	pw, ok := exprs[16].(*expr.Payload)
	require.True(t, ok, "expr[16] should be Payload write")
	assert.Equal(t, expr.PayloadWrite, pw.OperationType)
	assert.Equal(t, uint32(8), pw.Offset, "should write to IPv6 source offset")
	assert.Equal(t, uint32(16), pw.Len)
	assert.Equal(t, expr.CsumTypeNone, pw.CsumType, "IPv6 has no header checksum")

	// Mark reset to 0
	imm, ok := exprs[17].(*expr.Immediate)
	require.True(t, ok, "expr[17] should be Immediate")
	assert.Equal(t, binaryutil.NativeEndian.PutUint32(0), imm.Data)

	meta, ok := exprs[18].(*expr.Meta)
	require.True(t, ok, "expr[18] should be Meta")
	assert.Equal(t, expr.MetaKeyMARK, meta.Key)
	assert.True(t, meta.SourceRegister)
}

// --- Test helpers ---

func assertMeta(t *testing.T, e expr.Any, key expr.MetaKey) {
	t.Helper()
	m, ok := e.(*expr.Meta)
	require.True(t, ok, "expected Meta expression")
	assert.Equal(t, key, m.Key)
}

func assertCmpEq(t *testing.T, e expr.Any, data []byte) {
	t.Helper()
	c, ok := e.(*expr.Cmp)
	require.True(t, ok, "expected Cmp expression")
	assert.Equal(t, expr.CmpOpEq, c.Op)
	assert.Equal(t, data, c.Data)
}

func assertCmpNeq(t *testing.T, e expr.Any, data []byte) {
	t.Helper()
	c, ok := e.(*expr.Cmp)
	require.True(t, ok, "expected Cmp expression")
	assert.Equal(t, expr.CmpOpNeq, c.Op)
	assert.Equal(t, data, c.Data)
}

func assertPayloadLoad(t *testing.T, e expr.Any, base expr.PayloadBase, offset, length uint32) {
	t.Helper()
	p, ok := e.(*expr.Payload)
	require.True(t, ok, "expected Payload expression")
	assert.Equal(t, base, p.Base)
	assert.Equal(t, offset, p.Offset)
	assert.Equal(t, length, p.Len)
}

func assertLookupInvert(t *testing.T, e expr.Any, setName string, invert bool) {
	t.Helper()
	l, ok := e.(*expr.Lookup)
	require.True(t, ok, "expected Lookup expression")
	assert.Equal(t, setName, l.SetName)
	assert.Equal(t, invert, l.Invert)
}
