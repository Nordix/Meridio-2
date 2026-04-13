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
}

type suiteTestCase struct {
	name      string
	namespace string
	targetApp string
	gateways  []gwTestCase
}

var testCases = []suiteTestCase{
	{
		name:      "Common App Network",
		namespace: "e2e-common-appnet",
		targetApp: "target-b",
		gateways: []gwTestCase{
			{name: "gw-b1", vip: "30.0.0.1", targets: 2},
			{name: "gw-b2", vip: "30.0.0.2", targets: 2},
		},
	},
	{
		name:      "Separate App Network",
		namespace: "e2e-separate-appnet",
		targetApp: "target-a",
		gateways: []gwTestCase{
			{name: "gw-a1", vip: "20.0.0.1", targets: 2},
			{name: "gw-a2", vip: "20.0.0.2", targets: 2},
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

				It(fmt.Sprintf("should create %d ENCs with %d gateways each", len(suite.gateways), len(suite.gateways)), func() {
					Eventually(func(g Gomega) {
						cmd := exec.Command("kubectl", "get", "enc", "-n", suite.namespace,
							"-o", "jsonpath={range .items[*]}{.metadata.name}{\"\\n\"}{end}")
						out, err := utils.Run(cmd)
						g.Expect(err).NotTo(HaveOccurred())
						g.Expect(len(utils.GetNonEmptyLines(out))).To(Equal(len(suite.gateways)))
					}).Should(Succeed())
				})

				It(fmt.Sprintf("should have %d target Pods running with sidecar", len(suite.gateways)), func() {
					Eventually(func(g Gomega) {
						cmd := exec.Command("kubectl", "get", "pods", "-n", suite.namespace,
							"-l", fmt.Sprintf("app=%s", suite.targetApp), "--field-selector=status.phase=Running",
							"-o", "jsonpath={range .items[*]}{.metadata.name}{\"\\n\"}{end}")
						out, err := utils.Run(cmd)
						g.Expect(err).NotTo(HaveOccurred())
						g.Expect(len(utils.GetNonEmptyLines(out))).To(Equal(len(suite.gateways)))
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
