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
	"net"
	"strings"

	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
	"golang.org/x/sys/unix"
)

const (
	preroutingChainName = "prerouting"
	outputChainName     = "output"
	pmtudChainName      = "snat-local"
	defragTableName     = "meridio-defrag"
	defragPreChainName  = "pre-defrag"
	defragInChainName   = "in"
	defragOutChainName  = "out"
	ipv4VIPSetName      = "ipv4-vips"
	ipv6VIPSetName      = "ipv6-vips"
)

// Manager manages nftables rules for VIP traffic.
type Manager struct {
	tableName          string
	queueNum           uint16
	queueTotal         uint16
	table              *nftables.Table
	preChain           *nftables.Chain
	outputChain        *nftables.Chain
	pmtudChain         *nftables.Chain
	defragTable        *nftables.Table
	defragPreChain     *nftables.Chain
	defragInChain      *nftables.Chain
	defragOutChain     *nftables.Chain
	ipv4Set            *nftables.Set
	ipv6Set            *nftables.Set
	ipv4PreRule        *nftables.Rule
	ipv6PreRule        *nftables.Rule
	ipv4OutRule        *nftables.Rule
	ipv6OutRule        *nftables.Rule
	excludedIfPrefixes []string
	conn               *nftables.Conn
}

const sharedTableName = "meridio-lb" // Shared table for all DistributionGroups

// NewManager creates a new nftables manager.
// Uses a single shared table for all DistributionGroups.
// excludedIfPrefixes are interface name prefixes for which defragmentation is skipped
// (target-facing interfaces, to preserve PMTU information in outbound packets).
func NewManager(queueNum, queueTotal uint16, excludedIfPrefixes ...string) (*Manager, error) {
	return &Manager{
		tableName:          sharedTableName,
		queueNum:           queueNum,
		queueTotal:         queueTotal,
		excludedIfPrefixes: excludedIfPrefixes,
		conn:               &nftables.Conn{},
	}, nil
}

// Setup creates the nftables table, chains, and sets.
func (m *Manager) Setup() error {
	if err := m.createTable(); err != nil {
		return err
	}
	if err := m.createSets(); err != nil {
		return err
	}
	if err := m.createPreroutingChain(); err != nil {
		return err
	}
	if err := m.createOutputChain(); err != nil {
		return err
	}
	if err := m.createPMTUDChain(); err != nil {
		return err
	}
	if err := m.createDefragTable(); err != nil {
		return err
	}
	return nil
}

// SetVIPs updates the VIP sets with the given CIDRs.
func (m *Manager) SetVIPs(cidrs []string) error {
	vips := deduplicateVIPs(cidrs)
	ipv4, ipv6 := splitIPv4AndIPv6(vips)

	if err := m.updateSet(m.ipv4Set, ipv4); err != nil {
		return fmt.Errorf("failed to update IPv4 VIPs: %w", err)
	}
	if err := m.updateSet(m.ipv6Set, ipv6); err != nil {
		return fmt.Errorf("failed to update IPv6 VIPs: %w", err)
	}
	return nil
}

// Cleanup removes the nftables table and all associated rules.
func (m *Manager) Cleanup() error {
	m.conn.DelTable(m.table)
	if m.defragTable != nil {
		m.conn.DelTable(m.defragTable)
	}
	return m.conn.Flush()
}

func (m *Manager) createTable() error {
	m.table = m.conn.AddTable(&nftables.Table{
		Name:   m.tableName,
		Family: nftables.TableFamilyINet,
	})
	return m.conn.Flush()
}

func (m *Manager) createSets() error {
	m.ipv4Set = &nftables.Set{
		Table:    m.table,
		Name:     ipv4VIPSetName,
		KeyType:  nftables.TypeIPAddr,
		Interval: true,
	}
	if err := m.conn.AddSet(m.ipv4Set, []nftables.SetElement{}); err != nil {
		return fmt.Errorf("failed to create IPv4 set: %w", err)
	}

	m.ipv6Set = &nftables.Set{
		Table:    m.table,
		Name:     ipv6VIPSetName,
		KeyType:  nftables.TypeIP6Addr,
		Interval: true,
	}
	if err := m.conn.AddSet(m.ipv6Set, []nftables.SetElement{}); err != nil {
		return fmt.Errorf("failed to create IPv6 set: %w", err)
	}

	return m.conn.Flush()
}

