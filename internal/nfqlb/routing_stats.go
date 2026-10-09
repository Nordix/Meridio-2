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

package nfqlb

import (
	"fmt"

	"github.com/vishvananda/netlink"
)

// IP family labels for policy-route counts, matching the sidecar's ip_family label values so
// the LB and sidecar metrics agree on family naming.
const (
	ipFamilyV4 = "IPv4"
	ipFamilyV6 = "IPv6"
)

// ruleListFunc is the seam for listing policy rules; overridable in tests. Defaults to the real
// netlink call. Kept separate from the ensureRule/CleanupStaleRules call sites so the read path
// can be faked without a kernel.
var ruleListFunc = netlink.RuleList

// PolicyRouteCounts returns the number of LB-owned policy routing rules currently installed,
// grouped by IP family (ipFamilyV4 / ipFamilyV6). It is the read side of createPolicyRoute,
// used by the LB metrics collector to expose <prefix>_lb_policy_routes.
//
// "LB-owned" uses the same predicate CleanupStaleRules uses: a rule with Mark >= startingOffset
// (and Mark > 0), where the fwmark doubles as the routing table id. Rules are listed across both
// families (FAMILY_ALL) and bucketed by the rule's own Family field.
//
// Both families are always present in the returned map (0 when none), so a configured-but-empty
// family emits ...{ip_family=...}=0 rather than being omitted — matching the sidecar nexthops
// convention where "present but empty" stays distinguishable from "absent".
func PolicyRouteCounts(startingOffset int) (map[string]int, error) {
	rules, err := ruleListFunc(netlink.FAMILY_ALL)
	if err != nil {
		return nil, fmt.Errorf("failed to list policy rules: %w", err)
	}

	counts := map[string]int{ipFamilyV4: 0, ipFamilyV6: 0}
	for i := range rules {
		rule := &rules[i]
		if rule.Mark < uint32(startingOffset) || rule.Mark == 0 {
			continue // not an LB-owned fwmark rule
		}
		switch rule.Family {
		case netlink.FAMILY_V4:
			counts[ipFamilyV4]++
		case netlink.FAMILY_V6:
			counts[ipFamilyV6]++
		}
	}
	return counts, nil
}
