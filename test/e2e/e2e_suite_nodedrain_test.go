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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	e2eutils "github.com/nordix/meridio-2/test/e2e/utils"
	"github.com/nordix/meridio-2/test/utils"
)

// Node-drain test: verify pods reschedule and the service survives a worker
// node drain on the shared-appnetwork-ds dual-stack topology.
//
// Topology (reused from the Endpoint Scaling suite, same package-level
// constants scalingNamespace / scalingTargetApp / scalingGateways):
//   - namespace e2e-shared-appnetwork-ds, dual-stack
//   - two gateways gw-bds1 (VIPs 20.0.0.1, fd00:cafe:2::1) and gw-bds2 (VIPs
//     20.0.0.2, fd00:cafe:2::2), each with 2 LB replicas
//   - one target Deployment (app=target-bds) that backs both DistributionGroups
//
// Scenario (per the node-drain test description):
//  1. Deploy scenario + service (done by the suite deploy target).
//  2. Scale endpoints to 4.
//  3. Establish steady state, verify traffic is stable.
//  4. Cordon then drain a worker node where a gateway LB pod and at least one
//     endpoint are located (see node selection tiers below).
//  5. Verify recovery: rescheduled endpoints reach Ready, traffic restored,
//     no container restarts, and — when the drained node also hosted the
//     controller-manager — the controller-manager recovers to Ready.
//  6. AfterAll: uncordon the node and scale back down to 2 endpoints.
//  7. Re-verify deployment health and traffic.
//
// Disruption model: identical to the Endpoint Scaling suite. The load balancer
// is stateless Maglev with no per-connection flow cache, so evicting and
// rescheduling an endpoint pod (an endpoint-set change) rebuilds the Maglev
// table and remaps a fraction of established flows — established-connection
// disruption during the drain is expected and NOT bounded. The test therefore
// asserts what the stateless design guarantees: traffic keeps flowing during
// the drain, and once recovery settles, new connections have zero loss and are
// load balanced across the full endpoint set, with no target container
// restarts.

const (
	// nodeDrainEndpoints is the endpoint count established before the drain
	// (step 2). Four endpoints across a 4-worker Kind cluster make co-location
	// of a gateway LB pod and a target endpoint on one node highly likely.
	nodeDrainEndpoints = 4

	// controllerLabel / controllerContainer identify the Meridio
	// controller-manager. In the e2e deploy model each suite gets its own
	// controller-manager deployed into the suite namespace (scalingNamespace,
	// e2e-shared-appnetwork-ds) — not a single cluster-wide one — so the
	// namespace used for lookups is scalingNamespace, defined by the shared
	// topology. Used for the Tier-1 node selection and the controller-manager
	// recovery assertion.
	controllerLabel     = "control-plane=controller-manager"
	controllerContainer = "manager"

	// nodeDrainTrafficDuration is how long in-flight traffic runs while the
	// node is drained; it must outlast cordon+drain converging.
	nodeDrainTrafficDuration = 120 * time.Second
	// nodeDrainSettleBeforeDrain lets connections establish before draining.
	nodeDrainSettleBeforeDrain = 5 * time.Second
	// nodeDrainRetries bounds ctraffic reconnect attempts so a drain-induced
	// blip does not abort the run before the final stats are produced.
	nodeDrainRetries = 10
	// nodeDrainConnCount is the number of concurrent connections held open.
	nodeDrainConnCount = 200
)

// nodeDrainHealth returns the ServiceHealth baseline for the shared-appnetwork-ds
// topology with the given expected target endpoint count. Both gateways, their
// 2 LB replicas, both IP families over BGP, and the target ENCs are checked.
// The count is a parameter because it changes across the test (4 during the
// drain, 2 after the AfterAll scale-down).
func nodeDrainHealth(targetCount int) e2eutils.ServiceHealth {
	return e2eutils.ServiceHealth{
		Namespace: scalingNamespace,
		Gateways: []e2eutils.GatewayHealth{
			{
				Name:         "gw-bds1",
				LBReplicas:   2,
				VIPs:         []string{"20.0.0.1", "fd00:cafe:2::1"},
				BGPProtocols: []string{"NBR-gw-bds1-router-v4", "NBR-gw-bds1-router-v6"},
			},
			{
				Name:         "gw-bds2",
				LBReplicas:   2,
				VIPs:         []string{"20.0.0.2", "fd00:cafe:2::2"},
				BGPProtocols: []string{"NBR-gw-bds2-router-v4", "NBR-gw-bds2-router-v6"},
			},
		},
		Targets: []e2eutils.TargetHealth{
			{Label: "app=" + scalingTargetApp, Count: targetCount},
		},
	}
}

