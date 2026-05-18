/*
Copyright (c) 2024-2026 OpenInfra Foundation Europe

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

package nfqlb

import (
	"errors"
	"fmt"
	"net"

	"github.com/vishvananda/netlink"
)

var errInvalidIP = errors.New("the ip address is invalid")

// createPolicyRoute creates or replaces a policy route for the given fwmark.
// Uses RouteReplace to atomically handle stale routes (e.g., target IP change,
// container restart with kernel state surviving).
// Uses ensureRule to avoid accumulating duplicate ip rules across reconciles.
// Does NOT flush ARP/NDP entries — caller should use cleanNeighbor only when IPs change.
func createPolicyRoute(fwMark int, ip string) error {
	ipAddr := net.ParseIP(ip)
	if ipAddr == nil {
		return errInvalidIP
	}

	rule := getRule(fwMark, ipAddr)
	if err := ensureRule(rule); err != nil {
		return fmt.Errorf("failed to ensure rule for fwmark %d: %w", fwMark, err)
	}

	if err := netlink.RouteReplace(getRoute(fwMark, ipAddr)); err != nil {
		// Cleanup rule on failure to avoid orphaned rule pointing to empty table
		_ = netlink.RuleDel(rule)
		return fmt.Errorf("failed to RouteReplace for fwmark %d: %w", fwMark, err)
	}

	return nil
}

// ensureRule adds the desired rule only if it doesn't already exist.
// Linux allows duplicate ip rules, so we check first to avoid accumulating duplicates
// across reconciles.
func ensureRule(desired *netlink.Rule) error {
	rules, err := netlink.RuleList(desired.Family)
	if err != nil {
		// Can't list rules — fall through to add (best effort)
		return netlink.RuleAdd(desired)
	}

	for _, existing := range rules {
		if existing.Mark == desired.Mark && existing.Table == desired.Table && existing.Priority == desired.Priority {
			return nil // Already exists
		}
	}

	return netlink.RuleAdd(desired)
}

func deletePolicyRoute(fwMark int, ip string) error {
	ipAddr := net.ParseIP(ip)
	if ipAddr == nil {
		return errInvalidIP
	}

	err := netlink.RuleDel(getRule(fwMark, ipAddr))
	if err != nil {
		return fmt.Errorf("failed to RuleDel: %w", err)
	}

	err = netlink.RouteDel(getRoute(fwMark, ipAddr))
	if err != nil {
		return fmt.Errorf("failed to RouteDel: %w", err)
	}

	return nil
}

func getRoute(tableID int, ip net.IP) *netlink.Route {
	return &netlink.Route{
		Gw:    ip,
		Table: tableID,
	}
}

func getRule(fwMark int, ip net.IP) *netlink.Rule {
	rule := netlink.NewRule()
	rule.Table = fwMark
	rule.Mark = uint32(fwMark)
	rule.Family = netlink.FAMILY_V6

	if ip.To4() != nil {
		rule.Family = netlink.FAMILY_V4
	}

	return rule
}

func cleanNeighbor(ip net.IP) error {
	neighbors, err := netlink.NeighList(0, 0)
	if err != nil {
		return fmt.Errorf("failed to NeighList: %w", err)
	}

	for _, neighbor := range neighbors {
		if neighbor.IP.Equal(ip) {
			currentNeighbor := neighbor

			err = netlink.NeighDel(&currentNeighbor)
			if err != nil {
				return fmt.Errorf("failed to NeighDel: %w", err)
			}
		}
	}

	return nil
}
