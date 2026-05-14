//go:build e2e
// +build e2e

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
						lastingConn, lostConn, err := e2eutils.SendTraffic(gw.vip, 5000, "tcp", 100)
						Expect(err).NotTo(HaveOccurred())
						Expect(lostConn).To(BeZero())
						Expect(len(lastingConn)).To(Equal(gw.targets))
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

		})
	}
})
