/*
Copyright (c) 2024 OpenInfra Foundation Europe

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

// createPolicyRoute creates a new policy route based on the fowarding mark.
// If the policy route if already existing and correspond to the parameters,
// nothing will happen, otherwise the previous one will be deleted.
func createPolicyRoute(fwMark int, ip string) error {
	ipAddr := net.ParseIP(ip)
	if ipAddr == nil {
		return errInvalidIP
	}

	if validPolicyRoute(fwMark, ipAddr) {
		return nil
	}

	_ = deletePolicyRoute(fwMark, ip)
	_ = cleanNeighbor(ipAddr)

	err := netlink.RuleAdd(getRule(fwMark, ipAddr))
	if err != nil {
		return fmt.Errorf("failed to RuleAdd: %w", err)
	}

	err = netlink.RouteAdd(getRoute(fwMark, ipAddr))
	if err != nil {
		return fmt.Errorf("failed to RouteAdd: %w", err)
	}

	return nil
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

// todo: valid rule
func validPolicyRoute(fwMark int, ip net.IP) bool {
	family := netlink.FAMILY_V6

	if ip.To4() != nil {
		family = netlink.FAMILY_V4
	}

	routes, err := netlink.RouteListFiltered(family, getRoute(fwMark, ip), netlink.RT_FILTER_GW|netlink.RT_FILTER_TABLE)
	if err != nil {
		return false
	}

	if len(routes) != 1 || !routes[0].Gw.Equal(ip) {
		return false
	}

	return true
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
