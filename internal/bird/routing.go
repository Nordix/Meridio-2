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

// Config-error reason label values for ConfigErrorRecorder. Each maps to a distinct netlink
// object the router manages for VIP source-based routing, so a failure points at a specific
// diagnosis:
//   - rule: policy-routing rule add/delete (RuleAdd/RuleDel) for the BIRD and blackhole tables.
//   - route: blackhole default route installation (RouteReplace) in the blackhole table.
//
// These live here rather than in internal/metrics/router (where the sidecar keeps its equivalent)
// because they are consumed at the failure sites in this package, which cannot import that one:
// internal/metrics/router imports internal/bird for the collector, so the reverse direction is an
// import cycle. The metrics package references these, keeping a single definition.
const (
	ReasonRule  = "rule"
	ReasonRoute = "route"
)

// ConfigErrorRecorder records failed netlink operations by reason (see the Reason* constants
// above for the label values). Satisfied by *router.ConfigErrors from internal/metrics/router.
//
// Declared as an interface so this package stays free of a dependency on the metrics package,
// matching how internal/controller/sidecar declares its own configErrorRecorder.
type ConfigErrorRecorder interface {
	Inc(reason string)
}

// recordConfigError increments rec for reason, tolerating a nil recorder.
//
// Both nil forms must be handled, which is why this exists rather than calling Inc directly:
// a typed-nil *router.ConfigErrors is nil-safe through its own receiver check, but Config.ConfigErrors
// left unset is a *bare-nil interface* with no receiver to dispatch on, and calling Inc on it
// panics. Since ConfigErrors is an optional field on a general-purpose config struct, omitting it
// must stay safe: a metrics counter is not worth crashing the routing path for.
func recordConfigError(rec ConfigErrorRecorder, reason string) {
	if rec == nil {
		return
	}
	rec.Inc(reason)
}

// setPolicyRoutes creates source-based routing rules for VIPs.
// Traffic from VIP addresses will use the BIRD routing table (tableID),
// with a fallback to blackhole table (tableID+1) if no BGP routes exist.
// Errors are accumulated best-effort (partial progress over rollback);
// the next reconcile retries any failed operations.
func setPolicyRoutes(nl abstractNetlink, vips []string, tableID, priority int, rec ConfigErrorRecorder) error {
	blackholeTableID := tableID + 1
	blackholePriority := priority + 1

	// Setup blackhole routes as fallback
	if err := setupBlackholeRoutes(nl, blackholeTableID, rec); err != nil {
		return err
	}

	log.V(1).Info("setting policy routes", "vipCount", len(vips))

	// Get existing BGP table rules
	bgpRules, err := nl.RuleListFiltered(netlink.FAMILY_ALL, &netlink.Rule{
		Table: tableID,
	}, netlink.RT_FILTER_TABLE)
	if err != nil {
		return fmt.Errorf("failed to list BGP rules: %w", err)
	}

	// Get existing blackhole table rules
	blackholeRules, err := nl.RuleListFiltered(netlink.FAMILY_ALL, &netlink.Rule{
		Table: blackholeTableID,
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
			if err := nl.RuleDel(&rule); err != nil {
				recordConfigError(rec, ReasonRule)
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
			if err := nl.RuleDel(&rule); err != nil {
				recordConfigError(rec, ReasonRule)
				errFinal = errors.Join(errFinal, err)
			}
		} else {
			delete(needBlackholeRule, rule.Src.String())
		}
	}

	// Add new BGP rules
	for _, ipNet := range needBgpRule {
		bgpRule := netlink.NewRule()
		bgpRule.Priority = priority
		bgpRule.Table = tableID
		bgpRule.Src = ipNet
		log.V(1).Info("adding BGP rule", "src", ipNet.String())
		if err := nl.RuleAdd(bgpRule); err != nil {
			recordConfigError(rec, ReasonRule)
			errFinal = errors.Join(errFinal, err)
		}
	}

	// Add new blackhole rules
	for _, ipNet := range needBlackholeRule {
		blackholeRule := netlink.NewRule()
		blackholeRule.Priority = blackholePriority
		blackholeRule.Table = blackholeTableID
		blackholeRule.Src = ipNet
		log.V(1).Info("adding blackhole rule", "src", ipNet.String())
		if err := nl.RuleAdd(blackholeRule); err != nil {
			recordConfigError(rec, ReasonRule)
			errFinal = errors.Join(errFinal, err)
		}
	}

	return errFinal
}

// setupBlackholeRoutes adds blackhole default routes to the blackhole table.
// These act as a fallback when no BGP routes are available in the main table.
func setupBlackholeRoutes(nl abstractNetlink, blackholeTableID int, rec ConfigErrorRecorder) error {
	var errFinal error

	// IPv4 blackhole
	_, dst4, _ := net.ParseCIDR("0.0.0.0/0")
	route4 := &netlink.Route{
		Dst:   dst4,
		Table: blackholeTableID,
		Type:  unix.RTN_BLACKHOLE,
	}
	if err := nl.RouteReplace(route4); err != nil {
		recordConfigError(rec, ReasonRoute)
		errFinal = errors.Join(errFinal, fmt.Errorf("failed to add IPv4 blackhole: %w", err))
	}

	// IPv6 blackhole
	_, dst6, _ := net.ParseCIDR("::/0")
	route6 := &netlink.Route{
		Dst:   dst6,
		Table: blackholeTableID,
		Type:  unix.RTN_BLACKHOLE,
	}
	if err := nl.RouteReplace(route6); err != nil {
		recordConfigError(rec, ReasonRoute)
		errFinal = errors.Join(errFinal, fmt.Errorf("failed to add IPv6 blackhole: %w", err))
	}

	return errFinal
}
