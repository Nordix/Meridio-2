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

	e2eutils "github.com/nordix/meridio-2/test/e2e/utils"
	"github.com/nordix/meridio-2/test/utils"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// md5TestCases drives the BGP MD5 (RFC2385) e2e suite.
//
// Deprecated: MD5 is a weak, legacy mechanism superseded by TCP-AO (RFC5925).
// This suite exists to guard the deprecated MD5 path, not to endorse its use.
var md5TestCases = []suiteTestCase{
	{
		name:           "MD5",
		namespace:      "e2e-md5",
		targetApp:      "target-md5",
		targetReplicas: 2,
		gateways: []gwTestCase{
			{name: "gw-md1", vip: "80.0.0.1", targets: 2, dgName: "dg-md1"},
			{name: "gw-md2", vip: "80.0.0.2", targets: 2, dgName: "dg-md2"},
		},
	},
}

// Deprecated: this suite exercises the deprecated BGP MD5 (RFC2385) mechanism.
var _ = Describe("E2E MD5 Test Suite", Label("ipv4"), func() {
	SetDefaultEventuallyTimeout(5 * time.Minute)
	SetDefaultEventuallyPollingInterval(2 * time.Second)

	for _, suite := range md5TestCases {
		suite := suite
		Describe(suite.name, Ordered, func() {
			Context("Deployment for MD5", func() {
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

			Context("BGP configuration ready with MD5 for Bird", func() {
				for _, gw := range suite.gateways {
					gw := gw
					It(fmt.Sprintf("should have MD5 configured in BIRD for %s", gw.name), func() {
						Eventually(func(g Gomega) {
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
							g.Expect(out).To(ContainSubstring(`password "my-secure-md5-key-string";`))
						}).Should(Succeed())
					})
					It(fmt.Sprintf("BGP is Up %s", gw.name), func() {
						Eventually(func(g Gomega) {
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

			Context("Traffic flows through MD5 authenticated sessions", func() {
				for _, gw := range suite.gateways {
					gw := gw
					It(fmt.Sprintf("should reach %s VIP via ping", gw.name), func() {
						Eventually(func() error { return e2eutils.Ping(gw.vip) }).Should(Succeed())
					})

					It(fmt.Sprintf("should distribute %s TCP traffic across targets", gw.name), func() {
						lastingConn, lostConn, err := e2eutils.SendTraffic(gw.vip, 5000, "tcp", 100)
						Expect(err).NotTo(HaveOccurred())
						Expect(lostConn).To(BeZero())
						Expect(len(lastingConn)).To(Equal(gw.targets))
					})
				}
			})

			Context("MD5 enforcement (negative test)", func() {
				It("should lose BGP session when Secret has wrong key", func() {
					gw := suite.gateways[0]

					By("patching the Secret with a wrong key")
					cmd := exec.Command("kubectl", "patch", "secret", "bgp-md5-secret",
						"-n", suite.namespace, "--type=merge",
						"-p", `{"stringData":{"md5-key":"wrong-key-value"}}`)
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
					cmd = exec.Command("kubectl", "patch", "secret", "bgp-md5-secret",
						"-n", suite.namespace, "--type=merge",
						"-p", `{"stringData":{"md5-key":"my-secure-md5-key-string"}}`)
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
		})
	}
})
