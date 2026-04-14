//go:build e2e
// +build e2e

package e2e

import (
	"fmt"
	"os/exec"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/nordix/meridio-2/test/utils"
	e2eutils "github.com/nordix/meridio-2/test/e2e/utils"
)

type gwTestCase struct {
	name    string
	vip     string
	targets int
	dgName  string
}

type suiteTestCase struct {
	name           string
	namespace      string
	targetApp      string
	targetReplicas int
	gateways       []gwTestCase
}

var testCases = []suiteTestCase{
	{
		name:           "Common App Network",
		namespace:      "e2e-common-appnet",
		targetApp:      "target-b",
		targetReplicas: 2,
		gateways: []gwTestCase{
			{name: "gw-b1", vip: "30.0.0.1", targets: 2, dgName: "dg-b1"},
			{name: "gw-b2", vip: "30.0.0.2", targets: 2, dgName: "dg-b2"},
		},
	},
	{
		name:           "Separate App Network",
		namespace:      "e2e-separate-appnet",
		targetApp:      "target-a",
		targetReplicas: 2,
		gateways: []gwTestCase{
			{name: "gw-a1", vip: "20.0.0.1", targets: 2, dgName: "dg-a1"},
			{name: "gw-a2", vip: "20.0.0.2", targets: 2, dgName: "dg-a2"},
		},
	},
}

