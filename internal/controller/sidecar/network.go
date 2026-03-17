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

package sidecar

import (
	"context"
	"errors"
	"fmt"
	"net"
	"syscall"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// InterfaceNotFoundError indicates no interface matched the requested subnet.
type InterfaceNotFoundError struct {
	Subnet string
}

func (e *InterfaceNotFoundError) Error() string {
	return fmt.Sprintf("no interface found matching subnet %s", e.Subnet)
}

// findInterfaceBySubnet finds the network interface whose address matches the given subnet.
// If hint is non-empty, checks that interface first for faster discovery.
func findInterfaceBySubnet(nl netlinkOps, hint, subnet string) (netlink.Link, error) {
	_, subnetNet, err := net.ParseCIDR(subnet)
	if err != nil {
		return nil, fmt.Errorf("invalid subnet %q: %w", subnet, err)
	}

	// Fast path: check hint interface first
	if hint != "" {
		link, err := nl.LinkByName(hint)
		if err == nil {
			if interfaceMatchesSubnet(nl, link, subnetNet) {
				return link, nil
			}
		}
	}

	// Slow path: scan all interfaces
	links, err := nl.LinkList()
	if err != nil {
		return nil, fmt.Errorf("failed to list interfaces: %w", err)
	}

	for _, link := range links {
		if interfaceMatchesSubnet(nl, link, subnetNet) {
			return link, nil
		}
	}

	return nil, &InterfaceNotFoundError{Subnet: subnet}
}

// interfaceMatchesSubnet checks if any address on the interface belongs to the subnet.
func interfaceMatchesSubnet(nl netlinkOps, link netlink.Link, subnet *net.IPNet) bool {
	addrs, err := nl.AddrList(link, netlink.FAMILY_ALL)
	if err != nil {
		return false
	}

	for _, addr := range addrs {
		if subnet.Contains(addr.IP) {
			return true
		}
	}
	return false
}

// syncVIPs ensures exactly the desired VIPs are present on the interface.
// Adds missing VIPs, removes stale ones (only within the managed set).
// Always returns the current managed set reflecting actual kernel state,
// even on error, so the caller can track partially-applied changes.
func syncVIPs(nl netlinkOps, link netlink.Link, desiredVIPs []net.IP, managedVIPs map[string]bool) (map[string]bool, error) {
	// Clone managed set — we mutate it incrementally to track actual state
	current := make(map[string]bool, len(managedVIPs)+len(desiredVIPs))
	for k := range managedVIPs {
		current[k] = true
	}

	desired := make(map[string]bool, len(desiredVIPs))
	for _, vip := range desiredVIPs {
		desired[vip.String()] = true
	}

	// Remove stale managed VIPs (update current on each success)
	for vipStr := range managedVIPs {
		if desired[vipStr] {
			continue
		}
		ip := net.ParseIP(vipStr)
		if ip == nil {
			delete(current, vipStr)
			continue
		}
		addr := &netlink.Addr{IPNet: vipToIPNet(ip)}
		if err := nl.AddrDel(link, addr); err != nil && !errors.Is(err, syscall.EADDRNOTAVAIL) {
			return current, fmt.Errorf("failed to remove VIP %s: %w", vipStr, err)
		}
		delete(current, vipStr)
	}

	// Add missing VIPs (update current on each success)
	for _, vip := range desiredVIPs {
		vipStr := vip.String()
		if current[vipStr] {
			continue // already present
		}
		addr := &netlink.Addr{IPNet: vipToIPNet(vip)}
		if vip.To4() == nil {
			addr.Flags = unix.IFA_F_NODAD
		}
		if err := nl.AddrAdd(link, addr); err != nil && !errors.Is(err, syscall.EEXIST) {
			return current, fmt.Errorf("failed to add VIP %s: %w", vipStr, err)
		}
		current[vipStr] = true
	}

	return current, nil
}

// vipToIPNet converts a VIP IP to a /32 (IPv4) or /128 (IPv6) IPNet.
func vipToIPNet(ip net.IP) *net.IPNet {
	bits := 128
	if ip.To4() != nil {
		bits = 32
	}
	return &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)}
}