// nodeDrainTrafficExpectations returns the steady-state traffic checks (TCP and
// UDP over both IP families for both VIPs) expecting connections to spread
// across targetCount endpoints with zero loss.
func nodeDrainTrafficExpectations(targetCount int) []e2eutils.TrafficExpectation {
	var exps []e2eutils.TrafficExpectation
	for _, gw := range scalingGateways {
		if gw.family == "IPv4" || gw.family == "IPv6" {
			exps = append(exps,
				e2eutils.TrafficExpectation{
					VIP: gw.vip, Protocol: "tcp", Port: 5000,
					Connections: 100, ExpectedTargets: targetCount,
				},
				e2eutils.TrafficExpectation{
					VIP: gw.vip, Protocol: "udp", Port: 5001,
					Connections: 100, ExpectedTargets: targetCount,
				},
			)
		}
	}
	return exps
}

// verifyNodeDrainHealthy asserts full ServiceHealth plus traffic continuity for
// the given endpoint count, matching the "one complete bar" pattern used by the
// robustness suite: Gateway/LB/BGP/ENC state AND actual data-path traffic.
func verifyNodeDrainHealthy(targetCount int, timeout, polling time.Duration) {
	e2eutils.VerifyHealthy(nodeDrainHealth(targetCount), timeout, polling)
	for _, exp := range nodeDrainTrafficExpectations(targetCount) {
		Eventually(func() error { return e2eutils.VerifyTraffic(exp) }).
			WithTimeout(timeout).WithPolling(polling).Should(Succeed())
	}
}

// gatewayLBNodes returns the set of nodes hosting an LB pod of any gw-bds
// gateway.
func gatewayLBNodes() map[string]struct{} {
	nodes := make(map[string]struct{})
	for _, gw := range []string{"gw-bds1", "gw-bds2"} {
		for n := range e2eutils.NodesForPods(scalingNamespace,
			"gateway.networking.k8s.io/gateway-name="+gw) {
			nodes[n] = struct{}{}
		}
	}
	return nodes
}

// drainNodeSelection is the outcome of the 3-tier node selection.
type drainNodeSelection struct {
	node string
	// tier 1: LB + controller-manager + target; 2: LB + target; 3: target only.
	tier int
	// hostsController is true when the selected node also hosts the
	// controller-manager (always true for tier 1; may be true incidentally for
	// lower tiers, in which case its recovery is still asserted).
	hostsController bool
}

// selectDrainNode picks the worker node to drain using a 3-tier preference:
//
//	Tier 1: a node co-hosting a gateway LB pod AND the controller-manager AND a
//	        target endpoint (strongest recovery scenario).
//	Tier 2: a node co-hosting a gateway LB pod AND a target endpoint.
//	Tier 3: any node hosting a target endpoint (last resort).
//
// It returns an error only if no node hosts a target endpoint (Tier 3 empty),
// which means the target Deployment is not scheduled and the test precondition
// cannot be met.
func selectDrainNode() (drainNodeSelection, error) {
	targetNodes := e2eutils.NodesForPods(scalingNamespace, "app="+scalingTargetApp)
	if len(targetNodes) == 0 {
		return drainNodeSelection{}, fmt.Errorf(
			"no node hosts a %s endpoint; cannot select a node to drain", scalingTargetApp)
	}
	lbNodes := gatewayLBNodes()
	controllerNodes := e2eutils.NodesForPods(scalingNamespace, controllerLabel)

	// Tier 1: LB + controller-manager + target.
	for n := range targetNodes {
		_, hasLB := lbNodes[n]
		_, hasController := controllerNodes[n]
		if hasLB && hasController {
			return drainNodeSelection{node: n, tier: 1, hostsController: true}, nil
		}
	}
	// Tier 2: LB + target.
	for n := range targetNodes {
		if _, hasLB := lbNodes[n]; hasLB {
			_, hasController := controllerNodes[n]
			return drainNodeSelection{node: n, tier: 2, hostsController: hasController}, nil
		}
	}
	// Tier 3: any node with a target.
	for n := range targetNodes {
		_, hasController := controllerNodes[n]
		return drainNodeSelection{node: n, tier: 3, hostsController: hasController}, nil
	}
	return drainNodeSelection{}, fmt.Errorf("unreachable: target nodes present but none selected")
}