var _ = Describe("E2E Test Suites", func() {
	SetDefaultEventuallyTimeout(5 * time.Minute)
	SetDefaultEventuallyPollingInterval(2 * time.Second)

	for _, suite := range testCases {
		suite := suite
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

				It("should have Gateway status.addresses populated with VIPs", func() {
					for _, gw := range suite.gateways {
						gw := gw
						Eventually(func(g Gomega) {
							cmd := exec.Command("kubectl", "get", "gateway", gw.name, "-n", suite.namespace,
								"-o", "jsonpath={.status.addresses[*].value}")
							out, err := utils.Run(cmd)
							g.Expect(err).NotTo(HaveOccurred())
							g.Expect(out).To(ContainSubstring(gw.vip), "Gateway %s should have VIP %s in status", gw.name, gw.vip)
						}).Should(Succeed())
					}
				})

				It("should have DistributionGroups Ready", func() {
					for _, gw := range suite.gateways {
						gw := gw
						Eventually(func(g Gomega) {
							cmd := exec.Command("kubectl", "get", "distg", gw.dgName, "-n", suite.namespace,
								"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}")
							out, err := utils.Run(cmd)
							g.Expect(err).NotTo(HaveOccurred())
							g.Expect(out).To(Equal("True"), "%s should be Ready", gw.dgName)
						}).Should(Succeed())
					}
				})

				It(fmt.Sprintf("should create %d ENCs (one per target pod)", suite.targetReplicas), func() {
					Eventually(func(g Gomega) {
						cmd := exec.Command("kubectl", "get", "enc", "-n", suite.namespace,
							"-o", "jsonpath={range .items[*]}{.metadata.name}{\"\\n\"}{end}")
						out, err := utils.Run(cmd)
						g.Expect(err).NotTo(HaveOccurred())
						g.Expect(len(utils.GetNonEmptyLines(out))).To(Equal(suite.targetReplicas))
					}).Should(Succeed())
				})

				It("should create EndpointSlices with Maglev IDs for each DistributionGroup", func() {
					for _, gw := range suite.gateways {
						gw := gw
						Eventually(func(g Gomega) {
							// Get EndpointSlices for this DistributionGroup
							cmd := exec.Command("kubectl", "get", "endpointslice", "-n", suite.namespace,
								"-l", fmt.Sprintf("meridio-2.nordix.org/distribution-group=%s", gw.dgName),
								"-o", "json")
							out, err := utils.Run(cmd)
							g.Expect(err).NotTo(HaveOccurred())

							// Parse JSON to count endpoints and check Maglev IDs
							var result struct {
								Items []struct {
									Endpoints []struct {
										Addresses []string `json:"addresses"`
										Zone      *string  `json:"zone"`
									} `json:"endpoints"`
								} `json:"items"`
							}
							err = utils.ParseJSON(out, &result)
							g.Expect(err).NotTo(HaveOccurred())
							g.Expect(result.Items).NotTo(BeEmpty(), "no EndpointSlices found for %s", gw.dgName)

							// Count total endpoints across all slices
							totalEndpoints := 0
							for _, slice := range result.Items {
								for _, ep := range slice.Endpoints {
									totalEndpoints++
									// Verify Maglev ID format
									g.Expect(ep.Zone).NotTo(BeNil(), "endpoint missing zone field")
									g.Expect(*ep.Zone).To(MatchRegexp(`^maglev:\d+$`), "invalid Maglev ID format")
								}
							}
							g.Expect(totalEndpoints).To(Equal(suite.targetReplicas),
								"%s: expected %d endpoints, got %d", gw.dgName, suite.targetReplicas, totalEndpoints)
						}).Should(Succeed())
					}
				})

				It(fmt.Sprintf("should have %d target Pods running with sidecar", suite.targetReplicas), func() {
					Eventually(func(g Gomega) {
						cmd := exec.Command("kubectl", "get", "pods", "-n", suite.namespace,
							"-l", fmt.Sprintf("app=%s", suite.targetApp), "--field-selector=status.phase=Running",
							"-o", "jsonpath={range .items[*]}{.metadata.name}{\"\\n\"}{end}")
						out, err := utils.Run(cmd)
						g.Expect(err).NotTo(HaveOccurred())
						g.Expect(len(utils.GetNonEmptyLines(out))).To(Equal(suite.targetReplicas))
					}).Should(Succeed())
				})

				It("should have all ENCs Ready", func() {
					Eventually(func(g Gomega) {
						cmd := exec.Command("kubectl", "get", "enc", "-n", suite.namespace,
							"-o", "jsonpath={range .items[*]}{.status.conditions[?(@.type=='Ready')].status}{\"\\n\"}{end}")
						out, err := utils.Run(cmd)
						g.Expect(err).NotTo(HaveOccurred())
						lines := utils.GetNonEmptyLines(out)
						g.Expect(len(lines)).To(Equal(suite.targetReplicas), "expected %d ENCs", suite.targetReplicas)
						for _, status := range lines {
							g.Expect(status).To(Equal("True"), "all ENCs should be Ready")
						}
					}).Should(Succeed())
				})
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
							Expect(e2eutils.Ping(gw.vip)).To(Succeed())
						})
					}
				})

				Context("TCP load balancing", func() {
					for _, gw := range suite.gateways {
						gw := gw
						It("distributes "+gw.name+" TCP traffic across targets", func() {
							lastingConn, lostConn, err := e2eutils.SendTraffic(gw.vip, 5000, "tcp", 100)
							Expect(err).NotTo(HaveOccurred())
							Expect(lostConn).To(BeZero(), "no connections should be lost")
							Expect(len(lastingConn)).To(Equal(gw.targets),
								"%s: expected %d targets, got: %v", gw.name, gw.targets, lastingConn)
						})
					}
				})

				Context("UDP load balancing", func() {
					for _, gw := range suite.gateways {
						gw := gw
						It("distributes "+gw.name+" UDP traffic across targets", func() {
							lastingConn, lostConn, err := e2eutils.SendTraffic(gw.vip, 5001, "udp", 100)
							Expect(err).NotTo(HaveOccurred())
							Expect(lostConn).To(BeZero(), "no connections should be lost")
							Expect(len(lastingConn)).To(Equal(gw.targets),
								"%s: expected %d targets, got: %v", gw.name, gw.targets, lastingConn)
						})
					}
				})
			})
		})
	}
})
