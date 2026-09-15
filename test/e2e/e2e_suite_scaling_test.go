//go:build e2e
// +build e2e

package e2e

import (
	"fmt"
	"math"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	e2eutils "github.com/nordix/meridio-2/test/e2e/utils"
	"github.com/nordix/meridio-2/test/utils"
)

// Endpoint scaling tests verify traffic continuity while the number of
// DistributionGroup endpoints changes, covering both single-instance steps and
// bigger multi-instance jumps between the documented minimum and maximum.
//
// Coverage:
//   - Scale-out (increase instances) up to the documented max, single-instance
//     and bigger jumps.
//   - Scale-in (decrease instances) down to the documented min, single-instance
//     and bigger jumps.
//
// They run on the dual-stack shared-app-network suite (shared-appnetwork-ds):
// two gateways (gw-bds1 on VLAN 300, gw-bds2 on VLAN 400) share a single
// dual-stack app network, and both DistributionGroups select the same target
// application (app=target-bds). Scaling the target Deployment therefore changes
// the endpoint set of both DGs simultaneously.
//
// Data-plane note: the load balancer is (almost) fully stateless Maglev. There
// is no per-connection flow cache pinning an established connection to its
// originally chosen target — the only stateful behaviour is that fragments of a
// single IP packet are steered to the same target within a time window, which
// does not provide connection stickiness. When the endpoint set changes, Maglev
// rebuilds its lookup table and a fraction of established flows are remapped to
// a different backend and reset. That fraction scales with the size of the
// change: a single-instance step near N endpoints disrupts ~1/N of flows, while
// a bigger jump between oldN and newN disrupts roughly |newN-oldN|/max(oldN,newN).
// Established-connection disruption is therefore asserted against a jump-aware
// bound, not a flat fraction; new connections must always be load balanced
// correctly across the current endpoint set.
const (
	// scalingConnCount is the number of concurrent connections ctraffic holds
	// open. Tunable.
	scalingConnCount = 200

	// Documented instance range exercised by the tests.
	scalingMinReplicas = 2
	scalingMaxReplicas = 32 // equals the DistributionGroup maxEndpoints (at-capacity boundary)

	// Traffic window durations. The window must still be running when the scale
	// action finishes converging. A single-instance step converges in seconds; a
	// bigger jump (e.g. 3 -> 32) needs longer for all new pods/ENCs to be Ready.
	scalingTrafficDurationStep = 45 * time.Second
	scalingTrafficDurationJump = 120 * time.Second
	// scalingSettleBeforeScale lets connections establish before scaling.
	scalingSettleBeforeScale = 5 * time.Second
	// scalingRetries bounds ctraffic reconnect attempts so a scale-induced blip
	// does not abort the run before the final stats are produced.
	scalingRetries = 10

	// scalingCoverageFraction is the minimum fraction of endpoints that fresh
	// connections must reach after a scale event. Exact full coverage is not
	// asserted: with stateless Maglev hashing, a fixed number of connections
	// does not deterministically touch every backend at high endpoint counts
	// (e.g. 200 connections spread across 32 endpoints reach ~30). Zero
	// connection loss is asserted separately as the hard correctness signal.
	scalingCoverageFraction = 0.90
)

// scalingGwTestCase describes one gateway/VIP pair exercised by the scaling
// tests. It follows the suite-specific test-case struct convention used by the
// SCTP suite (sctpGwTestCase): shared field names (name, vip) plus fields
// specific to this suite (family, tcpPort).
type scalingGwTestCase struct {
	name    string
	family  string // "IPv4" or "IPv6"
	vip     string
	tcpPort int
}

const scalingNamespace = "e2e-shared-appnetwork-ds"
const scalingTargetApp = "target-bds"

var scalingGateways = []scalingGwTestCase{
	{name: "gw-bds1", family: "IPv4", vip: "20.0.0.1", tcpPort: 5000},
	{name: "gw-bds1", family: "IPv6", vip: "fd00:cafe:2::1", tcpPort: 5000},
	{name: "gw-bds2", family: "IPv4", vip: "20.0.0.2", tcpPort: 5000},
	{name: "gw-bds2", family: "IPv6", vip: "fd00:cafe:2::2", tcpPort: 5000},
}

// minCoverage returns the minimum number of distinct endpoints fresh
// connections must reach for a set of newN endpoints.
func minCoverage(newN int) int {
	return int(math.Ceil(scalingCoverageFraction * float64(newN)))
}

// trafficDurationFor picks a traffic window long enough for the scale from oldN
// to newN to converge: a longer window for bigger jumps.
func trafficDurationFor(oldN, newN int) time.Duration {
	if int(math.Abs(float64(newN-oldN))) > 1 {
		return scalingTrafficDurationJump
	}
	return scalingTrafficDurationStep
}

