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

package loadbalancer

import (
	"fmt"
	"net"

	"github.com/vishvananda/netlink"
)

// RoutingManager manages policy routing for load balancer targets
type RoutingManager struct {
	// Track configured fwmarks to avoid duplicates
	configuredFwmarks map[int]routeInfo
	// Allow disabling for tests
	disabled bool
	// Netlink operations (abstracted for testing).
	// *netlink.Handle satisfies this interface directly.
	nl netlinkOps
}

// netlinkOps abstracts netlink operations for testability.
// *netlink.Handle satisfies this interface directly.
type netlinkOps interface {
	RuleAdd(rule *netlink.Rule) error
	RuleDel(rule *netlink.Rule) error
	RuleList(family int) ([]netlink.Rule, error)
	RouteReplace(route *netlink.Route) error
	RouteDel(route *netlink.Route) error
	NeighList(link, family int) ([]netlink.Neigh, error)
	NeighDel(neigh *netlink.Neigh) error
}

type routeInfo struct {
	tableID int
	gateway net.IP
}

// NewRoutingManager creates a new routing manager
func NewRoutingManager() *RoutingManager {
	return &RoutingManager{
		configuredFwmarks: make(map[int]routeInfo),
		disabled:          false,
		nl:                &netlink.Handle{},
	}
}

// NewMockRoutingManager creates a disabled routing manager for tests
func NewMockRoutingManager() *RoutingManager {
	return &RoutingManager{
		configuredFwmarks: make(map[int]routeInfo),
		disabled:          true,
		nl:                &netlink.Handle{},
	}
}

// AddRoute configures policy routing for a target
// Creates: ip rule add fwmark <fwmark> table <tableID>
//
//	ip route replace default via <targetIP> table <tableID>
//
// Uses RouteReplace to atomically handle stale routes (e.g., target IP change,
// container restart with kernel state surviving).
func (r *RoutingManager) AddRoute(fwmark int, targetIP string) error {
	// Skip if disabled (for tests)
	if r.disabled {
		r.configuredFwmarks[fwmark] = routeInfo{tableID: fwmark}
		return nil
	}

	tableID := fwmark // Use fwmark as table ID

	// Parse target IP
	ip := net.ParseIP(targetIP)
	if ip == nil {
		return fmt.Errorf("invalid target IP: %s", targetIP)
	}

	// Clean stale neighbor entries
	_ = r.cleanNeighbor(ip)

	// Determine IP family
	family := netlink.FAMILY_V6
	if ip.To4() != nil {
		family = netlink.FAMILY_V4
	}

	// Ensure policy routing rule: fwmark -> table
	// Linux ip rule allows duplicates (no EEXIST), so we must check existing rules.
	// If a rule for this fwmark already exists with the correct table, skip.
	// If it exists with a different table, delete it and add the correct one.
	// If no rule exists, add it.
	rule := netlink.NewRule()
	rule.Mark = uint32(fwmark)
	rule.Table = tableID
	rule.Family = family
	rule.Priority = 32000 // Standard priority for fwmark rules

	if err := r.ensureRule(rule); err != nil {
		return fmt.Errorf("failed to ensure routing rule for fwmark %d: %w", fwmark, err)
	}

	// Replace route in custom table — atomically overwrites any existing route
	// regardless of current gateway IP (fixes stale route bug)
	route := &netlink.Route{
		Gw:    ip,
		Table: tableID,
	}

	if err := r.nl.RouteReplace(route); err != nil {
		// Cleanup rule on failure to avoid orphaned rule pointing to empty table
		_ = r.nl.RuleDel(rule)
		return fmt.Errorf("failed to replace route for fwmark %d: %w", fwmark, err)
	}

	r.configuredFwmarks[fwmark] = routeInfo{
		tableID: tableID,
		gateway: ip,
	}
	return nil
}

// ensureRule adds the desired rule only if it doesn't already exist.
// Linux allows duplicate ip rules, so we check first to avoid accumulating duplicates
// across reconciles.
func (r *RoutingManager) ensureRule(desired *netlink.Rule) error {
	rules, err := r.nl.RuleList(desired.Family)
	if err != nil {
		// Can't list rules — fall through to add (best effort)
		return r.nl.RuleAdd(desired)
	}

	for _, existing := range rules {
		if existing.Mark == desired.Mark && existing.Table == desired.Table && existing.Priority == desired.Priority {
			return nil // Already exists
		}
	}

	return r.nl.RuleAdd(desired)
}

// cleanNeighbor removes stale ARP/NDP entries for the IP
func (r *RoutingManager) cleanNeighbor(ip net.IP) error {
	neighbors, err := r.nl.NeighList(0, 0)
	if err != nil {
		return err
	}

	for _, neighbor := range neighbors {
		if neighbor.IP.Equal(ip) {
			_ = r.nl.NeighDel(&neighbor)
		}
	}

	return nil
}

// deleteRouteInternal deletes route without checking configuredFwmarks
func (r *RoutingManager) deleteRouteInternal(fwmark int, ip net.IP) {
	tableID := fwmark

	family := netlink.FAMILY_V6
	if ip.To4() != nil {
		family = netlink.FAMILY_V4
	}

	rule := netlink.NewRule()
	rule.Mark = uint32(fwmark)
	rule.Table = tableID
	rule.Family = family
	rule.Priority = 32000

	_ = r.nl.RuleDel(rule)

	route := &netlink.Route{
		Gw:    ip,
		Table: tableID,
	}

	_ = r.nl.RouteDel(route)
}

// DeleteRoute removes policy routing for a target
func (r *RoutingManager) DeleteRoute(fwmark int) error {
	info, exists := r.configuredFwmarks[fwmark]
	if !exists {
		return nil
	}

	// Skip actual deletion if disabled (for tests)
	if r.disabled {
		delete(r.configuredFwmarks, fwmark)
		return nil
	}

	// Delete using internal method
	r.deleteRouteInternal(fwmark, info.gateway)

	delete(r.configuredFwmarks, fwmark)
	return nil
}

// Cleanup removes all configured routes
func (r *RoutingManager) Cleanup() error {
	var lastErr error
	for fwmark := range r.configuredFwmarks {
		if err := r.DeleteRoute(fwmark); err != nil {
			lastErr = err
		}
	}
	return lastErr
}