// syncRules ensures policy routing rules exist for each VIP → table mapping.
// Removes stale rules (within managed table ID range) and adds missing ones.
func syncRules(ctx context.Context, nl netlinkOps, vips []net.IP, tableID, minTableID, maxTableID int) error {
	log := logf.FromContext(ctx)
	// Build desired rule set
	type ruleKey struct {
		src   string
		table int
	}
	desired := make(map[ruleKey]bool, len(vips))
	for _, vip := range vips {
		ipNet := vipToIPNet(vip)
		desired[ruleKey{src: ipNet.String(), table: tableID}] = true
	}

	// List existing rules and remove stale ones in our range
	rules, err := nl.RuleList(netlink.FAMILY_ALL)
	if err != nil {
		return fmt.Errorf("failed to list rules: %w", err)
	}

	for i := range rules {
		rule := &rules[i]
		if rule.Table < minTableID || rule.Table > maxTableID {
			continue
		}
		if rule.Table != tableID || rule.Src == nil {
			continue
		}
		key := ruleKey{src: rule.Src.String(), table: rule.Table}
		if desired[key] {
			delete(desired, key) // already exists
		} else {
			if err := nl.RuleDel(rule); err != nil {
				log.V(1).Info("failed to delete stale rule", "src", rule.Src, "table", rule.Table, "error", err)
			}
		}
	}

	// Add missing rules
	for key := range desired {
		_, ipNet, err := net.ParseCIDR(key.src)
		if err != nil {
			continue
		}
		family := netlink.FAMILY_V6
		if ipNet.IP.To4() != nil {
			family = netlink.FAMILY_V4
		}
		rule := netlink.NewRule()
		rule.Src = ipNet
		rule.Table = tableID
		rule.Family = family
		if err := nl.RuleAdd(rule); err != nil {
			return fmt.Errorf("failed to add rule for %s table %d: %w", key.src, tableID, err)
		}
	}

	return nil
}

// syncRoutes sets the default route in the given table with ECMP next-hops.
// Uses RouteReplace for idempotency.
func syncRoutes(ctx context.Context, nl netlinkOps, nextHops []net.IP, tableID int) error {
	// Group next-hops by IP family
	var v4Hops, v6Hops []net.IP
	for _, nh := range nextHops {
		if nh.To4() != nil {
			v4Hops = append(v4Hops, nh)
		} else {
			v6Hops = append(v6Hops, nh)
		}
	}

	if err := syncRoutesForFamily(ctx, nl, v4Hops, tableID, netlink.FAMILY_V4); err != nil {
		return err
	}
	return syncRoutesForFamily(ctx, nl, v6Hops, tableID, netlink.FAMILY_V6)
}

func syncRoutesForFamily(ctx context.Context, nl netlinkOps, hops []net.IP, tableID, family int) error {
	log := logf.FromContext(ctx)
	if len(hops) == 0 {
		routes, err := nl.RouteListFiltered(family, &netlink.Route{Table: tableID}, netlink.RT_FILTER_TABLE)
		if err != nil {
			return fmt.Errorf("failed to list routes for table %d: %w", tableID, err)
		}
		for i := range routes {
			if err := nl.RouteDel(&routes[i]); err != nil {
				log.V(1).Info("failed to delete route", "table", tableID, "error", err)
			}
		}
		return nil
	}

	nexthops := make([]*netlink.NexthopInfo, 0, len(hops))
	for _, hop := range hops {
		nexthops = append(nexthops, &netlink.NexthopInfo{Gw: hop})
	}

	var dst *net.IPNet
	if family == netlink.FAMILY_V4 {
		dst = &net.IPNet{IP: net.IPv4zero, Mask: net.CIDRMask(0, 32)}
	} else {
		dst = &net.IPNet{IP: net.IPv6zero, Mask: net.CIDRMask(0, 128)}
	}

	route := &netlink.Route{
		Dst:       dst,
		MultiPath: nexthops,
		Table:     tableID,
		Family:    family,
	}

	if err := nl.RouteReplace(route); err != nil {
		return fmt.Errorf("failed to replace route in table %d: %w", tableID, err)
	}
	return nil
}

// flushTable removes all routes and rules for a table ID within the managed range.
func flushTable(ctx context.Context, nl netlinkOps, tableID, minTableID, maxTableID int) {
	log := logf.FromContext(ctx)
	if tableID < minTableID || tableID > maxTableID {
		return
	}

	for _, family := range []int{netlink.FAMILY_V4, netlink.FAMILY_V6} {
		routes, err := nl.RouteListFiltered(family, &netlink.Route{Table: tableID}, netlink.RT_FILTER_TABLE)
		if err != nil {
			continue
		}
		for i := range routes {
			if err := nl.RouteDel(&routes[i]); err != nil {
				log.V(1).Info("flush: failed to delete route", "table", tableID, "error", err)
			}
		}
	}

	rules, err := nl.RuleList(netlink.FAMILY_ALL)
	if err != nil {
		return
	}
	for i := range rules {
		if rules[i].Table == tableID {
			if err := nl.RuleDel(&rules[i]); err != nil {
				log.V(1).Info("flush: failed to delete rule", "table", tableID, "error", err)
			}
		}
	}
}