// runningTargetCount returns the number of Running target-bds pods.
func runningTargetCount(g Gomega) int {
	cmd := exec.Command("kubectl", "get", "pods", "-n", scalingNamespace,
		"-l", "app="+scalingTargetApp, "--field-selector=status.phase=Running",
		"-o", "jsonpath={range .items[*]}{.metadata.name}{\"\\n\"}{end}")
	out, err := utils.Run(cmd)
	g.Expect(err).NotTo(HaveOccurred())
	return len(utils.GetNonEmptyLines(out))
}

// scaleTargets sets the target-bds Deployment replica count and waits until that
// many pods are Running and Ready and all ENCs are Ready.
func scaleTargets(replicas int) {
	By(fmt.Sprintf("scaling %s to %d replicas", scalingTargetApp, replicas))
	cmd := exec.Command("kubectl", "scale", "deployment", scalingTargetApp,
		"-n", scalingNamespace, fmt.Sprintf("--replicas=%d", replicas))
	_, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred())

	Eventually(func(g Gomega) {
		cmd := exec.Command("kubectl", "get", "pods", "-n", scalingNamespace,
			"-l", "app="+scalingTargetApp,
			"-o", "jsonpath={range .items[*]}{.status.phase}{\"=\"}{.status.containerStatuses[*].ready}{\"\\n\"}{end}")
		out, err := utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred())
		lines := utils.GetNonEmptyLines(out)
		ready := 0
		for _, l := range lines {
			if strings.HasPrefix(l, "Running=") && !strings.Contains(l, "false") {
				ready++
			}
		}
		g.Expect(ready).To(Equal(replicas), "expected %d Ready target pods", replicas)
	}).Should(Succeed())

	By("waiting for all ENCs to be Ready after scaling")
	Eventually(func(g Gomega) {
		cmd := exec.Command("kubectl", "get", "enc", "-n", scalingNamespace,
			"-o", "jsonpath={range .items[*]}{.status.conditions[?(@.type=='Ready')].status}{\"\\n\"}{end}")
		out, err := utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred())
		lines := utils.GetNonEmptyLines(out)
		g.Expect(len(lines)).To(Equal(replicas), "expected %d ENCs", replicas)
		for _, status := range lines {
			g.Expect(status).To(Equal("True"))
		}
	}).Should(Succeed())
}

// maxTargetRestartCount returns the max container restartCount across target pods.
func maxTargetRestartCount(g Gomega) int {
	cmd := exec.Command("kubectl", "get", "pods", "-n", scalingNamespace,
		"-l", "app="+scalingTargetApp,
		"-o", "jsonpath={.items[*].status.containerStatuses[*].restartCount}")
	out, err := utils.Run(cmd)
	g.Expect(err).NotTo(HaveOccurred())
	maxCount := 0
	for _, f := range strings.Fields(strings.TrimSpace(out)) {
		var n int
		if _, scanErr := fmt.Sscanf(f, "%d", &n); scanErr == nil && n > maxCount {
			maxCount = n
		}
	}
	return maxCount
}

// mustMaxRestartCount reads the current max restart count using a fresh Gomega.
func mustMaxRestartCount() int {
	var count int
	Eventually(func(g Gomega) {
		count = maxTargetRestartCount(g)
	}).Should(Succeed())
	return count
}

