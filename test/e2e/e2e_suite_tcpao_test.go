//go:build e2e
// +build e2e

package e2e

import (
	"fmt"
	"os/exec"
	"time"

	"github.com/nordix/meridio-2/test/utils"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var tcpaotestCases = []suiteTestCase{
	{
		name:           "TCP AO",
		namespace:      "e2e-tcp-ao",
		targetApp:      "target-a",
		targetReplicas: 2,
		gateways: []gwTestCase{
			{name: "gw-b1", vip: "30.0.0.1", targets: 2, dgName: "dg-b1"},
			{name: "gw-b2", vip: "30.0.0.2", targets: 2, dgName: "dg-b2"},
		},
	},
}

var _ = Describe("E2E TCP-AO Test Suite", func() {
	SetDefaultEventuallyTimeout(4 * time.Minute)
	SetDefaultEventuallyPollingInterval(2 * time.Second)

	for _, suite := range tcpaotestCases {
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
		})
	}
})