func (m *Manager) createPreroutingChain() error {
	m.preChain = m.conn.AddChain(&nftables.Chain{
		Name:     preroutingChainName,
		Table:    m.table,
		Type:     nftables.ChainTypeFilter,
		Hooknum:  nftables.ChainHookPrerouting,
		Priority: nftables.ChainPriorityFilter,
	})

	// IPv4 rule: ip daddr @ipv4-vips counter queue num X-Y fanout
	m.ipv4PreRule = m.conn.AddRule(&nftables.Rule{
		Table: m.table,
		Chain: m.preChain,
		Exprs: []expr.Any{
			&expr.Meta{Key: expr.MetaKeyNFPROTO, Register: 1},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{unix.AF_INET}},
			&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 16, Len: 4},
			&expr.Lookup{SourceRegister: 1, SetName: m.ipv4Set.Name, SetID: m.ipv4Set.ID},
			&expr.Counter{},
			&expr.Queue{Num: m.queueNum, Total: m.queueTotal, Flag: 0},
		},
	})

	// IPv6 rule: ip6 daddr @ipv6-vips counter queue num X-Y fanout
	m.ipv6PreRule = m.conn.AddRule(&nftables.Rule{
		Table: m.table,
		Chain: m.preChain,
		Exprs: []expr.Any{
			&expr.Meta{Key: expr.MetaKeyNFPROTO, Register: 1},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{unix.AF_INET6}},
			&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 24, Len: 16},
			&expr.Lookup{SourceRegister: 1, SetName: m.ipv6Set.Name, SetID: m.ipv6Set.ID},
			&expr.Counter{},
			&expr.Queue{Num: m.queueNum, Total: m.queueTotal, Flag: 0},
		},
	})

	return m.conn.Flush()
}

func (m *Manager) createOutputChain() error {
	m.outputChain = m.conn.AddChain(&nftables.Chain{
		Name:     outputChainName,
		Table:    m.table,
		Type:     nftables.ChainTypeFilter,
		Hooknum:  nftables.ChainHookOutput,
		Priority: nftables.ChainPriorityFilter,
	})

	// IPv4 rule: meta l4proto icmp ip daddr @ipv4-vips counter queue num X-Y fanout
	m.ipv4OutRule = m.conn.AddRule(&nftables.Rule{
		Table: m.table,
		Chain: m.outputChain,
		Exprs: []expr.Any{
			&expr.Meta{Key: expr.MetaKeyNFPROTO, Register: 1},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{unix.AF_INET}},
			&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{unix.IPPROTO_ICMP}},
			&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 16, Len: 4},
			&expr.Lookup{SourceRegister: 1, SetName: m.ipv4Set.Name, SetID: m.ipv4Set.ID},
			&expr.Counter{},
			&expr.Queue{Num: m.queueNum, Total: m.queueTotal, Flag: 0},
		},
	})

	// IPv6 rule: meta l4proto icmpv6 ip6 daddr @ipv6-vips counter queue num X-Y fanout
	m.ipv6OutRule = m.conn.AddRule(&nftables.Rule{
		Table: m.table,
		Chain: m.outputChain,
		Exprs: []expr.Any{
			&expr.Meta{Key: expr.MetaKeyNFPROTO, Register: 1},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{unix.AF_INET6}},
			&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{unix.IPPROTO_ICMPV6}},
			&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 24, Len: 16},
			&expr.Lookup{SourceRegister: 1, SetName: m.ipv6Set.Name, SetID: m.ipv6Set.ID},
			&expr.Counter{},
			&expr.Queue{Num: m.queueNum, Total: m.queueTotal, Flag: 0},
		},
	})

	return m.conn.Flush()
}

