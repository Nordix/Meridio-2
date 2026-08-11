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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	e2eutils "github.com/nordix/meridio-2/test/e2e/utils"
	"github.com/nordix/meridio-2/test/utils"
)

// openshiftCRCLBReplicas is the expected LB Pod replica count for the
// OpenShift CRC suite, set via GatewayConfiguration.spec.horizontalScaling.replicas
// in suites/openshift-crc/gateway.yaml.
const openshiftCRCLBReplicas = 2

// openshiftCRCTestCase describes the OpenShift CRC dual-stack suite topology.
// Traffic assertions run against the VPN gateway Pod (not a Docker container,
// unlike the Kind-based suites) via E2EVPNGatewayExecEnv, which the
// `openshift-crc`/`test-openshift-crc` Makefile targets set to
// "kubectl exec -n <namespace> vpn-gateway --".
var openshiftCRCTestCase = suiteTestCase{
	name:           "OpenShift CRC",
	namespace:      "meridio-2",
	targetApp:      "target-ocp",
	targetReplicas: 2,
	gateways: []gwTestCase{
		{name: "gw-ocp", vip: "100.0.0.1", targets: 2, dgName: "dg-ocp"},
	},
}

var _ = Describe("OpenShift CRC", Label("openshift-crc"), Ordered, func() {
	SetDefaultEventuallyTimeout(5 * time.Minute)
	SetDefaultEventuallyPollingInterval(2 * time.Second)

	suite := openshiftCRCTestCase

	Context("Deployment", func() {
		It("should have Gateway Accepted", func() {
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "gateway", "gw-ocp", "-n", suite.namespace,
					"-o", "jsonpath={.status.conditions[?(@.type=='Accepted')].status}")
				out, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(out).To(Equal("True"))
			}).Should(Succeed())
		})

		It("should have Gateway Programmed", func() {
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "gateway", "gw-ocp", "-n", suite.namespace,
					"-o", "jsonpath={.status.conditions[?(@.type=='Programmed')].status}")
				out, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(out).To(Equal("True"))
			}).Should(Succeed())
		})

		It("should have Gateway status.addresses with both IPv4 and IPv6 VIPs", func() {
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "gateway", "gw-ocp", "-n", suite.namespace,
					"-o", "jsonpath={.status.addresses[*].value}")
				out, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(out).To(ContainSubstring("100.0.0.1"), "should have IPv4 VIP")
				g.Expect(out).To(ContainSubstring("fd00:cafe:1::1"), "should have IPv6 VIP")
			}).Should(Succeed())
		})

		It("should deploy LB Pods", func() {
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pods", "-n", suite.namespace,
					"-l", "gateway.networking.k8s.io/gateway-name=gw-ocp",
					"-o", "jsonpath={.items[*].status.phase}")
				out, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				phases := strings.Fields(strings.TrimSpace(out))
				g.Expect(phases).To(HaveLen(openshiftCRCLBReplicas))
				for _, phase := range phases {
					g.Expect(phase).To(Equal("Running"))
				}
			}).Should(Succeed())
		})

		It("should have DistributionGroup Ready", func() {
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "distg", "dg-ocp", "-n", suite.namespace,
					"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}")
				out, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(out).To(Equal("True"))
			}).Should(Succeed())
		})

		It("should have target Pods Running", func() {
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pods", "-n", suite.namespace,
					"-l", "app="+suite.targetApp,
					"-o", "jsonpath={.items[*].status.phase}")
				out, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				phases := strings.Fields(strings.TrimSpace(out))
				g.Expect(phases).To(HaveLen(suite.targetReplicas))
				for _, phase := range phases {
					g.Expect(phase).To(Equal("Running"))
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

		It("should have LB pods with connectivity readiness gates", func() {
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pods", "-n", suite.namespace,
					"-l", "gateway.networking.k8s.io/gateway-name=gw-ocp",
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
				g.Expect(result.Items).To(HaveLen(openshiftCRCLBReplicas))

				for _, pod := range result.Items {
					gateTypes := []string{}
					for _, gate := range pod.Spec.ReadinessGates {
						gateTypes = append(gateTypes, gate.ConditionType)
					}
					g.Expect(gateTypes).To(ContainElement("meridio-2.nordix.org/ipv4-connectivity"),
						"should have IPv4 connectivity readiness gate")
					g.Expect(gateTypes).To(ContainElement("meridio-2.nordix.org/ipv6-connectivity"),
						"should have IPv6 connectivity readiness gate")

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
			Eventually(func() error { return e2eutils.Ping("100.0.0.1") }).Should(Succeed())
			Eventually(func() error { return e2eutils.Ping("fd00:cafe:1::1") }).Should(Succeed())

			By("waiting for IPv6 load balancing path to converge")
			Eventually(func() error {
				lasting, lost, err := e2eutils.SendTraffic("fd00:cafe:1::1", 5000, "tcp", 100)
				if err != nil {
					return err
				}
				if lost > 0 {
					return fmt.Errorf("%d connections lost", lost)
				}
				if len(lasting) < 2 {
					return fmt.Errorf("expected 2 targets, got %d", len(lasting))
				}
				return nil
			}).WithTimeout(60 * time.Second).WithPolling(5 * time.Second).Should(Succeed())
		})

		Context("ICMP reachability", func() {
			It("handles IPv4 ping on VIP", func() {
				Eventually(func() error { return e2eutils.Ping("100.0.0.1") }).
					WithTimeout(30 * time.Second).Should(Succeed())
			})

			It("handles IPv6 ping on VIP", func() {
				Eventually(func() error { return e2eutils.Ping("fd00:cafe:1::1") }).
					WithTimeout(30 * time.Second).Should(Succeed())
			})
		})

		Context("IPv4 load balancing", func() {
			It("distributes TCP traffic across targets", func() {
				lastingConn, lostConn, err := e2eutils.SendTraffic("100.0.0.1", 5000, "tcp", 100)
				Expect(err).NotTo(HaveOccurred())
				Expect(lostConn).To(BeZero(), "no connections should be lost")
				Expect(len(lastingConn)).To(Equal(2), "expected 2 targets, got: %v", lastingConn)
			})

			It("distributes UDP traffic across targets", func() {
				lastingConn, lostConn, err := e2eutils.SendTraffic("100.0.0.1", 5001, "udp", 100)
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
