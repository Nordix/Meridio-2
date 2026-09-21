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
	"os/exec"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	e2eutils "github.com/nordix/meridio-2/test/e2e/utils"
	"github.com/nordix/meridio-2/test/utils"
)

// Robustness tests reuse the dual-stack suite deployment (namespace
// e2e-dual-stack, gateway gw-ds) as their target topology, matching the
// ip_family: dualstack execution context requested by the ROB Jira tickets.
const (
	robNamespace           = "e2e-dual-stack"
	robGatewayName         = "gw-ds"
	robControllerLabel     = "control-plane=controller-manager"
	robControllerContainer = "manager"
	robTargetLabel         = "app=target-ds"
)

// robServiceHealth is the known-good state for the dual-stack gateway
// backing all robustness tests: 2 LB replicas, both IP families advertised
// over BGP, and 2 target Pods with Ready ENCs.
var robServiceHealth = e2eutils.ServiceHealth{
	Namespace: robNamespace,
	Gateways: []e2eutils.GatewayHealth{
		{
			Name:         robGatewayName,
			LBReplicas:   2,
			VIPs:         []string{"10.0.0.1", "fd00:cafe:1::1"},
			BGPProtocols: []string{"NBR-gw-ds-router-v4", "NBR-gw-ds-router-v6"},
		},
	},
	Targets: []e2eutils.TargetHealth{
		{Label: robTargetLabel, Count: 2},
	},
}

// robTrafficExpectations are the traffic checks that must pass alongside
// robServiceHealth to consider the data path fully intact — TCP and UDP
// over both IP families, matching the checks the Dual Stack suite itself
// exercises in steady state.
var robTrafficExpectations = []e2eutils.TrafficExpectation{
	{VIP: "10.0.0.1", Protocol: "tcp", Port: 5000, Connections: 100, ExpectedTargets: 2},
	{VIP: "10.0.0.1", Protocol: "udp", Port: 5001, Connections: 100, ExpectedTargets: 2},
	{VIP: "fd00:cafe:1::1", Protocol: "tcp", Port: 5000, Connections: 100, ExpectedTargets: 2},
	{VIP: "fd00:cafe:1::1", Protocol: "udp", Port: 5001, Connections: 100, ExpectedTargets: 2},
}

// verifyRobustHealthy asserts robServiceHealth plus full traffic continuity
// (robTrafficExpectations) in one call, so every robustness test judges
// "recovered" against the same, complete bar: Gateway/LB/BGP/ENC state AND
// actual data-path traffic, not just steady-state conditions.
func verifyRobustHealthy(timeout, polling time.Duration) {
	e2eutils.VerifyHealthy(robServiceHealth, timeout, polling)

	for _, exp := range robTrafficExpectations {
		Eventually(func() error { return e2eutils.VerifyTraffic(exp) }).
			WithTimeout(timeout).WithPolling(polling).Should(Succeed())
	}
}

var _ = Describe("Robustness", Label("dual-stack"), Serial, Ordered, func() {
	BeforeEach(func() {
		By("confirming baseline healthy state and traffic continuity before fault injection")
		verifyRobustHealthy(30*time.Second, 2*time.Second)
	})

	// Controller-manager restart recovery.
	// Goal: verify the controller-manager recovers after a restart, returns
	// to Ready, resumes reconciling, and the data path is healthy again
	// afterward. This test does not assert zero-disruption *during* the
	// outage window itself (no traffic is generated concurrently with the
	// restart) — only that nothing is left broken once the new Pod is Ready.
	Context("Controller-manager", func() {
		It("restarts cleanly, resumes reconciling, and the data path is healthy again afterward", func() {
			By("finding the controller-manager Pod")
			controllerPod := e2eutils.GetPodName(robNamespace, robControllerLabel)
			Expect(controllerPod).NotTo(BeEmpty(), "controller-manager Pod should exist")

			By("selecting a target Pod to delete later as a reconciliation probe")
			targetPod := e2eutils.GetPodName(robNamespace, robTargetLabel)
			Expect(targetPod).NotTo(BeEmpty(), "at least one target Pod should exist")

			By("restarting the controller-manager Pod (delete, let the Deployment recreate it)")
			cmd := exec.Command("kubectl", "delete", "pod", "-n", robNamespace, controllerPod)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("verifying a new controller-manager Pod becomes Ready")
			var newPod string
			Eventually(func(g Gomega) {
				newPod = e2eutils.GetPodName(robNamespace, robControllerLabel)
				g.Expect(newPod).NotTo(BeEmpty(), "a controller-manager Pod should exist after restart")
				g.Expect(e2eutils.IsPodReady(robNamespace, newPod)).To(BeTrue())
			}).WithTimeout(90 * time.Second).WithPolling(2 * time.Second).Should(Succeed())

			By("verifying the data path is healthy again after the restart (steady-state and traffic)")
			verifyRobustHealthy(60*time.Second, 2*time.Second)

			By("deleting a target Pod to force a fresh ENC reconciliation")
			cmd = exec.Command("kubectl", "delete", "pod", "-n", robNamespace, targetPod)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("verifying the restarted controller-manager reconciles the replacement target back to full health")
			verifyRobustHealthy(90*time.Second, 2*time.Second)

			By("confirming the new controller-manager Pod did not crash-loop")
			Expect(e2eutils.GetContainerRestarts(robNamespace, newPod, robControllerContainer)).
				To(BeNumerically("==", 0), "freshly restarted controller-manager should not have crashed again")
		})
	})
})