// createPMTUDChain creates the PMTU discovery chain.
// Rewrites source address of locally generated ICMP Frag Needed / ICMPv6 Packet Too Big
// replies to use the VIP from the encapsulated original packet. This ensures external
// clients receive PMTU feedback with the correct source (VIP, not LB pod IP).
//
// Chain type is "route" (not "filter") so the kernel re-evaluates routing after rewrite.
// Requires net.ipv4.fwmark_reflect=1 and net.ipv6.fwmark_reflect=1 sysctls.
func (m *Manager) createPMTUDChain() error {
	m.pmtudChain = m.conn.AddChain(&nftables.Chain{
		Name:     pmtudChainName,
		Table:    m.table,
		Type:     nftables.ChainTypeRoute,
		Hooknum:  nftables.ChainHookOutput,
		Priority: nftables.ChainPriorityRaw,
	})

	// IPv4: ICMP Dest Unreachable / Frag Needed → rewrite src to VIP from encapsulated packet
	m.conn.AddRule(&nftables.Rule{
		Table: m.table,
		Chain: m.pmtudChain,
		Exprs: buildPMTUDIPv4Exprs(m.ipv4Set),
	})

	// IPv6: ICMPv6 Packet Too Big → rewrite src to VIP from encapsulated packet
	m.conn.AddRule(&nftables.Rule{
		Table: m.table,
		Chain: m.pmtudChain,
		Exprs: buildPMTUDIPv6Exprs(m.ipv6Set),
	})

	return m.conn.Flush()
}

// buildPMTUDIPv4Exprs builds nftables expressions for IPv4 PMTU SNAT.
// Matches: ICMP type 3 (Dest Unreachable) code 4 (Frag Needed), mark != 0,
// dst NOT VIP, src NOT VIP, encapsulated dst IS VIP.
// Action: rewrite IP src to encapsulated dst (the VIP), reset mark to 0.
func buildPMTUDIPv4Exprs(ipv4Set *nftables.Set) []expr.Any {
	return []expr.Any{
		// Match IPv4
		&expr.Meta{Key: expr.MetaKeyNFPROTO, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{unix.AF_INET}},
		// Match ICMP
		&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{unix.IPPROTO_ICMP}},
		// Non-zero fwmark (packet was processed by nfqlb)
		&expr.Meta{Key: expr.MetaKeyMARK, Register: 1},
		&expr.Cmp{Op: expr.CmpOpNeq, Register: 1, Data: []byte{0x0}},
		// dst NOT in VIP set (avoid mangling ICMP destined to VIPs)
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 16, Len: 4},
		&expr.Lookup{SourceRegister: 1, SetName: ipv4Set.Name, SetID: ipv4Set.ID, Invert: true},
		// src NOT in VIP set (skip if source is already a VIP)
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 12, Len: 4},
		&expr.Lookup{SourceRegister: 1, SetName: ipv4Set.Name, SetID: ipv4Set.ID, Invert: true},
		// ICMP type == 3 (Destination Unreachable)
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseTransportHeader, Offset: 0, Len: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{0x3}},
		// ICMP code == 4 (Fragmentation Needed)
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseTransportHeader, Offset: 1, Len: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{0x4}},
		// Encapsulated dst (offset 24 in ICMP payload) IS a VIP
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseTransportHeader, Offset: 24, Len: 4},
		&expr.Lookup{SourceRegister: 1, SetName: ipv4Set.Name, SetID: ipv4Set.ID},
		// Counter
		&expr.Counter{},
		// Load encapsulated dst again → write to IP source with checksum update
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseTransportHeader, Offset: 24, Len: 4},
		&expr.Payload{
			OperationType:  expr.PayloadWrite,
			SourceRegister: 1,
			Base:           expr.PayloadBaseNetworkHeader,
			Offset:         12, // IP source address
			Len:            4,
			CsumType:       expr.CsumTypeInet,
			CsumOffset:     10, // IP header checksum offset
			CsumFlags:      unix.NFT_PAYLOAD_L4CSUM_PSEUDOHDR,
		},
		// Reset mark to 0 (prevent policy routing interference)
		&expr.Immediate{Register: 1, Data: binaryutil.NativeEndian.PutUint32(0)},
		&expr.Meta{Key: expr.MetaKeyMARK, SourceRegister: true, Register: 1},
	}
}