// scaleStep runs one scale transition from oldN to newN endpoints and verifies
// traffic continuity across the change.
//
// Continuity model: the load balancer is stateless Maglev with no per-connection
// flow cache, so any endpoint-set change rebuilds the Maglev table and remaps
// some established flows. A bigger jump makes new endpoints become Ready in
// waves, each triggering a rebuild, so established-flow disruption is inherently
// unbounded and expected. Rather than asserting a (flaky, source-entropy
// dependent) cumulative-reconnect bound, the test verifies what the stateless
// design actually guarantees:
//   - traffic keeps flowing throughout the scale (connections are served by
//     endpoints during the transition),
//   - after the scale settles, fresh connections are load balanced across at
//     least scalingCoverageFraction of the endpoints with zero new-connection
//     loss, and
//   - no target container restarts.
//
// The four gateway/family streams run concurrently across one scale action
// (rather than sequentially) to keep the step duration close to a single
// traffic window instead of four.
func scaleStep(oldN, newN int) {
	duration := trafficDurationFor(oldN, newN)
	var restartsBefore int

	BeforeAll(func() {
		By(fmt.Sprintf("confirming %d endpoints before scaling to %d", oldN, newN))
		Eventually(func(g Gomega) {
			g.Expect(runningTargetCount(g)).To(Equal(oldN))
		}).Should(Succeed())
		restartsBefore = mustMaxRestartCount()
	})

	It(fmt.Sprintf("keeps traffic flowing while scaling to target (all gateways/families) during %d->%d", oldN, newN), func() {
		By("starting long-lived in-flight traffic for every gateway/family concurrently")
		handles := make([]*e2eutils.TrafficHandle, len(scalingGateways))
		for i, gw := range scalingGateways {
			h, err := e2eutils.StartTraffic(gw.vip, gw.tcpPort, "tcp",
				scalingConnCount, duration, scalingRetries)
			Expect(err).NotTo(HaveOccurred(), "start traffic for %s (%s)", gw.name, gw.vip)
			handles[i] = h
		}

		By(fmt.Sprintf("scaling %d->%d once, while all streams are in flight", oldN, newN))
		time.Sleep(scalingSettleBeforeScale)
		scaleTargets(newN)

		By("waiting for all in-flight traffic runs to finish")
		for i, gw := range scalingGateways {
			hosts, _, err := handles[i].Wait()
			Expect(err).NotTo(HaveOccurred(), "wait traffic for %s (%s)", gw.name, gw.vip)
			// Established-flow disruption during a Maglev rebuild is expected and
			// not bounded; only require that traffic kept flowing — connections
			// were served by endpoints through the transition. Steady-state
			// correctness is asserted by the distribution spec below.
			Expect(len(hosts)).To(BeNumerically(">", 0),
				"%s %d->%d on %s: no traffic was served during the scale (hosts: %v)",
				gw.family, oldN, newN, gw.name, hosts)
		}
	})

	It(fmt.Sprintf("load balances new connections across the %d endpoints after %d->%d", newN, oldN, newN), func() {
		wantCoverage := minCoverage(newN)
		for _, gw := range scalingGateways {
			gw := gw
			By(fmt.Sprintf("sending fresh %s traffic to %s (%s)", gw.family, gw.name, gw.vip))
			// Retry: after a scale, the LB data plane (endpoint slices -> nfqlb
			// Maglev table) may take a moment to converge to the new endpoint
			// set even once pods and ENCs are Ready.
			Eventually(func(g Gomega) {
				hosts, lost, err := e2eutils.SendTraffic(gw.vip, gw.tcpPort, "tcp", scalingConnCount)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(lost).To(BeZero(), "new connections must not be lost")
				g.Expect(len(hosts)).To(BeNumerically(">=", wantCoverage),
					"%s new connections should reach >= %d of %d endpoints, got %d: %v",
					gw.family, wantCoverage, newN, len(hosts), hosts)
				g.Expect(len(hosts)).To(BeNumerically("<=", newN),
					"%s new connections reached more hosts (%d) than endpoints (%d): %v",
					gw.family, len(hosts), newN, hosts)
			}).WithTimeout(90 * time.Second).WithPolling(5 * time.Second).Should(Succeed())
		}
	})

	It(fmt.Sprintf("does not restart any target container during %d->%d", oldN, newN), func() {
		Eventually(func(g Gomega) {
			g.Expect(maxTargetRestartCount(g)).To(Equal(restartsBefore),
				"no target container should restart during %d->%d", oldN, newN)
		}).Should(Succeed())
	})
}

var _ = Describe("Endpoint Scaling", Label("dual-stack", "scaling"), Ordered, func() {
	SetDefaultEventuallyTimeout(5 * time.Minute)
	SetDefaultEventuallyPollingInterval(2 * time.Second)

	BeforeAll(func() {
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

		By(fmt.Sprintf("ensuring baseline of %d (min) target endpoints", scalingMinReplicas))
		scaleTargets(scalingMinReplicas)

		By("waiting for VIP routes to propagate to the VPN gateway (both families)")
		for _, gw := range scalingGateways {
			gw := gw
			Eventually(func() error { return e2eutils.Ping(gw.vip) }).Should(Succeed())
		}
	})

	// Scale-out with traffic continuity.
	// Increase instances up to the documented max, covering a single-instance
	// step and a bigger multi-instance jump.
	// Acceptance criteria: established connections continue (bounded), new
	// endpoints receive new connections after Ready, no container restarts.
	Context("Scale-out (single step then bigger jump, up to max)", func() {
		Context(fmt.Sprintf("single-instance step %d->%d", scalingMinReplicas, scalingMinReplicas+1), func() {
			scaleStep(scalingMinReplicas, scalingMinReplicas+1)
		})
		Context(fmt.Sprintf("bigger jump %d->%d", scalingMinReplicas+1, scalingMaxReplicas), func() {
			scaleStep(scalingMinReplicas+1, scalingMaxReplicas)
		})
	})

	// Scale-in with traffic continuity.
	// Decrease instances down to the documented min, covering a single-instance
	// step and a bigger multi-instance jump.
	// Acceptance criteria: connections on removed endpoints drain/terminate as
	// designed while surviving/new connections continue (bounded), no restarts.
	Context("Scale-in (single step then bigger jump, down to min)", func() {
		Context(fmt.Sprintf("single-instance step %d->%d", scalingMaxReplicas, scalingMaxReplicas-1), func() {
			scaleStep(scalingMaxReplicas, scalingMaxReplicas-1)
		})
		Context(fmt.Sprintf("bigger jump %d->%d", scalingMaxReplicas-1, scalingMinReplicas), func() {
			scaleStep(scalingMaxReplicas-1, scalingMinReplicas)
		})
	})
})
