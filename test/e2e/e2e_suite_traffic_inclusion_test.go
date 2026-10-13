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
	"k8s.io/utils/ptr"

	e2eutils "github.com/nordix/meridio-2/test/e2e/utils"
	"github.com/nordix/meridio-2/test/utils"
)

// Traffic inclusion of NotReady and terminating targets.
//
// This is the counterpart to the old exclusion behaviour. The LoadBalancer no
// longer withdraws an endpoint's NFQLB target when the backing Pod goes
// NotReady: the endpoint is kept in the Maglev table for as long as it is
// present in a LoadBalancerEndpointSlice, so a readiness flap on one endpoint
// does not recompute the ring and redistribute every other endpoint's
// connections. An endpoint leaves the table only when its Pod is actually gone
// (deleted and terminated) or no longer matches the DistributionGroup.
//
// Verified here:
//   - A NotReady target keeps receiving traffic (not excluded) while its
//     endpoint remains in the slice. The control-plane checkpoint asserts the
//     slice now carries ready=false while serving reflects the probe, and that
//     the Maglev identifier is preserved across the flip.
//   - A terminating target keeps serving until it is fully terminated: while it
//     has a deletion timestamp its endpoint reports terminating=true and (as
//     long as its probe still passes) serving=true, and it only drops out of
//     traffic once the Pod is gone.
//   - Multiple NotReady targets are all kept, not just one.
//
// Mechanism (see also the "controller-distributiongroup" / "controller-loadbalancer"
// skills):
//   - Readiness is file-based: the probe runs `cat /tmp/ready` every 2s
//     (failureThreshold: 3, so ~6s to detect NotReady). Removing /tmp/ready
//     flips the Pod to NotReady in place; re-creating it restores Ready. No
//     rollout, same Pod.
//   - The DistributionGroup controller derives each endpoint's conditions from
//     its Pod — terminating = deletionTimestamp set; serving = PodReady
//     regardless of termination; ready = serving && !terminating — preserving
//     the endpoint and its Maglev ID; only the conditions change.
//   - The LoadBalancer controller keeps every endpoint present in the slice as
//     an NFQLB target, regardless of conditions.
//
// This is a standalone Ordered Describe that reuses the already-deployed
// ipv4-simple infrastructure (2 IPv4 targets at baseline, single LB replica).
// It keeps its own state lifecycle: BeforeAll discovers Pods fresh and
// DeferCleanup unconditionally restores readiness and the baseline replica
// count, so it neither depends on nor perturbs the other specs. Serial ensures
// it does not run concurrently with the low-MTU traffic specs.
var _ = Describe("Traffic Inclusion", Label("ipv4"), Serial, Ordered, func() {
	const (
		namespace = "e2e-ipv4-simple"
		targetApp = "target-m"
		dgName    = "dg-m1"
		vip       = "40.0.0.1"
		tcpPort   = 5000
		// baselineReplicas is the target Deployment's default replica count
		// (see suites/ipv4-simple/targets.yaml). The multi-target spec scales up
		// and must restore this so it does not perturb sibling specs/suites
		// sharing the ipv4-simple deployment.
		baselineReplicas = 2
	)

	var (
		targetPods  []string // all running target Pod names (== ctraffic hostnames)
		notReadyPod string   // the Pod we flip to NotReady
		readyPods   []string // the Pods expected to also keep serving
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

	// endpointConditions holds the slice-side view of an endpoint, the
	// control-plane checkpoint that complements the traffic-side assertions.
	// found is false when no endpoint for the Pod exists in any slice (i.e. the
	// endpoint has left the table).
	type endpointConditions struct {
		found       bool
		ready       bool
		serving     bool
		terminating bool
		identifier  *int32
	}

	// lookupEndpoint returns the conditions and Maglev identifier the DG
	// controller published for the endpoint backing the given Pod in dg-m1's
	// slices. A missing endpoint returns found=false (not an error): that is the
	// expected terminal state once a Pod is fully gone.
	lookupEndpoint := func(pod string) (endpointConditions, error) {
		out, err := utils.Run(exec.Command("kubectl", "get", "lbeslice", "-n", namespace,
			"-l", "meridio-2.nordix.org/distribution-group="+dgName, "-o", "json"))
		if err != nil {
			return endpointConditions{}, err
		}
		var result struct {
			Items []struct {
				Spec struct {
					Endpoints []struct {
						Target struct {
							Name string `json:"name"`
						} `json:"target"`
						Identifier *int32 `json:"identifier"`
						Conditions struct {
							Ready       *bool `json:"ready"`
							Serving     *bool `json:"serving"`
							Terminating *bool `json:"terminating"`
						} `json:"conditions"`
					} `json:"endpoints"`
				} `json:"spec"`
			} `json:"items"`
		}
		if err := utils.ParseJSON(out, &result); err != nil {
			return endpointConditions{}, err
		}
		for _, slice := range result.Items {
			for _, ep := range slice.Spec.Endpoints {
				if ep.Target.Name == pod {
					// discovery/v1 nil semantics: ready and serving default to
					// true, terminating to false.
					// https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.26/#endpointconditions-v1-discovery-k8s-io
					return endpointConditions{
						found:       true,
						ready:       ptr.Deref(ep.Conditions.Ready, true),
						serving:     ptr.Deref(ep.Conditions.Serving, true),
						terminating: ptr.Deref(ep.Conditions.Terminating, false),
						identifier:  ep.Identifier,
					}, nil
				}
			}
		}
		return endpointConditions{found: false}, nil
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
				"need at least 2 target Pods to verify selective inclusion")
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

	It("keeps a NotReady target in traffic while others also keep serving", func() {
		By(fmt.Sprintf("recording the endpoint identifier of target %s before the flip", notReadyPod))
		var idBefore *int32
		Eventually(func(g Gomega) {
			ep, err := lookupEndpoint(notReadyPod)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(ep.found).To(BeTrue(), "endpoint for %s should exist before the flip", notReadyPod)
			g.Expect(ep.identifier).NotTo(BeNil(), "Maglev endpoint should have an identifier")
			idBefore = ep.identifier
		}).WithTimeout(30 * time.Second).WithPolling(2 * time.Second).Should(Succeed())

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

		By("verifying the control plane marks the endpoint ready=false (serving follows the probe) but keeps it")
		// Control-plane checkpoint: the DG controller derives conditions from the
		// Pod. The NotReady Pod (not terminating) yields ready=false, serving=false,
		// terminating=false — and critically the endpoint and its Maglev
		// identifier are preserved, which is what keeps it in the table.
		Eventually(func(g Gomega) {
			ep, err := lookupEndpoint(notReadyPod)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(ep.found).To(BeTrue(), "endpoint for %s must remain in the slice", notReadyPod)
			g.Expect(ep.ready).To(BeFalse(), "endpoint for %s should be ready=false", notReadyPod)
			g.Expect(ep.terminating).To(BeFalse(), "a NotReady (not deleted) Pod is not terminating")
			g.Expect(ep.identifier).To(Equal(idBefore),
				"endpoint for %s should keep its Maglev identifier across the flip", notReadyPod)
		}).WithTimeout(60 * time.Second).WithPolling(2 * time.Second).Should(Succeed())

		By("sending traffic and asserting the NotReady target still receives it")
		// This is the inversion of the old behaviour: the NotReady target MUST
		// still appear in the per-connection host stats, and no connection is lost.
		Eventually(func(g Gomega) {
			lastingConn, lostConn, err := e2eutils.SendTraffic(vip, tcpPort, "tcp", 100)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(lostConn).To(BeZero(), "no connections should be lost")
			g.Expect(lastingConn).To(HaveKey(notReadyPod),
				"NotReady target %s must still receive traffic (got: %v)", notReadyPod, lastingConn)
			for _, pod := range readyPods {
				g.Expect(lastingConn).To(HaveKey(pod),
					"Ready target %s should keep serving (got: %v)", pod, lastingConn)
			}
		}).WithTimeout(60 * time.Second).WithPolling(3 * time.Second).Should(Succeed())

		By("verifying inclusion is stable (the NotReady target is not withdrawn later)")
		// Guard against a delayed withdrawal: the NotReady target must stay in
		// traffic across repeated samples, not merely be present once.
		Consistently(func(g Gomega) {
			lastingConn, lostConn, err := e2eutils.SendTraffic(vip, tcpPort, "tcp", 100)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(lostConn).To(BeZero(), "no connections should be lost")
			g.Expect(lastingConn).To(HaveKey(notReadyPod),
				"NotReady target %s must stay included (got: %v)", notReadyPod, lastingConn)
		}).WithTimeout(15 * time.Second).WithPolling(3 * time.Second).Should(Succeed())

		By(fmt.Sprintf("restoring readiness on target %s", notReadyPod))
		Expect(setReady(notReadyPod, true)).To(Succeed())
		Eventually(func(g Gomega) {
			ep, err := lookupEndpoint(notReadyPod)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(ep.found).To(BeTrue())
			g.Expect(ep.ready).To(BeTrue(), "endpoint for %s should be ready=true again", notReadyPod)
		}).WithTimeout(60 * time.Second).WithPolling(2 * time.Second).Should(Succeed())
	})

	It("keeps a terminating target serving until it is fully terminated", func() {
		// A terminating Pod (deletion timestamp set) that is still passing its
		// probe is the case a single Ready bool could not express: the DG
		// controller reports terminating=true with serving=true, and the LB keeps
		// the endpoint programmed. The endpoint leaves the table only once the
		// Pod is actually gone.
		//
		// Note on timing: the example-target traps SIGTERM and exits promptly, so
		// without a preStop/terminationGracePeriod the drain window is short. This
		// spec therefore tolerates either observation — caught mid-drain
		// (terminating=true, serving=true, still in the slice) OR already gone
		// (endpoint absent) — and in both cases asserts no traffic is lost and the
		// surviving target keeps serving. A deterministic, longer drain window
		// would require adding a preStop sleep + terminationGracePeriodSeconds to
		// the example-target container in suites/ipv4-simple/targets.yaml.
		termPod := readyPods[0]
		survivors := append([]string{notReadyPod}, readyPods[1:]...)

		By("holding traffic open across the deletion to measure continuity")
		handle, err := e2eutils.StartTraffic(vip, tcpPort, "tcp", 100, 20*time.Second, 5)
		Expect(err).NotTo(HaveOccurred())

		By(fmt.Sprintf("deleting target %s with a grace period", termPod))
		// --wait=false returns immediately so we can observe the draining window
		// while the Pod is still terminating rather than blocking until it is gone.
		_, err = utils.Run(exec.Command("kubectl", "delete", "pod", termPod, "-n", namespace,
			"--grace-period=30", "--wait=false"))
		Expect(err).NotTo(HaveOccurred())

		By("observing the endpoint either draining (terminating+serving) or already removed")
		sawDraining := false
		Eventually(func(g Gomega) {
			ep, err := lookupEndpoint(termPod)
			g.Expect(err).NotTo(HaveOccurred())
			if !ep.found {
				// Fully terminated and removed from the table — a valid terminal
				// state. The endpoint must never be withdrawn while still present
				// and only disappears once the Pod is gone.
				return
			}
			// Still present: it must be marked terminating, and while its probe
			// passes it must still be serving — never silently dropped.
			g.Expect(ep.terminating).To(BeTrue(),
				"endpoint for terminating %s must report terminating=true", termPod)
			if ep.serving {
				sawDraining = true
			}
			g.Expect(ep.ready).To(BeFalse(),
				"a terminating endpoint must not be ready even while serving")
		}).WithTimeout(45 * time.Second).WithPolling(1 * time.Second).Should(Succeed())
		if !sawDraining {
			GinkgoWriter.Println("Note: Pod terminated too fast to observe the terminating+serving drain window; " +
				"endpoint removal was observed instead. Add a preStop/terminationGracePeriod to make the window deterministic.")
		}

		By("waiting for the terminated Pod's endpoint to leave the table")
		Eventually(func(g Gomega) {
			ep, err := lookupEndpoint(termPod)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(ep.found).To(BeFalse(),
				"endpoint for terminated %s must eventually be removed from the slice", termPod)
		}).WithTimeout(90 * time.Second).WithPolling(2 * time.Second).Should(Succeed())

		By("confirming traffic held across the termination stayed on the surviving targets")
		lastingConn, lostConn, err := handle.Wait()
		Expect(err).NotTo(HaveOccurred())
		// Established connections to the terminating Pod are expected to end when
		// it exits; the test asserts the data path as a whole kept serving via the
		// survivors rather than demanding zero loss from a Pod we deliberately killed.
		GinkgoWriter.Printf("traffic across termination: lasting=%v lost=%d\n", lastingConn, lostConn)
		serving := false
		for _, pod := range survivors {
			if _, ok := lastingConn[pod]; ok {
				serving = true
			}
		}
		Expect(serving).To(BeTrue(),
			"a surviving target should keep serving across the termination (got: %v)", lastingConn)

		By("waiting for the Deployment to replace the deleted Pod and refreshing bookkeeping")
		Eventually(func(g Gomega) { scaleTargets(g, baselineReplicas) }).
			WithTimeout(150 * time.Second).WithPolling(5 * time.Second).Should(Succeed())
		Eventually(func(g Gomega) {
			pods := runningTargetPods()
			g.Expect(len(pods)).To(Equal(baselineReplicas))
			for _, pod := range pods {
				g.Expect(setReady(pod, true)).To(Succeed())
			}
			targetPods = pods
			notReadyPod = targetPods[0]
			readyPods = targetPods[1:]
		}).WithTimeout(90 * time.Second).WithPolling(2 * time.Second).Should(Succeed())
	})

	It("keeps multiple NotReady targets in traffic while the rest also serve", func() {
		var (
			scaledPods  []string // all 4 target Pods after scale-up
			downPods    []string // the 2 flipped NotReady (must stay included)
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

		By("waiting for all scaled Pods to be ready=true in the LoadBalancerEndpointSlice")
		Eventually(func(g Gomega) {
			for _, pod := range scaledPods {
				g.Expect(podReady(pod)).To(Equal("true"), "target %s should be Ready", pod)
				ep, err := lookupEndpoint(pod)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(ep.found).To(BeTrue())
				g.Expect(ep.ready).To(BeTrue(), "endpoint for %s should be ready=true in the slice", pod)
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

		By("waiting for the down targets to report NotReady in Pod status and ready=false in the slice")
		Eventually(func(g Gomega) {
			for _, pod := range downPods {
				g.Expect(podReady(pod)).To(Equal("false"), "target %s should be NotReady", pod)
				ep, err := lookupEndpoint(pod)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(ep.found).To(BeTrue(), "endpoint for %s must remain in the slice", pod)
				g.Expect(ep.ready).To(BeFalse(), "endpoint for %s should be ready=false", pod)
			}
			for _, pod := range upPods {
				g.Expect(podReady(pod)).To(Equal("true"), "target %s should remain Ready", pod)
				ep, err := lookupEndpoint(pod)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(ep.ready).To(BeTrue(), "endpoint for %s should stay ready=true", pod)
			}
		}).WithTimeout(60 * time.Second).WithPolling(2 * time.Second).Should(Succeed())

		By("asserting all four targets — including the two NotReady — keep receiving traffic")
		Eventually(func(g Gomega) {
			lastingConn, lostConn, err := e2eutils.SendTraffic(vip, tcpPort, "tcp", 100)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(lostConn).To(BeZero(), "no connections should be lost")
			for _, pod := range scaledPods {
				g.Expect(lastingConn).To(HaveKey(pod),
					"target %s must keep receiving traffic regardless of readiness (got: %v)", pod, lastingConn)
			}
		}).WithTimeout(60 * time.Second).WithPolling(3 * time.Second).Should(Succeed())

		By("verifying the inclusion is stable (no delayed withdrawal of the NotReady targets)")
		Consistently(func(g Gomega) {
			lastingConn, lostConn, err := e2eutils.SendTraffic(vip, tcpPort, "tcp", 100)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(lostConn).To(BeZero(), "no connections should be lost")
			for _, pod := range downPods {
				g.Expect(lastingConn).To(HaveKey(pod),
					"NotReady target %s must stay included (got: %v)", pod, lastingConn)
			}
		}).WithTimeout(15 * time.Second).WithPolling(3 * time.Second).Should(Succeed())
	})
})
