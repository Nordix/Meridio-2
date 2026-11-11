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
	netipv6 "golang.org/x/net/ipv6"
	"golang.org/x/sys/unix"
)

const (
	preroutingChainName      = "prerouting"
	outputChainName          = "output"
	pmtudChainName           = "snat-local"
	dropAccountingChainName  = "drop-accounting"
	primaryGuardFwdChainName = "primary-egress-guard-forward"
	ipv4VIPSetName           = "ipv4-vips"
	ipv6VIPSetName           = "ipv6-vips"
)

// Manager manages nftables rules for VIP traffic.
type Manager struct {
	tableName       string
	queueNum        uint16
	queueTotal      uint16
	nolbFwmark      uint32
	notargetsFwmark uint32
	primaryIfindex  uint32
	table           *nftables.Table
	preChain        *nftables.Chain
	outputChain     *nftables.Chain
	pmtudChain      *nftables.Chain
	dropChain       *nftables.Chain
	primaryGuardFwd *nftables.Chain
	ipv4Set         *nftables.Set
	ipv6Set         *nftables.Set
	ipv4PreRule     *nftables.Rule
	ipv6PreRule     *nftables.Rule
	ipv4OutRule     *nftables.Rule
	ipv6OutRule     *nftables.Rule
	conn            *nftables.Conn
}

const sharedTableName = "meridio-lb" // Shared table for all DistributionGroups