// buildPMTUDIPv6Exprs builds nftables expressions for IPv6 PMTU SNAT.
// Matches: ICMPv6 type 2 (Packet Too Big), mark != 0,
// dst NOT VIP, src NOT VIP, encapsulated dst IS VIP.
// Action: rewrite IPv6 src to encapsulated dst (the VIP), reset mark to 0.
func buildPMTUDIPv6Exprs(ipv6Set *nftables.Set) []expr.Any {
	return []expr.Any{
		// Match IPv6
		&expr.Meta{Key: expr.MetaKeyNFPROTO, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{unix.AF_INET6}},
		// Match ICMPv6
		&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{unix.IPPROTO_ICMPV6}},
		// Non-zero fwmark
		&expr.Meta{Key: expr.MetaKeyMARK, Register: 1},
		&expr.Cmp{Op: expr.CmpOpNeq, Register: 1, Data: []byte{0x0}},
		// dst NOT in VIP set
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 24, Len: 16},
		&expr.Lookup{SourceRegister: 1, SetName: ipv6Set.Name, SetID: ipv6Set.ID, Invert: true},
		// src NOT in VIP set
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 8, Len: 16},
		&expr.Lookup{SourceRegister: 1, SetName: ipv6Set.Name, SetID: ipv6Set.ID, Invert: true},
		// ICMPv6 type == 2 (Packet Too Big)
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseTransportHeader, Offset: 0, Len: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{0x2}},
		// Encapsulated dst (offset 32 in ICMPv6 payload) IS a VIP
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseTransportHeader, Offset: 32, Len: 16},
		&expr.Lookup{SourceRegister: 1, SetName: ipv6Set.Name, SetID: ipv6Set.ID},
		// Counter
		&expr.Counter{},
		// Load encapsulated dst again → write to IPv6 source
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseTransportHeader, Offset: 32, Len: 16},
		&expr.Payload{
			OperationType:  expr.PayloadWrite,
			SourceRegister: 1,
			Base:           expr.PayloadBaseNetworkHeader,
			Offset:         8, // IPv6 source address
			Len:            16,
			CsumType:       expr.CsumTypeNone, // IPv6 has no header checksum
		},
		// Reset mark to 0
		&expr.Immediate{Register: 1, Data: binaryutil.NativeEndian.PutUint32(0)},
		&expr.Meta{Key: expr.MetaKeyMARK, SourceRegister: true, Register: 1},
	}
}

// createDefragTable creates a separate nftables table for selective defragmentation.
// Enables kernel defrag for external traffic (needed for L4 flow matching) while
// skipping defrag for target-facing interfaces (preserving PMTU information).
func (m *Manager) createDefragTable() error {
	m.defragTable = m.conn.AddTable(&nftables.Table{
		Name:   defragTableName,
		Family: nftables.TableFamilyINet,
	})

	// pre-defrag chain at priority -500 (before conntrack defrag at -400)
	// Applies notrack to packets from target-facing interfaces to skip defrag
	m.defragPreChain = m.conn.AddChain(&nftables.Chain{
		Name:     defragPreChainName,
		Table:    m.defragTable,
		Type:     nftables.ChainTypeFilter,
		Hooknum:  nftables.ChainHookPrerouting,
		Priority: nftables.ChainPriorityRef(-500),
	})

	// notrack for each excluded interface prefix
	for _, prefix := range m.excludedIfPrefixes {
		m.conn.AddRule(&nftables.Rule{
			Table: m.defragTable,
			Chain: m.defragPreChain,
			Exprs: []expr.Any{
				&expr.Meta{Key: expr.MetaKeyIIFNAME, Register: 1},
				// byte-stream without null terminator matches as prefix
				&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte(prefix)},
				&expr.Notrack{},
			},
		})
	}

	// in chain: load defrag via dummy conntrack, then notrack all ingress
	m.defragInChain = m.conn.AddChain(&nftables.Chain{
		Name:     defragInChainName,
		Table:    m.defragTable,
		Type:     nftables.ChainTypeFilter,
		Hooknum:  nftables.ChainHookPrerouting,
		Priority: nftables.ChainPriorityRaw,
	})

	// notrack all ingress (disable conntrack bookkeeping)
	m.conn.AddRule(&nftables.Rule{
		Table: m.defragTable,
		Chain: m.defragInChain,
		Exprs: []expr.Any{&expr.Notrack{}},
	})

	// dummy conntrack rule to trigger kernel defrag module loading
	m.conn.AddRule(&nftables.Rule{
		Table: m.defragTable,
		Chain: m.defragInChain,
		Exprs: []expr.Any{
			&expr.Ct{Register: 1, SourceRegister: false, Key: expr.CtKeySTATE},
			&expr.Bitwise{
				SourceRegister: 1,
				DestRegister:   1,
				Len:            4,
				Mask:           binaryutil.NativeEndian.PutUint32(expr.CtStateBitUNTRACKED),
				Xor:            binaryutil.NativeEndian.PutUint32(0),
			},
			&expr.Cmp{Op: expr.CmpOpNeq, Register: 1, Data: []byte{0, 0, 0, 0}},
			&expr.Verdict{Kind: expr.VerdictAccept},
		},
	})

	// out chain: notrack all egress
	m.defragOutChain = m.conn.AddChain(&nftables.Chain{
		Name:     defragOutChainName,
		Table:    m.defragTable,
		Type:     nftables.ChainTypeFilter,
		Hooknum:  nftables.ChainHookOutput,
		Priority: nftables.ChainPriorityRaw,
	})

	m.conn.AddRule(&nftables.Rule{
		Table: m.defragTable,
		Chain: m.defragOutChain,
		Exprs: []expr.Any{&expr.Notrack{}},
	})

	return m.conn.Flush()
}