// logPodPlacement prints the current pod->node placement for the gateways,
// targets, and controller-manager, to make the selected tier explainable in
// test output.
func logPodPlacement() {
	By("current pod->node placement:")
	for _, spec := range []struct{ ns, label, what string }{
		{scalingNamespace, "gateway.networking.k8s.io/gateway-name=gw-bds1", "LB gw-bds1"},
		{scalingNamespace, "gateway.networking.k8s.io/gateway-name=gw-bds2", "LB gw-bds2"},
		{scalingNamespace, "app=" + scalingTargetApp, "target-bds"},
		{scalingNamespace, controllerLabel, "controller-manager"},
	} {
		cmd := exec.Command("kubectl", "get", "pods", "-n", spec.ns, "-l", spec.label,
			"-o", "custom-columns=POD:.metadata.name,NODE:.spec.nodeName", "--no-headers")
		out, _ := utils.Run(cmd)
		GinkgoWriter.Printf("  [%s]\n%s\n", spec.what, out)
	}
}

// cordonNode marks the node unschedulable.
func cordonNode(node string) {
	By(fmt.Sprintf("cordoning node %s", node))
	cmd := exec.Command("kubectl", "cordon", node)
	_, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred(), "cordon node %s", node)
}

// uncordonNode marks the node schedulable again.
func uncordonNode(node string) {
	By(fmt.Sprintf("uncordoning node %s", node))
	cmd := exec.Command("kubectl", "uncordon", node)
	_, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred(), "uncordon node %s", node)
}

// workloadPodsOnNode returns the "namespace/name" of the suite's workload pods
// (LB pods for both gateways + target endpoints + controller-manager) currently
// scheduled on the given node. DaemonSet-managed and static pods are excluded by
// construction (only the suite's own labeled workloads are queried). Used to
// prove the drain actually evicted the pods that lived on the drained node,
// rather than inferring it from downstream health.
func workloadPodsOnNode(node string) []string {
	var pods []string
	for _, spec := range []struct{ ns, label string }{
		{scalingNamespace, "gateway.networking.k8s.io/gateway-name=gw-bds1"},
		{scalingNamespace, "gateway.networking.k8s.io/gateway-name=gw-bds2"},
		{scalingNamespace, "app=" + scalingTargetApp},
		{scalingNamespace, controllerLabel},
	} {
		for _, n := range e2eutils.GetPodNames(spec.ns, spec.label) {
			if e2eutils.GetPodNode(spec.ns, n) == node {
				pods = append(pods, spec.ns+"/"+n)
			}
		}
	}
	return pods
}

// drainNode evicts pods from the node. DaemonSet pods are ignored and local
// emptyDir data is allowed to be deleted (the target/LB pods use emptyDir
// scratch volumes). --force removes any standalone pods; a modest timeout keeps
// a stuck eviction from hanging the suite. The drain output is echoed to the
// run log so it is evident which pods were evicted.
func drainNode(node string) {
	By(fmt.Sprintf("draining node %s", node))
	cmd := exec.Command("kubectl", "drain", node,
		"--ignore-daemonsets", "--delete-emptydir-data", "--force",
		"--timeout=120s")
	out, err := utils.Run(cmd)
	GinkgoWriter.Printf("kubectl drain %s output:\n%s\n", node, out)
	Expect(err).NotTo(HaveOccurred(), "drain node %s", node)
}

