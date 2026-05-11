//go:build e2e
// +build e2e

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

type sctpGwTestCase struct {
	name   string
	vip    string
	dgName string
}

type sctpSuiteTestCase struct {
	name           string
	namespace      string
	targetApp      string
	targetReplicas int
	gateways       []sctpGwTestCase
}

var sctpTestCases = []sctpSuiteTestCase{
	{
		name:           "SCTP Multihoming",
		namespace:      "e2e-sctp-multihoming",
		targetApp:      "sctp-target",
		targetReplicas: 1,
		gateways: []sctpGwTestCase{
			{name: "sctp-gw1", vip: "50.0.0.1", dgName: "sctp-dg1"},
			{name: "sctp-gw2", vip: "50.0.0.2", dgName: "sctp-dg2"},
		},
	},
}

var _ = Describe("E2E SCTP Multihoming Suites", func() {
	SetDefaultEventuallyTimeout(5 * time.Minute)
	SetDefaultEventuallyPollingInterval(2 * time.Second)

	for _, suite := range sctpTestCases {
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
							cmd := exec.Command("kubectl", "get", "endpointslice", "-n", suite.namespace,
								"-l", fmt.Sprintf("meridio-2.nordix.org/distribution-group=%s", gw.dgName),
								"-o", "json")
							out, err := utils.Run(cmd)
							g.Expect(err).NotTo(HaveOccurred())

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

							totalEndpoints := 0
							for _, slice := range result.Items {
								for _, ep := range slice.Endpoints {
									totalEndpoints++
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

				Context("SCTP multihoming", func() {
					It("maintains zero loss with saturated bidirectional traffic", func() {
						By("running NetPerfMeter SCTP test for 30 seconds")

						// Start NetPerfMeter in background
						done := make(chan *e2eutils.NetPerfMeterResult, 1)
						errCh := make(chan error, 1)

						go func() {
							result, err := e2eutils.RunNetPerfMeterClient(e2eutils.NetPerfMeterConfig{
								Target:     suite.gateways[0].vip + ":9000",
								LocalAddrs: []string{"200.100.0.100", "200.100.1.100"},
								Protocol:   "sctp",
								Duration:   30,
								FrameRate:  "const0",
								FrameSize:  "const1400",
							})
							done <- result
							if err != nil {
								errCh <- err
							}
						}()

						By("waiting for SCTP association to establish")
						time.Sleep(3 * time.Second)

						By("verifying SCTP association exists")
						found, details, err := e2eutils.CheckSCTPAssociationWithVIPs(
							9000,
							[]string{"200.100.0.100", "200.100.1.100"},
							[]string{suite.gateways[0].vip, suite.gateways[1].vip},
						)
						Expect(err).NotTo(HaveOccurred())
						Expect(found).To(BeTrue(), "SCTP association not found with all local addresses and VIPs")
						GinkgoWriter.Printf("SCTP association found:\n%s\n", details)

						By("waiting for NetPerfMeter to complete")
						var result *e2eutils.NetPerfMeterResult
						select {
						case result = <-done:
							GinkgoWriter.Printf("NetPerfMeter completed\n")
						case <-time.After(35 * time.Second):
							Fail("NetPerfMeter timed out")
						}

						By("validating zero packet and frame loss")
						GinkgoWriter.Printf("NetPerfMeter results:\n")
						GinkgoWriter.Printf("  Transmitted: %d bytes\n", result.TransmittedBytes)
						GinkgoWriter.Printf("  Received: %d bytes\n", result.ReceivedBytes)
						GinkgoWriter.Printf("  Packet Loss: %d\n", result.PacketLoss)
						GinkgoWriter.Printf("  Frame Loss: %d\n", result.FrameLoss)

						Expect(result.TransmittedBytes).To(BeNumerically(">", 0))
						Expect(result.ReceivedBytes).To(BeNumerically(">", 0))
						Expect(result.PacketLoss).To(Equal(0))
						Expect(result.FrameLoss).To(Equal(0))
					})

				})
			})
		})
	}
})
