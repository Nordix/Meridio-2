//go:build e2e
// +build e2e

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

package e2e

import (
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	"github.com/onsi/ginkgo/v2/types"
)

// This file is the single registry for the e2e suite conventions described in
// CONVENTIONS.md: the label vocabulary, the global Eventually budgets, and the
// report-time check that enforces the labelling contract.

// IP-family labels. These select which CI runner (and therefore which Kind
// cluster configuration) a spec belongs to; see .github/workflows/e2e.yaml.
// A tree carries one label per address family it actually exercises.
const (
	labelIPv4      = "ipv4"
	labelIPv6      = "ipv6"
	labelDualStack = "dual-stack"
)

// suiteLabelPrefix marks a label as a suite selector. Every spec must carry
// exactly one label with this prefix; see verifySuiteLabels below.
const suiteLabelPrefix = "suite:"

// Suite labels — one per deployable topology, named after the Makefile target
// that deploys it, so `suite:<name>` always pairs with `make deploy-<name>`
// and `make test-<name>`.
//
// Several suites deliberately share a topology (for example low-mtu, bgp,
// non-default-bgp, resiliency, traffic-exclusion and any-port all reuse
// ipv4-simple). They still get their own suite label: the label identifies the
// spec tree to run, while the Makefile target maps it to the topology to
// deploy.
const (
	labelSuiteAnyPort                  = suiteLabelPrefix + "any-port"
	labelSuiteBFDDetection             = suiteLabelPrefix + "bfd-detection"
	labelSuiteBGP                      = suiteLabelPrefix + "bgp"
	labelSuiteClientV6                 = suiteLabelPrefix + "client-v6"
	labelSuiteDualStackSimple          = suiteLabelPrefix + "dual-stack-simple"
	labelSuiteLowMTU                   = suiteLabelPrefix + "low-mtu"
	labelSuiteMD5                      = suiteLabelPrefix + "md5"
	labelSuiteNodeDrain                = suiteLabelPrefix + "node-drain"
	labelSuiteNonDefaultBGP            = suiteLabelPrefix + "non-default-bgp"
	labelSuiteOpenShiftCRC             = suiteLabelPrefix + "openshift-crc"
	labelSuitePodCacheLabel            = suiteLabelPrefix + "pod-cache-label"
	labelSuiteReadinessGates           = suiteLabelPrefix + "readiness-gates"
	labelSuiteResiliency               = suiteLabelPrefix + "resiliency"
	labelSuiteRobustness               = suiteLabelPrefix + "robustness"
	labelSuiteSCTPMultihoming          = suiteLabelPrefix + "sctp-multihoming"
	labelSuiteScaling                  = suiteLabelPrefix + "scaling"
	labelSuiteSeparateAppnetworkV4     = suiteLabelPrefix + "separate-appnetwork-v4"
	labelSuiteSeparateAppnetworkV6     = suiteLabelPrefix + "separate-appnetwork-v6"
	labelSuiteSeparateStaticAppnetwork = suiteLabelPrefix + "separate-static-appnetwork"
	labelSuiteSharedAppnetwork         = suiteLabelPrefix + "shared-appnetwork"
	labelSuiteTCPAO                    = suiteLabelPrefix + "tcp-ao"
	labelSuiteTrafficExclusion         = suiteLabelPrefix + "traffic-exclusion"
)

// allSuiteLabels is the closed set of valid suite labels. Keep it in sync with
// the constants above; verifySuiteLabels rejects anything not listed here, so a
// typo in a Label() decorator fails the run instead of silently producing a
// spec that no Makefile target can select.
var allSuiteLabels = map[string]struct{}{
	labelSuiteAnyPort:                  {},
	labelSuiteBFDDetection:             {},
	labelSuiteBGP:                      {},
	labelSuiteClientV6:                 {},
	labelSuiteDualStackSimple:          {},
	labelSuiteLowMTU:                   {},
	labelSuiteMD5:                      {},
	labelSuiteNodeDrain:                {},
	labelSuiteNonDefaultBGP:            {},
	labelSuiteOpenShiftCRC:             {},
	labelSuitePodCacheLabel:            {},
	labelSuiteReadinessGates:           {},
	labelSuiteResiliency:               {},
	labelSuiteRobustness:               {},
	labelSuiteSCTPMultihoming:          {},
	labelSuiteScaling:                  {},
	labelSuiteSeparateAppnetworkV4:     {},
	labelSuiteSeparateAppnetworkV6:     {},
	labelSuiteSeparateStaticAppnetwork: {},
	labelSuiteSharedAppnetwork:         {},
	labelSuiteTCPAO:                    {},
	labelSuiteTrafficExclusion:         {},
}

// Global Eventually budgets, applied once in TestE2E.
//
// These are deliberately not set per container: Ginkgo executes every container
// body during tree construction, so a SetDefaultEventuallyTimeout call inside a
// Describe is a global, last-writer-wins mutation rather than a scoped setting.
// Suites that need a different budget state it at the assertion with
// .WithTimeout()/.WithPolling().
const (
	// defaultEventuallyTimeout covers worst-case control-plane convergence:
	// LB Deployment rollout, secondary network attachment, BGP session
	// establishment and nfqlb Maglev programming, on a loaded CI runner.
	defaultEventuallyTimeout = 5 * time.Minute
	// defaultEventuallyPolling keeps kubectl exec churn low while still
	// converging quickly.
	defaultEventuallyPolling = 2 * time.Second
)

// countOf renders a count and noun for spec names, pluralising the noun so the
// generated text stays grammatical at any count: "1 ENC", "2 ENCs". Without
// this, single-replica suites produced names like "should create 1 ENCs".
func countOf(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// verifySuiteLabels enforces the labelling contract from CONVENTIONS.md on
// every spec that actually ran: exactly one label from allSuiteLabels.
//
// Without this, a tree missing its suite label still runs under the IP-family
// filter in CI but cannot be selected by any `make test-<name>` target — a
// discrepancy that is invisible until someone tries to reproduce a CI failure
// locally.
var _ = ReportAfterSuite("[conventions] every spec carries exactly one suite label", func(report Report) {
	var violations []string

	for _, spec := range report.SpecReports {
		if spec.LeafNodeType != types.NodeTypeIt {
			continue
		}

		var found []string
		for _, label := range spec.Labels() {
			if !strings.HasPrefix(label, suiteLabelPrefix) {
				continue
			}
			if _, ok := allSuiteLabels[label]; !ok {
				violations = append(violations, fmt.Sprintf(
					"%s: unknown suite label %q (add it to allSuiteLabels)", spec.FullText(), label))
				continue
			}
			found = append(found, label)
		}

		switch len(found) {
		case 1:
		case 0:
			violations = append(violations, fmt.Sprintf(
				"%s: missing a %s<name> label", spec.FullText(), suiteLabelPrefix))
		default:
			violations = append(violations, fmt.Sprintf(
				"%s: carries %d suite labels (%s), expected exactly 1",
				spec.FullText(), len(found), strings.Join(found, ", ")))
		}
	}

	if len(violations) > 0 {
		Fail(fmt.Sprintf("spec labelling violates test/e2e/CONVENTIONS.md:\n  %s",
			strings.Join(violations, "\n  ")))
	}
})
