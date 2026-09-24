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
	"strings"
	"time"

	e2eutils "github.com/nordix/meridio-2/test/e2e/utils"
	"github.com/nordix/meridio-2/test/utils"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var tcpaoTestCases = []suiteTestCase{
	{
		name:           "TCP-AO",
		namespace:      "e2e-tcp-ao",
		targetApp:      "target-tao",
		targetReplicas: 2,
		gateways: []gwTestCase{
			{name: "gw-t1", vip: "60.0.0.1", targets: 2, dgName: "dg-t1"},
			{name: "gw-t2", vip: "60.0.0.2", targets: 2, dgName: "dg-t2"},
		},
	},
}

// Example output:
//
// BIRD 3.2.2 ready.
// Name       Proto      Table      State  Since         Info
// NBR-gw-t1-router-v4 BGP        ---        up     15:10:56.444  Established
func parseSinceFromShowProtocols(showProtocols, gwName string) string {
	var since string
	for line := range strings.SplitSeq(showProtocols, "\n") {
		line = strings.TrimSpace(line)
		fields := strings.Fields(line)
		if len(fields) < 6 {
			continue
		}
		if strings.Contains(fields[0], gwName) {
			since = fields[4]
			break
		}
	}
	return since
}

var _ = Describe("E2E TCP-AO Test Suite", Label("ipv4"), func() {
	SetDefaultEventuallyTimeout(5 * time.Minute)
	SetDefaultEventuallyPollingInterval(2 * time.Second)

	for _, suite := range tcpaoTestCases {
		suite := suite
		Describe(suite.name, Ordered, func() {
			Context("Deployment for TCP-AO", func() {
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

					It(fmt.Sprintf("should have %s Programmed", gw.name), func() {
						Eventually(func(g Gomega) {
							cmd := exec.Command("kubectl", "get", "gateway", gw.name, "-n", suite.namespace,
								"-o", "jsonpath={.status.conditions[?(@.type=='Programmed')].status}")
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

					It(fmt.Sprintf("should have %s LB Pod containers ready", gw.name), func() {
						Eventually(func(g Gomega) {
							cmd := exec.Command("kubectl", "get", "pods", "-n", suite.namespace,
								"-l", fmt.Sprintf("gateway.networking.k8s.io/gateway-name=%s", gw.name),
								"-o", "jsonpath={.items[*].status.containerStatuses[*].ready}")
							out, err := utils.Run(cmd)
							g.Expect(err).NotTo(HaveOccurred())
							g.Expect(out).NotTo(ContainSubstring("false"), "all containers should be ready")
						}).Should(Succeed())
					})
				}
			})

			Context("BGP configuration ready with TCP-AO for Bird", func() {
				for _, gw := range suite.gateways {
					gw := gw
					It(fmt.Sprintf("should have TCP-AO configured in BIRD for %s", gw.name), func() {
						Eventually(func(g Gomega) {
							// Get pod name first since kubectl exec doesn't support -l
							cmd := exec.Command("kubectl", "get", "pods", "-n", suite.namespace,
								"-l", fmt.Sprintf("gateway.networking.k8s.io/gateway-name=%s", gw.name),
								"-o", "jsonpath={.items[0].metadata.name}")
							podName, err := utils.Run(cmd)
							g.Expect(err).NotTo(HaveOccurred())
							g.Expect(podName).NotTo(BeEmpty())

							cmd = exec.Command("kubectl", "exec", "-n", suite.namespace,
								podName, "-c", "router", "--",
								"cat", "/etc/bird/bird.conf")
							out, err := utils.Run(cmd)
							g.Expect(err).NotTo(HaveOccurred())
							g.Expect(out).To(ContainSubstring("authentication ao;"))
							g.Expect(out).To(ContainSubstring("algorithm hmac sha256;"))
						}).Should(Succeed())
					})
					It(fmt.Sprintf("BGP is Up %s", gw.name), func() {
						Eventually(func(g Gomega) {
							// Get pod name first since kubectl exec doesn't support -l
							cmd := exec.Command("kubectl", "get", "pods", "-n", suite.namespace,
								"-l", fmt.Sprintf("gateway.networking.k8s.io/gateway-name=%s", gw.name),
								"-o", "jsonpath={.items[0].metadata.name}")
							podName, err := utils.Run(cmd)
							g.Expect(err).NotTo(HaveOccurred())
							g.Expect(podName).NotTo(BeEmpty())

							cmd = exec.Command("kubectl", "exec", "-n", suite.namespace,
								podName, "-c", "router", "--",
								"birdc", "-s", "/var/run/bird/bird.ctl", "show",
								"protocols")
							out, err := utils.Run(cmd)
							g.Expect(err).NotTo(HaveOccurred())
							g.Expect(out).To(ContainSubstring(gw.name))
							g.Expect(out).To(ContainSubstring("Established"))
						}).Should(Succeed())
					})
				}
			})

			Context("Traffic flows through TCP-AO authenticated sessions", func() {
				for _, gw := range suite.gateways {
					gw := gw
					It(fmt.Sprintf("should reach %s VIP via ping", gw.name), func() {
						Eventually(func() error { return e2eutils.Ping(gw.vip) }).Should(Succeed())
					})

					It(fmt.Sprintf("should distribute %s TCP traffic across targets", gw.name), func() {
						Eventually(func(g Gomega) {
							lastingConn, lostConn, err := e2eutils.SendTraffic(gw.vip, 5000, "tcp", 100)
							g.Expect(err).NotTo(HaveOccurred())
							g.Expect(lostConn).To(BeZero())
							g.Expect(len(lastingConn)).To(Equal(gw.targets))
						}).Should(Succeed())
					})
				}
			})

			Context("TCP-AO enforcement (negative test)", func() {
				It("should lose BGP session when Secret has wrong key", func() {
					gw := suite.gateways[0]

					By("patching the Secret with a wrong key")
					cmd := exec.Command("kubectl", "patch", "secret", "bgp-tcpao-secret",
						"-n", suite.namespace, "--type=merge",
						"-p", `{"stringData":{"master-key-1":"wrong-key-value"}}`)
					_, err := utils.Run(cmd)
					Expect(err).NotTo(HaveOccurred())

					By("waiting for BGP session to drop")
					Eventually(func(g Gomega) {
						cmd := exec.Command("kubectl", "get", "pods", "-n", suite.namespace,
							"-l", fmt.Sprintf("gateway.networking.k8s.io/gateway-name=%s", gw.name),
							"-o", "jsonpath={.items[0].metadata.name}")
						podName, err := utils.Run(cmd)
						g.Expect(err).NotTo(HaveOccurred())

						cmd = exec.Command("kubectl", "exec", "-n", suite.namespace,
							podName, "-c", "router", "--",
							"birdc", "-s", "/var/run/bird/bird.ctl", "show", "protocols")
						out, err := utils.Run(cmd)
						g.Expect(err).NotTo(HaveOccurred())
						g.Expect(out).NotTo(ContainSubstring("Established"))
					}).Should(Succeed())

					By("restoring the correct key")
					cmd = exec.Command("kubectl", "patch", "secret", "bgp-tcpao-secret",
						"-n", suite.namespace, "--type=merge",
						"-p", `{"stringData":{"master-key-1":"my-secure-master-key-string"}}`)
					_, err = utils.Run(cmd)
					Expect(err).NotTo(HaveOccurred())

					By("waiting for BGP session to re-establish")
					Eventually(func(g Gomega) {
						cmd := exec.Command("kubectl", "get", "pods", "-n", suite.namespace,
							"-l", fmt.Sprintf("gateway.networking.k8s.io/gateway-name=%s", gw.name),
							"-o", "jsonpath={.items[0].metadata.name}")
						podName, err := utils.Run(cmd)
						g.Expect(err).NotTo(HaveOccurred())

						cmd = exec.Command("kubectl", "exec", "-n", suite.namespace,
							podName, "-c", "router", "--",
							"birdc", "-s", "/var/run/bird/bird.ctl", "show", "protocols")
						out, err := utils.Run(cmd)
						g.Expect(err).NotTo(HaveOccurred())
						g.Expect(out).To(ContainSubstring("Established"))
					}).Should(Succeed())
				})
			})

			// A full TCP-AO key rotation requires both sides. However it could be a source of
			// unstability for other tests using the same BIRD instance as we do if we started
			// modifying the config of vpn-gateway's BIRD. We have two keys, none of them preferred
			// on the vpn-gateway side and we keep this config static. On the SLLBD side we start
			// with the same two keys, but the first being preferred. Then we patch the
			// gatewayrouter to prefer the second. This change leads to the `Current key` being
			// updated on the vpn-gateway side - which do not want to look at, because it's not
			// accessible via k8s. On the SLLBR side, this only updates `RNext key` - that's what we
			// assert for.
			Context("TCP-AO key rotation", func() {
				It("new preferred key should update RNext key", func() {
					gw := suite.gateways[0]

					cmd := exec.Command("kubectl", "get", "pods", "-n", suite.namespace,
						"-l", fmt.Sprintf("gateway.networking.k8s.io/gateway-name=%s", gw.name),
						"-o", "jsonpath={.items[0].metadata.name}")
					podName, err := utils.Run(cmd)
					Expect(err).NotTo(HaveOccurred())

					By("starting with the old RNext key")
					Eventually(func(g Gomega) {
						cmd = exec.Command("kubectl", "exec", "-n", suite.namespace,
							podName, "-c", "router", "--",
							"birdc", "-s", "/var/run/bird/bird.ctl", "show", "protocols", "all")
						out, err := utils.Run(cmd)
						g.Expect(err).NotTo(HaveOccurred())
						g.Expect(out).To(MatchRegexp(`RNext key:\s+1`))
					}).Should(Succeed())

					cmd = exec.Command("kubectl", "exec", "-n", suite.namespace,
						podName, "-c", "router", "--",
						"birdc", "-s", "/var/run/bird/bird.ctl", "show", "protocols", fmt.Sprintf(`"*%s*"`, gw.name))
					showBefore, err := utils.Run(cmd)
					Expect(err).NotTo(HaveOccurred())

					By("patching the GatewayRouter with a new currentKeyId")
					cmd = exec.Command("kubectl", "patch",
						"gatewayrouter.meridio-2.nordix.org/gw-t1-router-v4",
						"-n", suite.namespace, "--type=merge",
						"-p", `{"spec":{"bgp":{"authentication":{"currentKeyId":2}}}}`)
					_, err = utils.Run(cmd)
					Expect(err).NotTo(HaveOccurred())

					By("waiting for new RNext key")
					Eventually(func(g Gomega) {
						cmd = exec.Command("kubectl", "exec", "-n", suite.namespace,
							podName, "-c", "router", "--",
							"birdc", "-s", "/var/run/bird/bird.ctl", "show", "protocols", "all")
						out, err := utils.Run(cmd)
						g.Expect(err).NotTo(HaveOccurred())
						g.Expect(out).To(MatchRegexp(`RNext key:\s+2`))
					}).Should(Succeed())

					By("BGP is still up")
					Eventually(func(g Gomega) {
						cmd = exec.Command("kubectl", "exec", "-n", suite.namespace,
							podName, "-c", "router", "--",
							"birdc", "-s", "/var/run/bird/bird.ctl", "show", "protocols", fmt.Sprintf(`"*%s*"`, gw.name))
						out, err := utils.Run(cmd)
						g.Expect(err).NotTo(HaveOccurred())
						g.Expect(out).To(ContainSubstring("Established"))
					}).Should(Succeed())

					cmd = exec.Command("kubectl", "exec", "-n", suite.namespace,
						podName, "-c", "router", "--",
						"birdc", "-s", "/var/run/bird/bird.ctl", "show", "protocols", fmt.Sprintf(`"*%s*"`, gw.name))
					showAfter, err := utils.Run(cmd)
					Expect(err).NotTo(HaveOccurred())

					By("BGP session was not re-established")
					sinceBefore := parseSinceFromShowProtocols(showBefore, gw.name)
					sinceAfter := parseSinceFromShowProtocols(showAfter, gw.name)
					Expect(sinceBefore).NotTo(Equal(""))
					Expect(sinceAfter).NotTo(Equal(""))
					Expect(sinceAfter).To(Equal(sinceBefore))

				})
			})

		})
	}
})
