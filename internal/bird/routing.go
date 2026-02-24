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

package bird

import (
	"errors"
	"fmt"
	"net"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

var log = logf.Log.WithName("routing")

const (
	kernelTableID          = 4096 // BIRD-managed routing table
	blackholeKernelTableID = 4097 // Blackhole fallback routes
	rulePriority           = 100  // Priority for kernel table lookup
	blackholeRulePriority  = 101  // Priority for blackhole fallback
)

// setPolicyRoutes creates source-based routing rules for VIPs.
// Traffic from VIP addresses will use the BIRD routing table (4096),
// with a fallback to blackhole table (4097) if no BGP routes exist.
// Errors are accumulated best-effort (partial progress over rollback);
// the next reconcile retries any failed operations.
func setPolicyRoutes(vips []string) error {
	// Setup blackhole routes as fallback
	if err := setupBlackholeRoutes(); err != nil {
		return err
	}

	log.V(1).Info("setting policy routes", "vipCount", len(vips))

	// Get existing BGP table rules
	bgpRules, err := netlink.RuleListFiltered(netlink.FAMILY_ALL, &netlink.Rule{
		Table: kernelTableID,
	}, netlink.RT_FILTER_TABLE)
	if err != nil {
		return fmt.Errorf("failed to list BGP rules: %w", err)
	}

	// Get existing blackhole table rules
	blackholeRules, err := netlink.RuleListFiltered(netlink.FAMILY_ALL, &netlink.Rule{
		Table: blackholeKernelTableID,
	}, netlink.RT_FILTER_TABLE)
	if err != nil {
		return fmt.Errorf("failed to list blackhole rules: %w", err)
	}

	vipMap := make(map[string]*net.IPNet)
	for _, vip := range vips {
		_, ipNet, err := net.ParseCIDR(vip)
		if err != nil {
			return fmt.Errorf("failed to parse VIP CIDR %s: %w", vip, err)
		}
		vipMap[ipNet.String()] = ipNet
	}
	log.V(1).Info("parsed VIPs", "vipMap", vipMap)

	var errFinal error

	// Track which VIPs need new rules
	needBgpRule := make(map[string]*net.IPNet)
	needBlackholeRule := make(map[string]*net.IPNet)
	for k, v := range vipMap {
		needBgpRule[k] = v
		needBlackholeRule[k] = v
	}

	// Clean up old BGP rules
	for _, rule := range bgpRules {
		if _, exists := vipMap[rule.Src.String()]; !exists {
			log.V(1).Info("deleting BGP rule", "src", rule.Src.String())
			if err := netlink.RuleDel(&rule); err != nil {
				errFinal = errors.Join(errFinal, err)
			}
		} else {
			delete(needBgpRule, rule.Src.String())
		}
	}

	// Clean up old blackhole rules
	for _, rule := range blackholeRules {
		if _, exists := vipMap[rule.Src.String()]; !exists {
			log.V(1).Info("deleting blackhole rule", "src", rule.Src.String())
			if err := netlink.RuleDel(&rule); err != nil {
				errFinal = errors.Join(errFinal, err)
			}
		} else {
			delete(needBlackholeRule, rule.Src.String())
		}
	}

	// Add new BGP rules
	for _, ipNet := range needBgpRule {
		bgpRule := netlink.NewRule()
		bgpRule.Priority = rulePriority
		bgpRule.Table = kernelTableID
		bgpRule.Src = ipNet
		log.V(1).Info("adding BGP rule", "src", ipNet.String())
		if err := netlink.RuleAdd(bgpRule); err != nil {
			errFinal = errors.Join(errFinal, err)
		}
	}

	// Add new blackhole rules
	for _, ipNet := range needBlackholeRule {
		blackholeRule := netlink.NewRule()
		blackholeRule.Priority = blackholeRulePriority
		blackholeRule.Table = blackholeKernelTableID
		blackholeRule.Src = ipNet
		log.V(1).Info("adding blackhole rule", "src", ipNet.String())
		if err := netlink.RuleAdd(blackholeRule); err != nil {
			errFinal = errors.Join(errFinal, err)
		}
	}

	return errFinal
}

// setupBlackholeRoutes adds blackhole default routes to table 4097.
// These act as a fallback when no BGP routes are available in table 4096.
func setupBlackholeRoutes() error {
	var errFinal error

	// IPv4 blackhole
	_, dst4, _ := net.ParseCIDR("0.0.0.0/0")
	route4 := &netlink.Route{
		Dst:   dst4,
		Table: blackholeKernelTableID,
		Type:  unix.RTN_BLACKHOLE,
	}
	if err := netlink.RouteReplace(route4); err != nil {
		errFinal = errors.Join(errFinal, fmt.Errorf("failed to add IPv4 blackhole: %w", err))
	}

	// IPv6 blackhole
	_, dst6, _ := net.ParseCIDR("::/0")
	route6 := &netlink.Route{
		Dst:   dst6,
		Table: blackholeKernelTableID,
		Type:  unix.RTN_BLACKHOLE,
	}
	if err := netlink.RouteReplace(route6); err != nil {
		errFinal = errors.Join(errFinal, fmt.Errorf("failed to add IPv6 blackhole: %w", err))
	}

	return errFinal
}