// NewManager creates a new nftables manager.
// Uses a single shared table for all DistributionGroups.
//
// primaryIfindex is the ifindex of the LB Pod's primary (default CNI) network interface.
// Forwarded packets egressing on it are counted and dropped (see
// createPrimaryEgressGuardForwardChain). 0 skips the guard chain.
func NewManager(queueNum, queueTotal uint16, nolbFwmark, notargetsFwmark, primaryIfindex uint32) (*Manager, error) {
	return &Manager{
		tableName:       sharedTableName,
		queueNum:        queueNum,
		queueTotal:      queueTotal,
		nolbFwmark:      nolbFwmark,
		notargetsFwmark: notargetsFwmark,
		primaryIfindex:  primaryIfindex,
		conn:            &nftables.Conn{},
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
	if err := m.createDropAccountingChain(); err != nil {
		return err
	}
	if err := m.createOutputChain(); err != nil {
		return err
	}
	if err := m.createPMTUDChain(); err != nil {
		return err
	}
	if err := m.createPrimaryEgressGuardForwardChain(); err != nil {
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
	m.conn.FlushChain(m.preChain) // drop rules left by a previous instance in this netns

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

// createDropAccountingChain creates a separate prerouting chain with higher priority
// (runs after the main prerouting chain) to catch packets that NFQLB returned with
// NF_ACCEPT and a drop-reason fwmark. Packets returning from NFQUEUE with NF_ACCEPT
// skip remaining rules in the originating chain, so a separate chain is required.
func (m *Manager) createDropAccountingChain() error {
	if m.nolbFwmark == 0 && m.notargetsFwmark == 0 {
		return nil // Drop accounting disabled
	}

	m.dropChain = m.conn.AddChain(&nftables.Chain{
		Name:     dropAccountingChainName,
		Table:    m.table,
		Type:     nftables.ChainTypeFilter,
		Hooknum:  nftables.ChainHookPrerouting,
		Priority: new(*nftables.ChainPriorityFilter + 1),
	})
	m.conn.FlushChain(m.dropChain)

	// No-flow drop: packet matched a VIP but no flow selector matched in nfqlb
	if m.nolbFwmark != 0 {
		m.conn.AddRule(&nftables.Rule{
			Table: m.table,
			Chain: m.dropChain,
			Exprs: []expr.Any{
				&expr.Meta{Key: expr.MetaKeyMARK, Register: 1},
				&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: binaryutil.NativeEndian.PutUint32(m.nolbFwmark)},
				&expr.Counter{},
				&expr.Verdict{Kind: expr.VerdictDrop},
			},
		})
	}

	// No-targets drop: flow matched but the LB instance has no active targets
	if m.notargetsFwmark != 0 {
		m.conn.AddRule(&nftables.Rule{
			Table: m.table,
			Chain: m.dropChain,
			Exprs: []expr.Any{
				&expr.Meta{Key: expr.MetaKeyMARK, Register: 1},
				&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: binaryutil.NativeEndian.PutUint32(m.notargetsFwmark)},
				&expr.Counter{},
				&expr.Verdict{Kind: expr.VerdictDrop},
			},
		})
	}

	return m.conn.Flush()
}

// createPrimaryEgressGuardForwardChain creates a forward-hook safety net that counts and drops
// forwarded packets egressing on the LB Pod's primary interface. Nothing in the LB
// legitimately forwards toward the primary network: VIP traffic goes to targets on secondary
// networks and return traffic leaves via the external secondary network. A packet hitting this
// rule fell through to the main table's default route, e.g. because a target's fwmark policy
// route is missing or the VIP is not (yet) in the VIP sets. Occasional hits are expected during
// transients such as startup or target/VIP churn; a steadily growing counter points to a
// persistent fault.
//
// The chain is on the data path of all load-balanced traffic, so the rule is a single 4-byte
// ifindex compare (meta oif) with no ingress qualifier. The ifindex is resolved once per process
// start, which suffices because the primary interface in K8s lives as long as the Pod's network
// sandbox and so outlives the process. Policy is accept: a policy drop chain in this shared
// table would break all other traffic.
func (m *Manager) createPrimaryEgressGuardForwardChain() error {
	if m.primaryIfindex == 0 {
		return nil // No primary interface given: skip the guard chain
	}

	policy := nftables.ChainPolicyAccept
	m.primaryGuardFwd = m.conn.AddChain(&nftables.Chain{
		Name:     primaryGuardFwdChainName,
		Table:    m.table,
		Type:     nftables.ChainTypeFilter,
		Hooknum:  nftables.ChainHookForward,
		Priority: nftables.ChainPriorityFilter,
		Policy:   &policy,
	})
	m.conn.FlushChain(m.primaryGuardFwd)

	// meta oif <primary> counter drop
	m.conn.AddRule(&nftables.Rule{
		Table: m.table,
		Chain: m.primaryGuardFwd,
		Exprs: buildPrimaryEgressDropExprs(m.primaryIfindex),
	})

	return m.conn.Flush()
}

// buildPrimaryEgressDropExprs builds the expressions for: meta oif <primaryIfindex> counter drop.
func buildPrimaryEgressDropExprs(primaryIfindex uint32) []expr.Any {
	return []expr.Any{
		&expr.Meta{Key: expr.MetaKeyOIF, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: binaryutil.NativeEndian.PutUint32(primaryIfindex)},
		&expr.Counter{},
		&expr.Verdict{Kind: expr.VerdictDrop},
	}
}

func (m *Manager) createOutputChain() error {
	m.outputChain = m.conn.AddChain(&nftables.Chain{
		Name:     outputChainName,
		Table:    m.table,
		Type:     nftables.ChainTypeFilter,
		Hooknum:  nftables.ChainHookOutput,
		Priority: nftables.ChainPriorityFilter,
	})
	m.conn.FlushChain(m.outputChain)

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

	// IPv6 rule: meta l4proto icmpv6 ip6 daddr @ipv6-vips icmpv6 type { ... } counter queue num X-Y fanout
	// Only the types in steeredICMPv6Types are queued; see there for why Neighbor Discovery
	// must stay out of nfqlb.
	typeSet := &nftables.Set{
		Table:     m.table,
		Anonymous: true,
		Constant:  true,
		KeyType:   nftables.TypeICMP6Type,
	}
	typeElems := make([]nftables.SetElement, 0, len(steeredICMPv6Types))
	for _, t := range steeredICMPv6Types {
		typeElems = append(typeElems, nftables.SetElement{Key: []byte{byte(t)}})
	}
	if err := m.conn.AddSet(typeSet, typeElems); err != nil {
		return fmt.Errorf("failed to create ICMPv6 type set: %w", err)
	}
	m.ipv6OutRule = m.conn.AddRule(&nftables.Rule{
		Table: m.table,
		Chain: m.outputChain,
		Exprs: buildOutputIPv6Exprs(m.ipv6Set, typeSet, m.queueNum, m.queueTotal),
	})

	return m.conn.Flush()
}

// steeredICMPv6Types are the locally generated ICMPv6 types addressed to a VIP that the output
// chain queues to nfqlb, so that they reach a target rather than being routed by the LB:
//   - Error messages the LB generates for VIP-sourced packets it forwards on behalf of a target
//     (for example Packet Too Big when the external MTU is smaller). nfqlb steers Destination
//     Unreachable and Packet Too Big using the embedded original header. Time Exceeded and
//     Parameter Problem are hashed on the outer addresses, so with several targets they may land
//     on a different target.
//   - Echo Reply to a VIP-sourced ping of an LB address. It is hashed the same way.
//   - Echo Request from the LB Pod to a VIP (for example an operator pinging the VIP), so that
//     a target answers it as with IPv4, whose output rule queues all ICMP. nfqlb matches it as
//     a ping and hashes it including the Echo identifier. This only applies when the LB Pod
//     has a route covering the VIP (for example a default route on the primary network): the
//     route lookup happens before the output hook, so without one the ping fails at the socket.
//
// Neighbor Discovery must not be queued. When an application Pod resolves its LB next hop
// for a packet it sends from the VIP (a connection it initiates, or a reply to inbound
// traffic), the kernel sources the Neighbor Solicitation from the VIP, so the LB's Neighbor
// Advertisement is addressed to the VIP. With --promiscuous_ping, nfqlb matches any ICMPv6
// other than Destination Unreachable and Packet Too Big to the highest priority flow covering
// the VIP and load-balances it to a target instead of the Pod that solicited. Left to the
// kernel, the NA follows the neighbor entry the NS just created and reaches the right Pod.
var steeredICMPv6Types = []netipv6.ICMPType{
	netipv6.ICMPTypeDestinationUnreachable,
	netipv6.ICMPTypePacketTooBig,
	netipv6.ICMPTypeTimeExceeded,
	netipv6.ICMPTypeParameterProblem,
	netipv6.ICMPTypeEchoRequest,
	netipv6.ICMPTypeEchoReply,
}

// buildOutputIPv6Exprs builds the nftables expressions for the IPv6 output rule.
// Matches: IPv6, ICMPv6, dst IS VIP, ICMPv6 type in typeSet.
// Action: count and queue to nfqlb.
func buildOutputIPv6Exprs(ipv6Set, typeSet *nftables.Set, queueNum, queueTotal uint16) []expr.Any {
	return []expr.Any{
		// Match IPv6
		&expr.Meta{Key: expr.MetaKeyNFPROTO, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{unix.AF_INET6}},
		// Match ICMPv6
		&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{unix.IPPROTO_ICMPV6}},
		// dst IS VIP (IPv6 dst at offset 24, 16 bytes)
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 24, Len: 16},
		&expr.Lookup{SourceRegister: 1, SetName: ipv6Set.Name, SetID: ipv6Set.ID},
		// ICMPv6 type in the steered set
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseTransportHeader, Offset: 0, Len: 1},
		&expr.Lookup{SourceRegister: 1, SetName: typeSet.Name, SetID: typeSet.ID},
		&expr.Counter{},
		&expr.Queue{Num: queueNum, Total: queueTotal, Flag: 0},
	}
}