var _ = Describe("Node Drain", Label("dual-stack"), Serial, Ordered, func() {
	var (
		selection      drainNodeSelection
		restartsBefore int
		controllerPod  string
	)

	BeforeAll(func() {
		SetDefaultEventuallyTimeout(5 * time.Minute)
		SetDefaultEventuallyPollingInterval(2 * time.Second)

		By("verifying gateways are Programmed")
		for _, gw := range []string{"gw-bds1", "gw-bds2"} {
			gw := gw
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "gateway", gw, "-n", scalingNamespace,
					"-o", "jsonpath={.status.conditions[?(@.type=='Programmed')].status}")
				out, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(out).To(Equal("True"))
			}).Should(Succeed())
		}

		By("verifying DistributionGroups are Ready")
		for _, dg := range []string{"dg-bds1", "dg-bds2"} {
			dg := dg
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "distg", dg, "-n", scalingNamespace,
					"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}")
				out, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(out).To(Equal("True"))
			}).Should(Succeed())
		}

		// Step 2: scale endpoints to 4.
		By(fmt.Sprintf("scaling %s to %d endpoints", scalingTargetApp, nodeDrainEndpoints))
		scaleTargets(nodeDrainEndpoints)

		By("waiting for VIP routes to propagate to the VPN gateway (both families)")
		for _, gw := range scalingGateways {
			gw := gw
			Eventually(func() error { return e2eutils.Ping(gw.vip) }).Should(Succeed())
		}
	})

	// Step 3: establish steady state and verify traffic is stable.
	It("establishes a steady state with 4 endpoints and stable traffic", func() {
		verifyNodeDrainHealthy(nodeDrainEndpoints, 90*time.Second, 2*time.Second)
		restartsBefore = mustMaxRestartCount()
	})

	// Step 4 + 5: cordon then drain a co-located node, verify recovery.
	It("reschedules endpoints and keeps the service alive across a worker node drain", func() {
		By("selecting a node to drain (Tier 1: LB+controller+target, Tier 2: LB+target, Tier 3: target)")
		logPodPlacement()
		var err error
		selection, err = selectDrainNode()
		Expect(err).NotTo(HaveOccurred())
		GinkgoWriter.Printf("selected node %q via Tier %d (hostsController=%v)\n",
			selection.node, selection.tier, selection.hostsController)

		if selection.hostsController {
			controllerPod = e2eutils.GetPodName(scalingNamespace, controllerLabel)
			Expect(controllerPod).NotTo(BeEmpty(),
				"controller-manager pod should exist on/for the selected node")
		}

		By("starting long-lived in-flight traffic for every gateway/family concurrently")
		handles := make([]*e2eutils.TrafficHandle, len(scalingGateways))
		for i, gw := range scalingGateways {
			h, startErr := e2eutils.StartTraffic(gw.vip, gw.tcpPort, "tcp",
				nodeDrainConnCount, nodeDrainTrafficDuration, nodeDrainRetries)
			Expect(startErr).NotTo(HaveOccurred(), "start traffic for %s (%s)", gw.name, gw.vip)
			handles[i] = h
		}

		time.Sleep(nodeDrainSettleBeforeDrain)

		// Record the exact suite workload pods living on the node before draining,
		// so the drain can be proven to have evicted them (not merely inferred
		// from downstream health).
		podsOnNodeBefore := workloadPodsOnNode(selection.node)
		GinkgoWriter.Printf("workload pods on %s before drain: %v\n",
			selection.node, podsOnNodeBefore)
		Expect(podsOnNodeBefore).NotTo(BeEmpty(),
			"selected node %s should host at least one suite workload pod before draining",
			selection.node)

		// Cordon then drain the selected node.
		cordonNode(selection.node)
		drainNode(selection.node)

		// Acceptance (drain effect): the node must be emptied of the suite's
		// workload pods. This proves the drain evicted them rather than the
		// test inferring it from a later healthy state.
		By("verifying the drained node is emptied of suite workload pods")
		Eventually(func(g Gomega) {
			remaining := workloadPodsOnNode(selection.node)
			g.Expect(remaining).To(BeEmpty(),
				"no suite workload pod should remain on drained node %s, still present: %v",
				selection.node, remaining)
		}).WithTimeout(3 * time.Minute).WithPolling(3 * time.Second).Should(Succeed())

		By("waiting for all in-flight traffic runs to finish (traffic kept flowing)")
		for i, gw := range scalingGateways {
			hosts, _, waitErr := handles[i].Wait()
			Expect(waitErr).NotTo(HaveOccurred(), "wait traffic for %s (%s)", gw.name, gw.vip)
			// Stateless Maglev: established-flow disruption during the drain is
			// expected and not bounded. Only require that traffic kept flowing
			// through the transition (connections were served by endpoints).
			Expect(len(hosts)).To(BeNumerically(">", 0),
				"%s on %s: no traffic was served during the drain (hosts: %v)",
				gw.family, gw.name, hosts)
		}

		// Acceptance: on drain, pods reschedule to other nodes and reach Ready.
		By("verifying the endpoint count is restored on the remaining nodes")
		Eventually(func(g Gomega) {
			g.Expect(runningTargetCount(g)).To(Equal(nodeDrainEndpoints))
		}).WithTimeout(3 * time.Minute).Should(Succeed())

		By("verifying no rescheduled endpoint landed back on the drained node")
		Eventually(func(g Gomega) {
			nodes := e2eutils.NodesForPods(scalingNamespace, "app="+scalingTargetApp)
			_, stillThere := nodes[selection.node]
			g.Expect(stillThere).To(BeFalse(),
				"no target endpoint should be scheduled on cordoned node %s", selection.node)
			g.Expect(len(nodes)).To(BeNumerically(">", 0))
		}).WithTimeout(3 * time.Minute).Should(Succeed())

		// Acceptance: data path restored (steady-state health + traffic).
		By("verifying full recovery: health + traffic across all gateways/families")
		verifyNodeDrainHealthy(nodeDrainEndpoints, 3*time.Minute, 3*time.Second)

		// Assert controller-manager recovery when the drained node hosted it.
		if selection.hostsController {
			By("verifying the controller-manager rescheduled and became Ready again")
			var newControllerPod string
			Eventually(func(g Gomega) {
				newControllerPod = e2eutils.GetPodName(scalingNamespace, controllerLabel)
				g.Expect(newControllerPod).NotTo(BeEmpty(),
					"a controller-manager pod should exist after the drain")
				g.Expect(e2eutils.IsPodReady(scalingNamespace, newControllerPod)).To(BeTrue())
				g.Expect(e2eutils.GetPodNode(scalingNamespace, newControllerPod)).
					NotTo(Equal(selection.node),
						"controller-manager should be rescheduled off the drained node")
			}).WithTimeout(3 * time.Minute).WithPolling(3 * time.Second).Should(Succeed())

			By("confirming the rescheduled controller-manager did not crash-loop")
			Expect(e2eutils.GetContainerRestarts(scalingNamespace, newControllerPod, controllerContainer)).
				To(BeNumerically("==", 0),
					"rescheduled controller-manager should not have crashed")
		}
	})

	// Acceptance: no container restarts on the surviving/rescheduled targets.
	It("does not restart any surviving target container during the drain", func() {
		Eventually(func(g Gomega) {
			g.Expect(maxTargetRestartCount(g)).To(Equal(restartsBefore),
				"no target container should restart due to the node drain")
		}).Should(Succeed())
	})

	AfterAll(func() {
		// Step 6: uncordon the node and scale back down to 2 endpoints.
		if selection.node != "" {
			uncordonNode(selection.node)
		}
		By(fmt.Sprintf("scaling %s back down to %d endpoints", scalingTargetApp, scalingMinReplicas))
		scaleTargets(scalingMinReplicas)

		// Step 7: re-verify deployment health and traffic at the baseline count.
		By("verifying baseline health and traffic after uncordon + scale-down")
		verifyNodeDrainHealthy(scalingMinReplicas, 3*time.Minute, 3*time.Second)
	})
})
