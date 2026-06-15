//go:build e2e
// +build e2e

package e2e

import (
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	e2eutils "github.com/nordix/meridio-2/test/e2e/utils"
	"github.com/nordix/meridio-2/test/utils"
)

var dualStackTestCase = suiteTestCase{
	name:           "Dual Stack",
	namespace:      "e2e-dual-stack",
	targetApp:      "target-ds",
	targetReplicas: 2,
	gateways: []gwTestCase{
		{name: "gw-ds", vip: "10.0.0.1", targets: 2, dgName: "dg-ds"},
	},
}

var _ = Describe("Dual Stack", Label("dual-stack"), Ordered, func() {
	SetDefaultEventuallyTimeout(5 * time.Minute)
	SetDefaultEventuallyPollingInterval(2 * time.Second)

	suite := dualStackTestCase

	Context("Deployment", func() {
		It("should have Gateway Accepted", func() {
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "gateway", "gw-ds", "-n", suite.namespace,
					"-o", "jsonpath={.status.conditions[?(@.type=='Accepted')].status}")
				out, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(out).To(Equal("True"))
			}).Should(Succeed())
		})

		It("should have Gateway Programmed", func() {
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "gateway", "gw-ds", "-n", suite.namespace,
					"-o", "jsonpath={.status.conditions[?(@.type=='Programmed')].status}")
				out, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(out).To(Equal("True"))
			}).Should(Succeed())
		})

		It("should have Gateway status.addresses with both IPv4 and IPv6 VIPs", func() {
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "gateway", "gw-ds", "-n", suite.namespace,
					"-o", "jsonpath={.status.addresses[*].value}")
				out, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(out).To(ContainSubstring("10.0.0.1"), "should have IPv4 VIP")
				g.Expect(out).To(ContainSubstring("fd00:cafe:1::1"), "should have IPv6 VIP")
			}).Should(Succeed())
		})

		It("should deploy LB Pods", func() {
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pods", "-n", suite.namespace,
					"-l", "gateway.networking.k8s.io/gateway-name=gw-ds",
					"-o", "jsonpath={.items[*].status.phase}")
				out, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(out).To(ContainSubstring("Running"))
			}).Should(Succeed())
		})

		It("should have DistributionGroup Ready", func() {
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "distg", "dg-ds", "-n", suite.namespace,
					"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}")
				out, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(out).To(Equal("True"))
			}).Should(Succeed())
		})

		It("should create dual-stack EndpointSlices with shared Maglev IDs", func() {
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "endpointslice", "-n", suite.namespace,
					"-l", "meridio-2.nordix.org/distribution-group=dg-ds",
					"-o", "json")
				out, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())

				var result struct {
					Items []struct {
						AddressType string `json:"addressType"`
						Endpoints   []struct {
							Addresses []string `json:"addresses"`
							Zone      *string  `json:"zone"`
						} `json:"endpoints"`
					} `json:"items"`
				}
				err = utils.ParseJSON(out, &result)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(result.Items).To(HaveLen(2), "should have IPv4 and IPv6 EndpointSlices")

				// Collect Maglev IDs per address type
				maglevIDs := map[string][]string{"IPv4": {}, "IPv6": {}}
				for _, slice := range result.Items {
					for _, ep := range slice.Endpoints {
						g.Expect(ep.Zone).NotTo(BeNil())
						g.Expect(*ep.Zone).To(MatchRegexp(`^maglev:\d+$`))
						maglevIDs[slice.AddressType] = append(maglevIDs[slice.AddressType], *ep.Zone)
					}
				}

				// Verify both families have same Maglev IDs (shared allocation)
				g.Expect(maglevIDs["IPv4"]).To(Equal(maglevIDs["IPv6"]),
					"IPv4 and IPv6 EndpointSlices should have same Maglev IDs")
			}).Should(Succeed())
		})

		It("should have target Pods with dual-stack secondary IPs", func() {
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pods", "-n", suite.namespace,
					"-l", "app=target-ds", "-o", "json")
				out, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())

				var result struct {
					Items []struct {
						Metadata struct {
							Name        string            `json:"name"`
							Annotations map[string]string `json:"annotations"`
						} `json:"metadata"`
					} `json:"items"`
				}
				err = utils.ParseJSON(out, &result)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(result.Items).To(HaveLen(suite.targetReplicas))

				for _, pod := range result.Items {
					netStatus := pod.Metadata.Annotations["k8s.v1.cni.cncf.io/network-status"]
					g.Expect(netStatus).To(ContainSubstring("169.111.103."), "should have IPv4 on net1")
					g.Expect(netStatus).To(ContainSubstring("fd00:cafe:103::"), "should have IPv6 on net1")
				}
			}).Should(Succeed())
		})

		It("should have ENCs Ready", func() {
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "enc", "-n", suite.namespace,
					"-o", "jsonpath={range .items[*]}{.status.conditions[?(@.type=='Ready')].status}{\"\\n\"}{end}")
				out, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				lines := utils.GetNonEmptyLines(out)
				g.Expect(len(lines)).To(Equal(suite.targetReplicas))
				for _, status := range lines {
					g.Expect(status).To(Equal("True"))
				}
			}).Should(Succeed())
		})

		It("should have VIPs assigned on target Pods for both IP families", func() {
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pods", "-n", suite.namespace,
					"-l", "app=target-ds", "--field-selector=status.phase=Running",
					"-o", "jsonpath={.items[0].metadata.name}")
				targetPod, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(targetPod).NotTo(BeEmpty())

				cmd = exec.Command("kubectl", "exec", "-n", suite.namespace, strings.TrimSpace(targetPod),
					"-c", "example-target", "--", "ip", "addr", "show", "net1")
				out, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(out).To(ContainSubstring("10.0.0.1/32"), "should have IPv4 VIP")
				g.Expect(out).To(ContainSubstring("fd00:cafe:1::1/128"), "should have IPv6 VIP")
			}).Should(Succeed())
		})

		It("should have source-based routing rules for both VIPs", func() {
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pods", "-n", suite.namespace,
					"-l", "app=target-ds", "--field-selector=status.phase=Running",
					"-o", "jsonpath={.items[0].metadata.name}")
				targetPod, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				targetPod = strings.TrimSpace(targetPod)

				// Check IPv4 rule
				cmd = exec.Command("kubectl", "exec", "-n", suite.namespace, targetPod,
					"-c", "example-target", "--", "ip", "rule", "show")
				out, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(out).To(ContainSubstring("from 10.0.0.1 lookup"), "should have IPv4 source routing rule")

				// Check IPv6 rule
				cmd = exec.Command("kubectl", "exec", "-n", suite.namespace, targetPod,
					"-c", "example-target", "--", "ip", "-6", "rule", "show")
				out, err = utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(out).To(ContainSubstring("from fd00:cafe:1::1 lookup"), "should have IPv6 source routing rule")
			}).Should(Succeed())
		})

		It("should have ECMP routes to LB pods in both IP families", func() {
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pods", "-n", suite.namespace,
					"-l", "app=target-ds", "--field-selector=status.phase=Running",
					"-o", "jsonpath={.items[0].metadata.name}")
				targetPod, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				targetPod = strings.TrimSpace(targetPod)

				// Extract table ID from IPv4 rule
				cmd = exec.Command("kubectl", "exec", "-n", suite.namespace, targetPod,
					"-c", "example-target", "--", "ip", "rule", "show")
				ruleOut, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())

				var tableID string
				for _, line := range strings.Split(ruleOut, "\n") {
					if strings.Contains(line, "10.0.0.1") && strings.Contains(line, "lookup") {
						fields := strings.Fields(line)
						for i, f := range fields {
							if f == "lookup" && i+1 < len(fields) {
								tableID = fields[i+1]
								break
							}
						}
					}
				}
				g.Expect(tableID).NotTo(BeEmpty(), "should find routing table ID")

				// Check IPv4 ECMP routes
				cmd = exec.Command("kubectl", "exec", "-n", suite.namespace, targetPod,
					"-c", "example-target", "--", "ip", "route", "show", "table", tableID)
				out, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(out).To(ContainSubstring("nexthop"), "should have ECMP route")
				// Count nexthops (should be 2 for 2 LB pods)
				nexthopCount := strings.Count(out, "nexthop")
				g.Expect(nexthopCount).To(Equal(2), "should have 2 IPv4 next-hops")

				// Check IPv6 ECMP routes
				cmd = exec.Command("kubectl", "exec", "-n", suite.namespace, targetPod,
					"-c", "example-target", "--", "ip", "-6", "route", "show", "table", tableID)
				out, err = utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(out).To(ContainSubstring("nexthop"), "should have IPv6 ECMP route")
				nexthopCount = strings.Count(out, "nexthop")
				g.Expect(nexthopCount).To(Equal(2), "should have 2 IPv6 next-hops")
			}).Should(Succeed())
		})

		It("should have LB pods with connectivity readiness gates", func() {
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pods", "-n", suite.namespace,
					"-l", "gateway.networking.k8s.io/gateway-name=gw-ds",
					"-o", "json")
				out, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())

				var result struct {
					Items []struct {
						Spec struct {
							ReadinessGates []struct {
								ConditionType string `json:"conditionType"`
							} `json:"readinessGates"`
						} `json:"spec"`
						Status struct {
							Conditions []struct {
								Type   string `json:"type"`
								Status string `json:"status"`
							} `json:"conditions"`
						} `json:"status"`
					} `json:"items"`
				}
				err = utils.ParseJSON(out, &result)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(result.Items).NotTo(BeEmpty())

				for _, pod := range result.Items {
					// Check readiness gates are declared
					gateTypes := []string{}
					for _, gate := range pod.Spec.ReadinessGates {
						gateTypes = append(gateTypes, gate.ConditionType)
					}
					g.Expect(gateTypes).To(ContainElement("meridio-2.nordix.org/ipv4-connectivity"),
						"should have IPv4 connectivity readiness gate")
					g.Expect(gateTypes).To(ContainElement("meridio-2.nordix.org/ipv6-connectivity"),
						"should have IPv6 connectivity readiness gate")

					// Check conditions are True
					for _, cond := range pod.Status.Conditions {
						if cond.Type == "meridio-2.nordix.org/ipv4-connectivity" ||
							cond.Type == "meridio-2.nordix.org/ipv6-connectivity" {
							g.Expect(cond.Status).To(Equal("True"),
								"readiness gate %s should be True", cond.Type)
						}
					}
				}
			}).Should(Succeed())
		})
	})

	Context("Traffic", func() {
		BeforeAll(func() {
			By("waiting for BGP routes to propagate to VPN gateway")
			Eventually(func() error { return e2eutils.Ping("10.0.0.1") }).Should(Succeed())
			Eventually(func() error { return e2eutils.Ping("fd00:cafe:1::1") }).Should(Succeed())
		})

		Context("ICMP reachability", func() {
			It("handles IPv4 ping on VIP", func() {
				Eventually(func() error { return e2eutils.Ping("10.0.0.1") }).
					WithTimeout(30 * time.Second).Should(Succeed())
			})

			It("handles IPv6 ping on VIP", func() {
				Eventually(func() error { return e2eutils.Ping("fd00:cafe:1::1") }).
					WithTimeout(30 * time.Second).Should(Succeed())
			})
		})

		Context("IPv4 load balancing", func() {
			It("distributes TCP traffic across targets", func() {
				lastingConn, lostConn, err := e2eutils.SendTraffic("10.0.0.1", 5000, "tcp", 100)
				Expect(err).NotTo(HaveOccurred())
				Expect(lostConn).To(BeZero(), "no connections should be lost")
				Expect(len(lastingConn)).To(Equal(2), "expected 2 targets, got: %v", lastingConn)
			})

			It("distributes UDP traffic across targets", func() {
				lastingConn, lostConn, err := e2eutils.SendTraffic("10.0.0.1", 5001, "udp", 100)
				Expect(err).NotTo(HaveOccurred())
				Expect(lostConn).To(BeZero(), "no connections should be lost")
				Expect(len(lastingConn)).To(Equal(2), "expected 2 targets, got: %v", lastingConn)
			})
		})

		Context("IPv6 load balancing", func() {
			It("distributes TCP traffic across targets", func() {
				lastingConn, lostConn, err := e2eutils.SendTraffic("fd00:cafe:1::1", 5000, "tcp", 100)
				Expect(err).NotTo(HaveOccurred())
				Expect(lostConn).To(BeZero(), "no connections should be lost")
				Expect(len(lastingConn)).To(Equal(2), "expected 2 targets, got: %v", lastingConn)
			})

			It("distributes UDP traffic across targets", func() {
				lastingConn, lostConn, err := e2eutils.SendTraffic("fd00:cafe:1::1", 5001, "udp", 100)
				Expect(err).NotTo(HaveOccurred())
				Expect(lostConn).To(BeZero(), "no connections should be lost")
				Expect(len(lastingConn)).To(Equal(2), "expected 2 targets, got: %v", lastingConn)
			})
		})
	})
})
