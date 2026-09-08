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
	"os/exec"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	e2eutils "github.com/nordix/meridio-2/test/e2e/utils"
	"github.com/nordix/meridio-2/test/utils"
)

var lowMTUTestCase = suiteTestCase{
	name:      "Low MTU",
	namespace: "e2e-ipv4-simple",
	targetApp: "target-m",
	gateways: []gwTestCase{
		{name: "gw-m1", vip: "40.0.0.1", targets: 2},
	},
}

// Low MTU suite: tests PMTU discovery with a 1200 MTU internal network.
// A 1400-byte ping (DF set) exceeds the 1200 MTU app network, so the LB must
// return ICMP Frag Needed with the VIP as source address.
var _ = Describe("E2E Low MTU", Label("ipv4"), func() {
	SetDefaultEventuallyTimeout(5 * time.Minute)
	SetDefaultEventuallyPollingInterval(2 * time.Second)

	suite := lowMTUTestCase

	Describe(suite.name, Ordered, func() {
		Context("Deployment", func() {
			for _, gw := range suite.gateways {
				gw := gw
				It(fmt.Sprintf("should have %s Accepted", gw.name), func() {
					Eventually(func(g Gomega) {
						cmd := exec.Command("kubectl", "get", "gateway", gw.name, "-n", suite.namespace,
							"-o", "jsonpath={.status.conditions[?(@.type=='Accepted')].status}")
						out, err := utils.Run(cmd)
						g.Expect(err).NotTo(HaveOccurred())
						g.Expect(out).To(Equal("True"))
					}).Should(Succeed())
				})

				It(fmt.Sprintf("should deploy LB Pod for %s", gw.name), func() {
					Eventually(func(g Gomega) {
						cmd := exec.Command("kubectl", "get", "pods", "-n", suite.namespace,
							"-l", fmt.Sprintf("gateway.networking.k8s.io/gateway-name=%s", gw.name),
							"-o", "jsonpath={.items[*].status.phase}")
						out, err := utils.Run(cmd)
						g.Expect(err).NotTo(HaveOccurred())
						g.Expect(out).To(ContainSubstring("Running"))
					}).Should(Succeed())
				})
			}
		})

		Context("Traffic", func() {
			BeforeAll(func() {
				By("waiting for BGP routes to propagate to VPN gateway")
				for _, gw := range suite.gateways {
					Eventually(func() error { return e2eutils.Ping(gw.vip) }).Should(Succeed())
				}
			})

			Context("ICMP reachability", func() {
				for _, gw := range suite.gateways {
					gw := gw
					It("handles ping on "+gw.name+" VIP", func() {
						Eventually(func() error { return e2eutils.Ping(gw.vip) }).
							WithTimeout(30 * time.Second).Should(Succeed())
					})
				}
			})

			Context("PMTU discovery", func() {
				for _, gw := range suite.gateways {
					gw := gw
					It("returns ICMP Frag Needed from VIP on "+gw.name+" (1400 bytes > 1200 MTU)", func() {
						// 1400 payload + 28 headers = 1428 > 1200 MTU.
						// LB must return ICMP Frag Needed with VIP as source (not LB pod IP).
						Expect(e2eutils.VerifyPMTU(gw.vip, 1400)).To(Succeed())
					})
				}
			})
		})
	})
})
