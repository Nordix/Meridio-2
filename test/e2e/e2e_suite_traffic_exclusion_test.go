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

// Traffic exclusion of NotReady targets.
//
// Verifies that when a single target Pod is flipped to NotReady (its readiness
// probe fails, no rollout), the load balancer stops distributing traffic to
// that Pod while the remaining Ready target(s) keep serving. The check is done
// purely from the traffic side: the NotReady Pod's hostname MUST NOT appear in
// the ctraffic per-connection host stats, while a Ready Pod's hostname MUST.
//
// Mechanism (see also the "controller-distributiongroup" / "controller-loadbalancer"
// skills):
//   - Each target's readiness is file-based: the probe runs `cat /tmp/ready`
//     every 2s (failureThreshold: 1). Removing /tmp/ready flips the Pod to
//     NotReady in place; re-creating it restores Ready. No rollout, same Pod.
//   - The DistributionGroup controller reflects Pod readiness onto each
//     LoadBalancerEndpointSlice endpoint's `Ready` flag (the endpoint and its
//     Maglev ID are preserved, only the flag changes).
//   - The LoadBalancer controller programs only Ready endpoints as NFQLB
//     targets, so a NotReady endpoint receives no hashed traffic.
//
// This is a standalone Ordered Describe that reuses the already-deployed
// ipv4-simple infrastructure (2 IPv4 targets, single LB replica). It runs in the
// same ginkgo invocation as the other specs but keeps its own state lifecycle:
// BeforeAll discovers Pods fresh and DeferCleanup unconditionally restores
// readiness, so it neither depends on nor perturbs the other specs. Serial
// ensures it does not run concurrently with the low-MTU traffic specs.
var _ = Describe("Traffic Exclusion", Label("ipv4"), Serial, Ordered, func() {
	const (
		namespace = "e2e-ipv4-simple"
		targetApp = "target-m"
		dgName    = "dg-m1"
		vip       = "40.0.0.1"
		tcpPort   = 5000
	)

	var (
		targetPods  []string // all running target Pod names (== ctraffic hostnames)
		notReadyPod string   // the Pod we flip to NotReady
		readyPods   []string // the Pods expected to keep serving
	)

	// runningTargetPods returns the names of all Running target Pods.
	runningTargetPods := func() []string {
		out, err := utils.Run(exec.Command("kubectl", "get", "pods", "-n", namespace,
			"-l", "app="+targetApp, "--field-selector=status.phase=Running",
			"-o", "jsonpath={.items[*].metadata.name}"))
		if err != nil {
			return nil
		}
		return strings.Fields(out)
	}

	// podReady returns the container readiness ("true"/"false") of the
	// example-target container in the given Pod.
	podReady := func(pod string) string {
		out, err := utils.Run(exec.Command("kubectl", "get", "pod", pod, "-n", namespace,
			"-o", "jsonpath={.status.containerStatuses[?(@.name=='example-target')].ready}"))
		if err != nil {
			return fmt.Sprintf("ERROR: %v", err)
		}
		return strings.TrimSpace(out)
	}

	// setReady creates (ready=true) or removes (ready=false) /tmp/ready in the
	// example-target container of the given Pod, flipping its readiness probe.
	setReady := func(pod string, ready bool) error {
		var fileCmd []string
		if ready {
			fileCmd = []string{"touch", "/tmp/ready"}
		} else {
			fileCmd = []string{"rm", "-f", "/tmp/ready"}
		}
		args := append([]string{"exec", "-n", namespace, pod, "-c", "example-target", "--"}, fileCmd...)
		_, err := utils.Run(exec.Command("kubectl", args...))
		return err
	}

	BeforeAll(func() {
		By("discovering the running target Pods")
		Eventually(func(g Gomega) {
			targetPods = runningTargetPods()
			g.Expect(len(targetPods)).To(BeNumerically(">=", 2),
				"need at least 2 target Pods to verify selective exclusion")
		}).WithTimeout(60 * time.Second).WithPolling(2 * time.Second).Should(Succeed())

		notReadyPod = targetPods[0]
		readyPods = targetPods[1:]

		By("ensuring all targets start Ready")
		for _, pod := range targetPods {
			Expect(setReady(pod, true)).To(Succeed())
		}
		Eventually(func(g Gomega) {
			for _, pod := range targetPods {
				g.Expect(podReady(pod)).To(Equal("true"), "target %s should be Ready", pod)
			}
		}).WithTimeout(60 * time.Second).WithPolling(2 * time.Second).Should(Succeed())

		By("verifying the VIP is reachable before the test")
		Eventually(func() error { return e2eutils.Ping(vip) }).
			WithTimeout(30 * time.Second).WithPolling(2 * time.Second).Should(Succeed())

		By("waiting for the target Deployment to be Available")
		Eventually(func(g Gomega) {
			out, err := utils.Run(exec.Command("kubectl", "get", "deployment", targetApp, "-n", namespace,
				"-o", "jsonpath={.status.conditions[?(@.type=='Available')].status}"))
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(out).To(Equal("True"), "target Deployment %s should be Available", targetApp)
		}).WithTimeout(120 * time.Second).WithPolling(2 * time.Second).Should(Succeed())

		By("waiting for the DistributionGroup to be Ready")
		Eventually(func(g Gomega) {
			out, err := utils.Run(exec.Command("kubectl", "get", "distg", dgName, "-n", namespace,
				"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}"))
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(out).To(Equal("True"), "%s should be Ready", dgName)
		}).WithTimeout(120 * time.Second).WithPolling(2 * time.Second).Should(Succeed())

		// Always restore full readiness so later specs see a healthy suite,
		// even if an assertion below fails mid-sequence.
		DeferCleanup(func() {
			By("restoring readiness on all target Pods")
			for _, pod := range targetPods {
				_ = setReady(pod, true)
			}
			Eventually(func(g Gomega) {
				for _, pod := range targetPods {
					g.Expect(podReady(pod)).To(Equal("true"))
				}
			}).WithTimeout(60 * time.Second).WithPolling(2 * time.Second).Should(Succeed())
		})
	})

	It("baseline: all targets receive traffic while Ready", func() {
		lastingConn, lostConn, err := e2eutils.SendTraffic(vip, tcpPort, "tcp", 100)
		Expect(err).NotTo(HaveOccurred())
		Expect(lostConn).To(BeZero(), "no connections should be lost")
		Expect(lastingConn).To(HaveKey(notReadyPod),
			"target %s should serve traffic while Ready (got: %v)", notReadyPod, lastingConn)
		for _, pod := range readyPods {
			Expect(lastingConn).To(HaveKey(pod),
				"target %s should serve traffic while Ready (got: %v)", pod, lastingConn)
		}
	})

	It("excludes a NotReady target from traffic while others keep serving", func() {
		By(fmt.Sprintf("flipping target %s to NotReady", notReadyPod))
		Expect(setReady(notReadyPod, false)).To(Succeed())

		By("waiting for the target to report NotReady (others stay Ready)")
		Eventually(func(g Gomega) {
			g.Expect(podReady(notReadyPod)).To(Equal("false"),
				"target %s should be NotReady", notReadyPod)
			for _, pod := range readyPods {
				g.Expect(podReady(pod)).To(Equal("true"),
					"target %s should remain Ready", pod)
			}
		}).WithTimeout(60 * time.Second).WithPolling(2 * time.Second).Should(Succeed())

		By("sending traffic and asserting the NotReady target's hostname is absent")
		// Poll: the LB needs a moment to observe the NotReady endpoint and drop
		// it as an NFQLB target. Once excluded, no connection should ever hash to
		// it, and no connections should be lost (Ready targets absorb them all).
		Eventually(func(g Gomega) {
			lastingConn, lostConn, err := e2eutils.SendTraffic(vip, tcpPort, "tcp", 100)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(lostConn).To(BeZero(), "no connections should be lost")
			g.Expect(lastingConn).NotTo(HaveKey(notReadyPod),
				"NotReady target %s must not receive traffic (got: %v)", notReadyPod, lastingConn)
			// At least one Ready target must still be serving.
			serving := false
			for _, pod := range readyPods {
				if _, ok := lastingConn[pod]; ok {
					serving = true
				}
			}
			g.Expect(serving).To(BeTrue(),
				"at least one Ready target should keep serving (got: %v)", lastingConn)
		}).WithTimeout(60 * time.Second).WithPolling(3 * time.Second).Should(Succeed())
	})

	It("re-includes the target in traffic once it becomes Ready again", func() {
		By(fmt.Sprintf("restoring readiness on target %s", notReadyPod))
		Expect(setReady(notReadyPod, true)).To(Succeed())

		By("waiting for the target to report Ready again")
		Eventually(func(g Gomega) {
			g.Expect(podReady(notReadyPod)).To(Equal("true"),
				"target %s should be Ready again", notReadyPod)
		}).WithTimeout(60 * time.Second).WithPolling(2 * time.Second).Should(Succeed())

		By("sending traffic and asserting the recovered target serves again")
		Eventually(func(g Gomega) {
			lastingConn, lostConn, err := e2eutils.SendTraffic(vip, tcpPort, "tcp", 100)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(lostConn).To(BeZero(), "no connections should be lost")
			g.Expect(lastingConn).To(HaveKey(notReadyPod),
				"recovered target %s should receive traffic again (got: %v)", notReadyPod, lastingConn)
		}).WithTimeout(60 * time.Second).WithPolling(3 * time.Second).Should(Succeed())
	})
})