func (m *Manager) updateSet(set *nftables.Set, cidrs []string) error {
	elements := []nftables.SetElement{}
	for _, cidr := range cidrs {
		_, ipNet, err := net.ParseCIDR(cidr)
		if err != nil {
			return fmt.Errorf("invalid CIDR %s: %w", cidr, err)
		}
		setElems := cidrToSetElements(ipNet)
		elements = append(elements, setElems...)
	}

	// Allow empty sets - just flush and return
	if len(elements) == 0 {
		// Get the actual set from the kernel
		sets, err := m.conn.GetSets(m.table)
		if err != nil {
			return fmt.Errorf("failed to get sets: %w", err)
		}

		var targetSet *nftables.Set
		for _, s := range sets {
			if s.Name == set.Name {
				targetSet = s
				break
			}
		}
		if targetSet == nil {
			return fmt.Errorf("set %s not found", set.Name)
		}

		m.conn.FlushSet(targetSet)
		return m.conn.Flush()
	}

	// Get the actual set from the kernel
	sets, err := m.conn.GetSets(m.table)
	if err != nil {
		return fmt.Errorf("failed to get sets: %w", err)
	}

	var targetSet *nftables.Set
	for _, s := range sets {
		if s.Name == set.Name {
			targetSet = s
			break
		}
	}
	if targetSet == nil {
		return fmt.Errorf("set %s not found", set.Name)
	}

	m.conn.FlushSet(targetSet)
	if err := m.conn.SetAddElements(targetSet, elements); err != nil {
		return fmt.Errorf("failed to add elements: %w", err)
	}
	if err := m.conn.Flush(); err != nil {
		return fmt.Errorf("failed to flush connection: %w", err)
	}
	return nil
}

func cidrToSetElements(ipNet *net.IPNet) []nftables.SetElement {
	start := ipNet.IP
	end := nextIP(broadcast(ipNet))

	// Normalize to IPv4 if applicable
	if v4 := start.To4(); v4 != nil {
		start = v4
		end = end.To4()
	}

	return []nftables.SetElement{
		{Key: start, IntervalEnd: false},
		{Key: end, IntervalEnd: true},
	}
}

func broadcast(ipNet *net.IPNet) net.IP {
	ip := ipNet.IP
	mask := ipNet.Mask
	broadcast := make(net.IP, len(ip))
	for i := range ip {
		broadcast[i] = ip[i] | ^mask[i]
	}
	return broadcast
}

func nextIP(ip net.IP) net.IP {
	next := make(net.IP, len(ip))
	copy(next, ip)
	for i := len(next) - 1; i >= 0; i-- {
		next[i]++
		if next[i] != 0 {
			break
		}
	}
	return next
}

func deduplicateVIPs(cidrs []string) []string {
	seen := make(map[string]struct{})
	result := []string{}
	for _, cidr := range cidrs {
		if _, exists := seen[cidr]; !exists {
			seen[cidr] = struct{}{}
			result = append(result, cidr)
		}
	}
	return result
}

func splitIPv4AndIPv6(cidrs []string) ([]string, []string) {
	ipv4 := []string{}
	ipv6 := []string{}
	for _, cidr := range cidrs {
		ip, _, err := net.ParseCIDR(cidr)
		if err != nil {
			continue
		}
		// Use To4() to check if it's IPv4 or IPv4-mapped IPv6
		// For true IPv4, To4() returns 4-byte representation
		// For IPv4-mapped IPv6 (::ffff:192.0.2.1), To4() also returns non-nil
		// But we want to treat IPv4-mapped as IPv6, so check original length
		if ip.To4() != nil && !strings.Contains(cidr, ":") {
			ipv4 = append(ipv4, cidr)
		} else {
			ipv6 = append(ipv6, cidr)
		}
	}
	return ipv4, ipv6
}
