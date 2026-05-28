//go:build e2e
// +build e2e

package e2e

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	e2eutils "github.com/nordix/meridio-2/test/e2e/utils"
	"github.com/nordix/meridio-2/test/utils"
)

const maglevIDFormat = `^maglev:\d+$`

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

var lowMTUTestCase = suiteTestCase{
	name:      "Low MTU",
	namespace: "e2e-low-mtu",
	targetApp: "target-m",
	gateways: []gwTestCase{
		{name: "gw-m1", vip: "40.0.0.1", targets: 2},
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

							// Parse JSON to verify Maglev IDs
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

							// Verify Maglev ID format for all endpoints
							for _, slice := range result.Items {
								for _, ep := range slice.Endpoints {
									g.Expect(ep.Zone).NotTo(BeNil(), "endpoint missing zone field")
									g.Expect(*ep.Zone).To(MatchRegexp(maglevIDFormat), "invalid Maglev ID format")
								}
							}
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
							Eventually(func() error { return e2eutils.Ping(gw.vip) }).
								WithTimeout(30 * time.Second).Should(Succeed())
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

			// Sidecar restart recovery test (for suites with 2+ gateways)
			if len(suite.gateways) >= 2 {
				Context("Sidecar Restart Recovery", func() {
					var (
						targetPod string
						gw1       gwTestCase // First gateway (will be preserved)
						gw2       gwTestCase // Second gateway (will be deleted)
						tableID1  string
						tableID2  string
					)

					BeforeAll(func() {
						gw1 = suite.gateways[0]
						gw2 = suite.gateways[1]

						By("selecting first target pod")
						Eventually(func(g Gomega) {
							cmd := exec.Command("kubectl", "get", "pods", "-n", suite.namespace,
								"-l", "app="+suite.targetApp, "--field-selector=status.phase=Running",
								"-o", "jsonpath={.items[0].metadata.name}")
							out, err := utils.Run(cmd)
							g.Expect(err).NotTo(HaveOccurred())
							targetPod = strings.TrimSpace(out)
							g.Expect(targetPod).NotTo(BeEmpty())
						}).Should(Succeed())

						By("waiting for ENC to be Ready")
						Eventually(func(g Gomega) {
							cmd := exec.Command("kubectl", "get", "enc", targetPod, "-n", suite.namespace,
								"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}")
							out, err := utils.Run(cmd)
							g.Expect(err).NotTo(HaveOccurred())
							g.Expect(out).To(Equal("True"))
						}).Should(Succeed())

						By("capturing initial table IDs")
						cmd := exec.Command("kubectl", "exec", "-n", suite.namespace, targetPod,
							"-c", "network-sidecar", "--", "ip", "rule", "show")
						out, err := utils.Run(cmd)
						Expect(err).NotTo(HaveOccurred())

						for _, line := range strings.Split(out, "\n") {
							if strings.Contains(line, gw1.vip) {
								fields := strings.Fields(line)
								for i, f := range fields {
									if f == "lookup" && i+1 < len(fields) {
										tableID1 = fields[i+1]
										break
									}
								}
							} else if strings.Contains(line, gw2.vip) {
								fields := strings.Fields(line)
								for i, f := range fields {
									if f == "lookup" && i+1 < len(fields) {
										tableID2 = fields[i+1]
										break
									}
								}
							}
						}
						Expect(tableID1).NotTo(BeEmpty(), "should find table ID for %s", gw1.name)
						Expect(tableID2).NotTo(BeEmpty(), "should find table ID for %s", gw2.name)

						By("setting restart gate marker")
						cmd = exec.Command("kubectl", "exec", "-n", suite.namespace, targetPod,
							"-c", "network-sidecar", "--", "sh", "-c",
							"touch /restart-gate/already-started && rm -f /restart-gate/release-restart")
						_, err = utils.Run(cmd)
						Expect(err).NotTo(HaveOccurred())
					})

					AfterAll(func() {
						By(fmt.Sprintf("restoring %s gateway for subsequent tests", gw2.name))
						// Find the gateway.yaml relative to this test file
						_, filename, _, ok := runtime.Caller(0)
						Expect(ok).To(BeTrue(), "failed to get test file path")
						testDir := filepath.Dir(filename)

						// Derive suite directory from suite name
						var suiteDir string
						switch suite.name {
						case "Common App Network":
							suiteDir = "common-appnetwork"
						case "Separate App Network":
							suiteDir = "separate-appnetwork"
						default:
							Skip(fmt.Sprintf("Unknown suite: %s", suite.name))
						}

						gatewayPath := filepath.Join(testDir, "suites", suiteDir, "gateway.yaml")
						cmd := exec.Command("kubectl", "apply", "-f", gatewayPath, "-n", suite.namespace)
						_, err := utils.Run(cmd)
						Expect(err).NotTo(HaveOccurred())

						By(fmt.Sprintf("waiting for %s to be Accepted", gw2.name))
						Eventually(func(g Gomega) {
							cmd := exec.Command("kubectl", "get", "gateway", gw2.name, "-n", suite.namespace,
								"-o", "jsonpath={.status.conditions[?(@.type=='Accepted')].status}")
							out, err := utils.Run(cmd)
							g.Expect(err).NotTo(HaveOccurred())
							g.Expect(out).To(Equal("True"))
						}).Should(Succeed())

						By(fmt.Sprintf("waiting for %s to be Programmed", gw2.name))
						Eventually(func(g Gomega) {
							cmd := exec.Command("kubectl", "get", "gateway", gw2.name, "-n", suite.namespace,
								"-o", "jsonpath={.status.conditions[?(@.type=='Programmed')].status}")
							out, err := utils.Run(cmd)
							g.Expect(err).NotTo(HaveOccurred())
							g.Expect(out).To(Equal("True"))
						}).Should(Succeed())

						By(fmt.Sprintf("waiting for %s ENC to include %s", targetPod, gw2.name))
						Eventually(func(g Gomega) {
							cmd := exec.Command("kubectl", "get", "enc", targetPod, "-n", suite.namespace,
								"-o", "jsonpath={.spec.gateways[*].name}")
							out, err := utils.Run(cmd)
							g.Expect(err).NotTo(HaveOccurred())
							g.Expect(out).To(ContainSubstring(gw2.name))
						}).Should(Succeed())

						By(fmt.Sprintf("waiting for sidecar to configure %s VIP", gw2.name))
						Eventually(func(g Gomega) {
							cmd := exec.Command("kubectl", "exec", "-n", suite.namespace, targetPod,
								"-c", "network-sidecar", "--", "ip", "addr", "show")
							out, err := utils.Run(cmd)
							g.Expect(err).NotTo(HaveOccurred())
							g.Expect(out).To(ContainSubstring(gw2.vip))
						}).Should(Succeed())
					})

					It("should preserve table IDs and clean up deleted gateway state", func() {
						By("killing sidecar container (will pause at restart gate)")
						cmd := exec.Command("kubectl", "exec", "-n", suite.namespace, targetPod,
							"-c", "network-sidecar", "--", "kill", "1")
						utils.Run(cmd) // Ignore error (container dies)

						By("waiting for sidecar to reach restart gate")
						Eventually(func(g Gomega) {
							cmd := exec.Command("kubectl", "logs", "-n", suite.namespace, targetPod,
								"-c", "network-sidecar", "--tail=5")
							out, err := utils.Run(cmd)
							g.Expect(err).NotTo(HaveOccurred())
							g.Expect(out).To(ContainSubstring("Restart detected"))
						}).Should(Succeed())

						By(fmt.Sprintf("deleting %s gateway while sidecar is paused", gw2.name))
						cmd = exec.Command("kubectl", "delete", "gateway", gw2.name, "-n", suite.namespace)
						_, err := utils.Run(cmd)
						Expect(err).NotTo(HaveOccurred())

						By(fmt.Sprintf("waiting for ENC controller to remove %s from ENC", gw2.name))
						Eventually(func(g Gomega) {
							cmd := exec.Command("kubectl", "get", "enc", targetPod, "-n", suite.namespace,
								"-o", "jsonpath={.spec.gateways[*].name}")
							out, err := utils.Run(cmd)
							g.Expect(err).NotTo(HaveOccurred())
							g.Expect(out).NotTo(ContainSubstring(gw2.name))
							g.Expect(out).To(ContainSubstring(gw1.name))
						}).Should(Succeed())

						By("releasing restart gate")
						cmd = exec.Command("kubectl", "exec", "-n", suite.namespace, targetPod,
							"-c", "network-sidecar", "--", "touch", "/restart-gate/release-restart")
						_, err = utils.Run(cmd)
						Expect(err).NotTo(HaveOccurred())

						By("waiting for sidecar to become ready")
						Eventually(func(g Gomega) {
							cmd := exec.Command("kubectl", "get", "pod", targetPod, "-n", suite.namespace,
								"-o", "jsonpath={.status.containerStatuses[?(@.name=='network-sidecar')].ready}")
							out, err := utils.Run(cmd)
							g.Expect(err).NotTo(HaveOccurred())
							g.Expect(out).To(Equal("true"))
						}).Should(Succeed())

						By("waiting for ENC to be Ready after restart")
						Eventually(func(g Gomega) {
							cmd := exec.Command("kubectl", "get", "enc", targetPod, "-n", suite.namespace,
								"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}")
							out, err := utils.Run(cmd)
							g.Expect(err).NotTo(HaveOccurred())
							g.Expect(out).To(Equal("True"))
						}).Should(Succeed())

						By(fmt.Sprintf("waiting for %s VIP to be removed", gw2.name))
						Eventually(func(g Gomega) {
							cmd := exec.Command("kubectl", "exec", "-n", suite.namespace, targetPod,
								"-c", "network-sidecar", "--", "ip", "addr", "show")
							out, err := utils.Run(cmd)
							g.Expect(err).NotTo(HaveOccurred())
							g.Expect(out).NotTo(ContainSubstring(gw2.vip))
						}, 30*time.Second, 1*time.Second).Should(Succeed())

						By(fmt.Sprintf("verifying %s table ID unchanged and no orphaned rules/tables", gw1.name))
						cmd = exec.Command("kubectl", "exec", "-n", suite.namespace, targetPod,
							"-c", "network-sidecar", "--", "ip", "rule", "show")
						out, err := utils.Run(cmd)
						Expect(err).NotTo(HaveOccurred())

						var newTableID1 string
						for _, line := range strings.Split(out, "\n") {
							if strings.Contains(line, gw1.vip) {
								fields := strings.Fields(line)
								for i, f := range fields {
									if f == "lookup" && i+1 < len(fields) {
										newTableID1 = fields[i+1]
										break
									}
								}
							}
						}
						Expect(newTableID1).To(Equal(tableID1), "%s table ID should be preserved", gw1.name)
						Expect(out).To(ContainSubstring(gw1.vip), "%s rule should be present", gw1.name)
						Expect(out).NotTo(ContainSubstring(gw2.vip), "%s rule should be removed (no orphan)", gw2.name)
						hasOrphanedTable := strings.Contains(out, "lookup "+tableID2)
						Expect(hasOrphanedTable).To(BeFalse(), "%s old table %s should be cleaned up (no orphan)", gw2.name, tableID2)

						By(fmt.Sprintf("verifying no VIP leak (%s VIP removed)", gw2.name))
						cmd = exec.Command("kubectl", "exec", "-n", suite.namespace, targetPod,
							"-c", "network-sidecar", "--", "ip", "addr", "show")
						out, err = utils.Run(cmd)
						Expect(err).NotTo(HaveOccurred())
						Expect(out).To(ContainSubstring(gw1.vip), "%s VIP should be present", gw1.name)
						Expect(out).NotTo(ContainSubstring(gw2.vip), "%s VIP should be removed (no leak)", gw2.name)
					})
				})
			}
		})
	}

	// Low MTU suite: tests PMTU discovery with 1200 MTU internal network.
	// A 1400-byte ping (DF set) exceeds the 1200 MTU app network, so the LB
	// must return ICMP Frag Needed with the VIP as source address.
	Describe(lowMTUTestCase.name, Ordered, func() {
		suite := lowMTUTestCase

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