// createPMTUDChain creates the PMTU discovery chain.
// Rewrites source address of locally generated ICMP Frag Needed / ICMPv6 Packet Too Big
// replies to use the VIP from the encapsulated original packet. This ensures external
// clients receive PMTU feedback with the correct source (VIP, not LB pod IP).
//
// Chain type is "route" (not "filter") so the kernel re-evaluates routing after rewrite.
// Requires net.ipv4.fwmark_reflect=1 and net.ipv6.fwmark_reflect=1 sysctls.
//
// Notes:
//   - If src of ICMP reply is already a VIP address then leave it unchanged.
//   - Avoid mangling locally generated ICMPv4/v6 replies destined to VIP addresses.
//     (Those shall be handled by nfqlb.)
//   - Checking non-zero fwmark makes sure that offending packet got processed by nfqlb.
//     Thus, checks if original packet was destined to a VIP address (provides early skip
//     for packets not relevant, therefore no need to walk through most of the matches
//     in vain and do an additional transport lookup verifying if orig dst was VIP).
//     REQUIRES enabling fwmark_reflect sysctl for both IPv4 and IPv6!
//     (Note: Sysctls also allows for an early successful route lookup to generate the reply.)
//   - Additional safety check verifies that encapsulated destination address is a VIP address.
//   - Resetting packet mark at the end makes sure policy based routes related to Targets
//     do not interfere.
func (m *Manager) createPMTUDChain() error {
	m.pmtudChain = m.conn.AddChain(&nftables.Chain{
		Name:     pmtudChainName,
		Table:    m.table,
		Type:     nftables.ChainTypeRoute,
		Hooknum:  nftables.ChainHookOutput,
		Priority: nftables.ChainPriorityRaw,
	})
	m.conn.FlushChain(m.pmtudChain)

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
