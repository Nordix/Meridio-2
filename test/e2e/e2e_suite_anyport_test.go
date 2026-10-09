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
	"os/exec"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	e2eutils "github.com/nordix/meridio-2/test/e2e/utils"
	"github.com/nordix/meridio-2/test/utils"
)

// "any" port conversion (issue #262).
//
// Verifies that an L34Route using the documented "any" port spelling actually
// programs a working all-ports match in the data plane - not just that it is
// admitted. Historically "any" was passed unchanged to nfqlb, whose numeric
// port parser rejected it, so the flow failed to program silently. The fix
// converts "any" -> "0-65535" in the LB controller's flow adapter
// (anyPortToExplicitRange), so nfqlb receives a full-range set and omits the
// port filter (matching all ports).
//
// This reuses the already-deployed ipv4-simple infrastructure (VIP 40.0.0.1,
// route-m1, TCP on port 5000). It is a standalone Ordered Describe that patches
// route-m1's destinationPorts to ["any"] and restores the baseline
// (["5000-5001"]) in DeferCleanup, so it neither depends on nor perturbs the
// other ipv4 specs. Serial ensures it does not run concurrently with them.
var _ = Describe("Any Port Conversion", Label("ipv4"), Serial, Ordered, func() {
	const (
		namespace = "e2e-ipv4-simple"
		routeName = "route-m1"
		dgName    = "dg-m1"
		vip       = "40.0.0.1"
		tcpPort   = 5000
		// baselinePorts is route-m1's default destinationPorts (see
		// suites/ipv4-simple/routing.yaml). Restored on cleanup so sibling ipv4
		// specs (and the Low MTU / Traffic Exclusion suites sharing this route)
		// see a clean state.
		baselinePorts = `["5000-5001"]`
	)

	// patchDestinationPorts replaces route-m1's destinationPorts with the given
	// JSON array value (e.g. `["any"]`).
	patchDestinationPorts := func(portsJSON string) error {
		_, err := utils.Run(exec.Command("kubectl", "patch", "l34route", routeName,
			"-n", namespace, "--type=merge",
			"-p", `{"spec":{"destinationPorts":`+portsJSON+`}}`))
		return err
	}

	BeforeAll(func() {
		By("verifying the VIP is reachable before the test")
		Eventually(func() error { return e2eutils.Ping(vip) }).
			WithTimeout(30 * time.Second).WithPolling(2 * time.Second).Should(Succeed())

		By("waiting for the DistributionGroup to be Ready")
		Eventually(func(g Gomega) {
			out, err := utils.Run(exec.Command("kubectl", "get", "distg", dgName, "-n", namespace,
				"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}"))
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(out).To(Equal("True"), "%s should be Ready", dgName)
		}).WithTimeout(120 * time.Second).WithPolling(2 * time.Second).Should(Succeed())

		// Always restore the baseline destinationPorts so later specs see the
		// original route, even if an assertion below fails mid-sequence.
		DeferCleanup(func() {
			By("restoring route-m1 destinationPorts to the baseline")
			_ = patchDestinationPorts(baselinePorts)
		})
	})

	It("programs a working all-ports match when destinationPorts is \"any\"", func() {
		By(`patching route-m1 destinationPorts to ["any"]`)
		Expect(patchDestinationPorts(`["any"]`)).To(Succeed())

		By("sending TCP traffic and asserting it is served with zero loss")
		// Poll: the LB needs a moment to observe the route change and reprogram
		// its NFQLB flow. Once the "any" match is programmed, traffic to the VIP
		// must be served (non-empty host set) with no connection loss. Before the
		// fix, nfqlb would reject the literal "any" and the flow would not
		// program, so traffic would be lost.
		Eventually(func(g Gomega) {
			lastingConn, lostConn, err := e2eutils.SendTraffic(vip, tcpPort, "tcp", 100)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(lostConn).To(BeZero(), "no connections should be lost with an \"any\" port match")
			g.Expect(len(lastingConn)).To(BeNumerically(">", 0),
				"traffic should be served by at least one target (got: %v)", lastingConn)
		}).WithTimeout(60 * time.Second).WithPolling(3 * time.Second).Should(Succeed())
	})
})
