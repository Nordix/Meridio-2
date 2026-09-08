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
// Verifies that when target Pods are flipped to NotReady (their readiness probe
// fails, no rollout), the load balancer stops distributing traffic to them
// while the remaining Ready targets keep serving. Covers single-target
// exclusion, recovery, and multi-target exclusion (scale to 4, exclude 2). The
// primary check is traffic-side — a NotReady Pod's hostname MUST NOT appear in
// the ctraffic per-connection host stats while a Ready Pod's MUST — backed by a
// control-plane checkpoint on the LoadBalancerEndpointSlice `ready` flag to make
// failures easy to triage (DG controller vs. LB/dataplane).
//
// Mechanism (see also the "controller-distributiongroup" / "controller-loadbalancer"
// skills):
//   - Each target's readiness is file-based: the probe runs `cat /tmp/ready`
//     every 2s (failureThreshold: 3, so ~6s to detect NotReady). Removing
//     /tmp/ready flips the Pod to NotReady in place; re-creating it restores
//     Ready. No rollout, same Pod.
//   - The DistributionGroup controller reflects Pod readiness onto each
//     LoadBalancerEndpointSlice endpoint's `ready` flag (the endpoint and its
//     Maglev ID are preserved, only the flag changes).
//   - The LoadBalancer controller programs only Ready endpoints as NFQLB
//     targets, so a NotReady endpoint receives no hashed traffic.
//
// This is a standalone Ordered Describe that reuses the already-deployed
// ipv4-simple infrastructure (2 IPv4 targets at baseline, single LB replica).
// It runs in the same ginkgo invocation as the other specs but keeps its own
// state lifecycle: BeforeAll discovers Pods fresh and DeferCleanup
// unconditionally restores readiness and the baseline replica count, so it
// neither depends on nor perturbs the other specs. Serial ensures it does not
// run concurrently with the low-MTU traffic specs.
var _ = Describe("Traffic Exclusion", Label("ipv4"), Serial, Ordered, func() {
	const (
		namespace = "e2e-ipv4-simple"
		targetApp = "target-m"
		dgName    = "dg-m1"
		vip       = "40.0.0.1"
		tcpPort   = 5000
		// baselineReplicas is the target Deployment's default replica count
		// (see suites/ipv4-simple/targets.yaml). The multi-exclusion spec scales
		// up and must restore this so it does not perturb sibling specs/suites
		// sharing the ipv4-simple deployment.
		baselineReplicas = 2
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

	// endpointReady returns the LoadBalancerEndpointSlice `ready` flag for the
	// endpoint backing the given Pod in dg-m1's slices, as a control-plane
	// checkpoint that complements the traffic-side assertions. Returns:
	//   "true"/"false" — the endpoint's ready flag
	//   "absent"       — no endpoint for this Pod was found in any slice
	//   "ERROR: ..."   — the lookup itself failed
	endpointReady := func(pod string) string {
		out, err := utils.Run(exec.Command("kubectl", "get", "lbeslice", "-n", namespace,
			"-l", "meridio-2.nordix.org/distribution-group="+dgName, "-o", "json"))
		if err != nil {
			return fmt.Sprintf("ERROR: %v", err)
		}
		var result struct {
			Items []struct {
				Spec struct {
					Endpoints []struct {
						Target struct {
							Name string `json:"name"`
						} `json:"target"`
						Ready bool `json:"ready"`
					} `json:"endpoints"`
				} `json:"spec"`
			} `json:"items"`
		}
		if err := utils.ParseJSON(out, &result); err != nil {
			return fmt.Sprintf("ERROR: %v", err)
		}
		for _, slice := range result.Items {
			for _, ep := range slice.Spec.Endpoints {
				if ep.Target.Name == pod {
					if ep.Ready {
						return "true"
					}
					return "false"
				}
			}
		}
		return "absent"
	}

	// scaleTargets scales the target Deployment to the given replica count and
	// waits for the rollout to settle.
	scaleTargets := func(g Gomega, replicas int) {
		_, err := utils.Run(exec.Command("kubectl", "scale", "deployment", targetApp,
			"-n", namespace, fmt.Sprintf("--replicas=%d", replicas)))
		g.Expect(err).NotTo(HaveOccurred())
		_, err = utils.Run(exec.Command("kubectl", "rollout", "status",
			"deployment", targetApp, "-n", namespace, "--timeout=120s"))
		g.Expect(err).NotTo(HaveOccurred())
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
		// Poll: Ready conditions (Pod/Deployment/DG) do not guarantee the LB has
		// finished programming its NFQLB targets and nftables VIP rules. Retry
		// until real TCP traffic lands with zero loss across all targets, so this
		// spec doubles as the data-path warm-up gate for the specs that follow.
		// Each attempt runs ctraffic for ~10s, so ~6 attempts fit the timeout.
		Eventually(func(g Gomega) {
			lastingConn, lostConn, err := e2eutils.SendTraffic(vip, tcpPort, "tcp", 100)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(lostConn).To(BeZero(), "no connections should be lost")
			g.Expect(lastingConn).To(HaveKey(notReadyPod),
				"target %s should serve traffic while Ready (got: %v)", notReadyPod, lastingConn)
			for _, pod := range readyPods {
				g.Expect(lastingConn).To(HaveKey(pod),
					"target %s should serve traffic while Ready (got: %v)", pod, lastingConn)
			}
		}).WithTimeout(60 * time.Second).WithPolling(3 * time.Second).Should(Succeed())
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

		By("verifying the control plane marks the endpoint NotReady in the LoadBalancerEndpointSlice")
		// Control-plane checkpoint: the DG controller mirrors Pod readiness onto
		// the endpoint's `ready` flag. Asserting it directly makes a failure
		// easy to triage (DG controller vs. LB/nftables) rather than inferring
		// solely from traffic-side absence.
		Eventually(func(g Gomega) {
			g.Expect(endpointReady(notReadyPod)).To(Equal("false"),
				"endpoint for %s should be marked NotReady in the slice", notReadyPod)
			for _, pod := range readyPods {
				g.Expect(endpointReady(pod)).To(Equal("true"),
					"endpoint for %s should stay Ready in the slice", pod)
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

		By("verifying the exclusion is stable (no flap re-including the target)")
		// Guard against a transient endpoint reappearance: the excluded target
		// must stay absent from traffic across repeated samples, not just clear
		// once.
		Consistently(func(g Gomega) {
			lastingConn, lostConn, err := e2eutils.SendTraffic(vip, tcpPort, "tcp", 100)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(lostConn).To(BeZero(), "no connections should be lost")
			g.Expect(lastingConn).NotTo(HaveKey(notReadyPod),
				"NotReady target %s must stay excluded (got: %v)", notReadyPod, lastingConn)
		}).WithTimeout(15 * time.Second).WithPolling(3 * time.Second).Should(Succeed())
	})

	It("re-includes the target in traffic once it becomes Ready again", func() {
		By(fmt.Sprintf("restoring readiness on target %s", notReadyPod))
		Expect(setReady(notReadyPod, true)).To(Succeed())

		By("waiting for the target to report Ready again")
		Eventually(func(g Gomega) {
			g.Expect(podReady(notReadyPod)).To(Equal("true"),
				"target %s should be Ready again", notReadyPod)
		}).WithTimeout(60 * time.Second).WithPolling(2 * time.Second).Should(Succeed())

		By("verifying the control plane marks the endpoint Ready again in the LoadBalancerEndpointSlice")
		Eventually(func(g Gomega) {
			g.Expect(endpointReady(notReadyPod)).To(Equal("true"),
				"endpoint for %s should be marked Ready again in the slice", notReadyPod)
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

	It("excludes multiple NotReady targets while the rest keep serving", func() {
		var (
			scaledPods  []string // all 4 target Pods after scale-up
			downPods    []string // the 2 flipped NotReady
			upPods      []string // the 2 kept Ready
			scaledCount = 4
			downCount   = 2
		)

		// Restore the baseline (replicas=2, all Ready) before leaving so sibling
		// specs and the Low MTU suite sharing these Pods see a clean state, even
		// if an assertion below fails mid-sequence. Registered first so it runs
		// last (LIFO), after any nested cleanup.
		DeferCleanup(func() {
			By("scaling the target Deployment back to baseline and restoring readiness")
			Eventually(func(g Gomega) { scaleTargets(g, baselineReplicas) }).
				WithTimeout(150 * time.Second).WithPolling(5 * time.Second).Should(Succeed())
			// Re-mark every surviving Pod Ready and refresh the outer targetPods
			// slice the BeforeAll DeferCleanup relies on.
			Eventually(func(g Gomega) {
				pods := runningTargetPods()
				g.Expect(len(pods)).To(Equal(baselineReplicas))
				for _, pod := range pods {
					g.Expect(setReady(pod, true)).To(Succeed())
				}
				targetPods = pods
			}).WithTimeout(60 * time.Second).WithPolling(2 * time.Second).Should(Succeed())
			Eventually(func(g Gomega) {
				for _, pod := range runningTargetPods() {
					g.Expect(podReady(pod)).To(Equal("true"))
				}
			}).WithTimeout(60 * time.Second).WithPolling(2 * time.Second).Should(Succeed())
		})

		By(fmt.Sprintf("scaling the target Deployment up to %d replicas", scaledCount))
		Eventually(func(g Gomega) { scaleTargets(g, scaledCount) }).
			WithTimeout(150 * time.Second).WithPolling(5 * time.Second).Should(Succeed())

		By("discovering the scaled-up target Pods and marking them all Ready")
		Eventually(func(g Gomega) {
			scaledPods = runningTargetPods()
			g.Expect(len(scaledPods)).To(Equal(scaledCount),
				"expected %d running target Pods after scale-up", scaledCount)
			for _, pod := range scaledPods {
				g.Expect(setReady(pod, true)).To(Succeed())
			}
		}).WithTimeout(90 * time.Second).WithPolling(2 * time.Second).Should(Succeed())

		By("waiting for all scaled Pods to be Ready in the LoadBalancerEndpointSlice")
		Eventually(func(g Gomega) {
			for _, pod := range scaledPods {
				g.Expect(podReady(pod)).To(Equal("true"), "target %s should be Ready", pod)
				g.Expect(endpointReady(pod)).To(Equal("true"),
					"endpoint for %s should be Ready in the slice", pod)
			}
		}).WithTimeout(90 * time.Second).WithPolling(2 * time.Second).Should(Succeed())

		By("confirming all targets receive traffic before the disruption")
		Eventually(func(g Gomega) {
			lastingConn, lostConn, err := e2eutils.SendTraffic(vip, tcpPort, "tcp", 100)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(lostConn).To(BeZero(), "no connections should be lost")
			for _, pod := range scaledPods {
				g.Expect(lastingConn).To(HaveKey(pod),
					"target %s should serve traffic while Ready (got: %v)", pod, lastingConn)
			}
		}).WithTimeout(90 * time.Second).WithPolling(3 * time.Second).Should(Succeed())

		downPods = scaledPods[:downCount]
		upPods = scaledPods[downCount:]

		By(fmt.Sprintf("flipping %d targets to NotReady: %v", downCount, downPods))
		for _, pod := range downPods {
			Expect(setReady(pod, false)).To(Succeed())
		}

		By("waiting for the down targets to report NotReady in Pod status and the slice")
		Eventually(func(g Gomega) {
			for _, pod := range downPods {
				g.Expect(podReady(pod)).To(Equal("false"), "target %s should be NotReady", pod)
				g.Expect(endpointReady(pod)).To(Equal("false"),
					"endpoint for %s should be marked NotReady in the slice", pod)
			}
			for _, pod := range upPods {
				g.Expect(podReady(pod)).To(Equal("true"), "target %s should remain Ready", pod)
				g.Expect(endpointReady(pod)).To(Equal("true"),
					"endpoint for %s should stay Ready in the slice", pod)
			}
		}).WithTimeout(60 * time.Second).WithPolling(2 * time.Second).Should(Succeed())

		By("asserting both NotReady targets are excluded while the Ready ones absorb all traffic")
		Eventually(func(g Gomega) {
			lastingConn, lostConn, err := e2eutils.SendTraffic(vip, tcpPort, "tcp", 100)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(lostConn).To(BeZero(), "no connections should be lost")
			for _, pod := range downPods {
				g.Expect(lastingConn).NotTo(HaveKey(pod),
					"NotReady target %s must not receive traffic (got: %v)", pod, lastingConn)
			}
			for _, pod := range upPods {
				g.Expect(lastingConn).To(HaveKey(pod),
					"Ready target %s should keep serving (got: %v)", pod, lastingConn)
			}
		}).WithTimeout(60 * time.Second).WithPolling(3 * time.Second).Should(Succeed())

		By("verifying the multi-target exclusion is stable")
		Consistently(func(g Gomega) {
			lastingConn, lostConn, err := e2eutils.SendTraffic(vip, tcpPort, "tcp", 100)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(lostConn).To(BeZero(), "no connections should be lost")
			for _, pod := range downPods {
				g.Expect(lastingConn).NotTo(HaveKey(pod),
					"NotReady target %s must stay excluded (got: %v)", pod, lastingConn)
			}
		}).WithTimeout(15 * time.Second).WithPolling(3 * time.Second).Should(Succeed())
	})
})
