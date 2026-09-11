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
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/nordix/meridio-2/test/utils"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// This suite reuses the separate-appnetwork-v4 topology, re-patched by the
// test/e2e/suites/bfd-detection overlay so the two GatewayRouters differ in
// exactly one property: BFD.
//
//	gw-a1-router-v4 : BGP + BFD  (VLAN 100, vpn-gateway subinterface vlan1)
//	gw-a2-router-v4 : BGP only   (VLAN 200, vpn-gateway subinterface vlan2)
//
// Both use a 15s BGP hold time. We inject an abrupt, silent link failure on the
// VPN gateway (ip link set <vlan> down — no BGP NOTIFICATION, no TCP teardown
// reaches the peer) and measure how long each SLLBR router takes to notice the
// session is gone:
//
//   - BFD router:    ~minRx * multiplier = 300ms * 3 ≈ 0.9s
//   - no-BFD router: only when the BGP hold timer expires ≈ 15s
//
// The link is brought down abruptly so detection cannot happen via a graceful
// teardown — the receiver must rely on its own timers, which is the whole point
// of the comparison.
//
// Cleanup restores the former setup: the VLAN links are brought back up and the
// original separate-appnetwork-v4 routing (BFD on both routers, 24s hold time)
// is re-applied, then both sessions are awaited Established again.
const (
	bfdNamespace = "e2e-separate-appnetwork-v4"

	// BGP hold time configured by the bfd-detection overlay (holdTime: 15s).
	bfdHoldTime = 15 * time.Second

	// The BFD router should detect the failure well within this bound
	// (minRx 300ms * multiplier 3 ≈ 0.9s, plus scheduling/poll slack).
	bfdMaxDetect = 5 * time.Second

	// The no-BFD router relies on the BGP hold timer. Actual detection is
	// holdTime minus the age of the last received message when the link drops,
	// so it varies (roughly holdTime - one keepalive interval - jitter) and can
	// land a few seconds below holdTime. This floor is intentionally well below
	// holdTime to tolerate that spread, while staying far above the ~0.9s BFD
	// path so the two regimes remain clearly distinguishable.
	noBfdMinDetect = 8 * time.Second

	// Upper bound for the no-BFD detection (holdTime + convergence slack).
	noBfdMaxDetect = 25 * time.Second

	// How often we poll birdc while waiting for the session to drop.
	bfdPollInterval = 250 * time.Millisecond
)

type bfdRouterCase struct {
	gwName     string // Gateway name (LB Pod selector)
	routerName string // GatewayRouter name -> BIRD protocol "NBR-<routerName>"
	vlanIf     string // VPN gateway VLAN subinterface to toggle
	bfdEnabled bool
}

var bfdRouterCases = []bfdRouterCase{
	{gwName: "gw-a1", routerName: "gw-a1-router-v4", vlanIf: "vlan1", bfdEnabled: true},
	{gwName: "gw-a2", routerName: "gw-a2-router-v4", vlanIf: "vlan2", bfdEnabled: false},
}

// baseRoutingPath returns the absolute path to the original
// separate-appnetwork-v4 routing.yaml, resolved relative to this test file so
// it works regardless of the working directory.
func baseRoutingPath() string {
	_, thisFile, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(thisFile),
		"suites", "separate-appnetwork-v4", "routing.yaml")
}

// vpnGatewayExec returns the argv prefix used to run a command against the VPN
// gateway, honoring E2E_VPN_GATEWAY_EXEC (defaults to "docker exec vpn-gateway",
// matching the Kind-based suites).
func vpnGatewayExec() []string {
	if prefix := os.Getenv("E2E_VPN_GATEWAY_EXEC"); prefix != "" {
		return strings.Fields(prefix)
	}
	return []string{"docker", "exec", "vpn-gateway"}
}

func vpnGatewayCommand(args ...string) *exec.Cmd {
	full := append(vpnGatewayExec(), args...)
	return exec.Command(full[0], full[1:]...) //nolint:gosec // args are test-controlled
}

// lbPodName returns the name of an SLLBR Pod for the given gateway.
func lbPodName(gwName string) (string, error) {
	cmd := exec.Command("kubectl", "get", "pods", "-n", bfdNamespace,
		"-l", fmt.Sprintf("gateway.networking.k8s.io/gateway-name=%s", gwName),
		"-o", "jsonpath={.items[0].metadata.name}")
	return utils.Run(cmd)
}

// bgpEstablished reports whether the NBR-<routerName> BGP session on the given
// SLLBR router Pod is currently Established.
func bgpEstablished(podName, routerName string) (bool, error) {
	cmd := exec.Command("kubectl", "exec", "-n", bfdNamespace, podName, "-c", "router", "--",
		"birdc", "-s", "/var/run/bird/bird.ctl", "show", "protocols", fmt.Sprintf(`"NBR-%s"`, routerName))
	out, err := utils.Run(cmd)
	if err != nil {
		return false, err
	}
	return strings.Contains(out, "Established"), nil
}

// measureSessionDown returns how long after t0 the NBR-<routerName> session on
// podName first leaves the Established state. It polls quickly and fails the
// spec if the session has not dropped within maxWait.
func measureSessionDown(podName, routerName string, t0 time.Time, maxWait time.Duration) time.Duration {
	deadline := time.Now().Add(maxWait)
	for time.Now().Before(deadline) {
		established, err := bgpEstablished(podName, routerName)
		// Tolerate transient birdc errors while the session flaps.
		if err == nil && !established {
			return time.Since(t0)
		}
		time.Sleep(bfdPollInterval)
	}
	Fail(fmt.Sprintf("session NBR-%s on %s did not leave Established within %s", routerName, podName, maxWait))
	return 0
}

var _ = Describe("BFD Detection", Label("ipv4"), Serial, Ordered, func() {
	SetDefaultEventuallyTimeout(2 * time.Minute)
	SetDefaultEventuallyPollingInterval(2 * time.Second)

	// podFor caches the SLLBR Pod name per gateway for the duration of the suite.
	podFor := map[string]string{}

	BeforeAll(func() {
		By("resolving an SLLBR Pod for each gateway")
		for _, rc := range bfdRouterCases {
			Eventually(func(g Gomega) {
				pod, err := lbPodName(rc.gwName)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(pod).NotTo(BeEmpty())
				podFor[rc.gwName] = pod
			}).Should(Succeed())
		}
	})

	// AfterAll restores the former setup regardless of spec outcome:
	//   1. bring the VPN gateway VLAN subinterfaces back up,
	//   2. re-apply the original separate-appnetwork-v4 routing (BFD on both
	//      routers, 24s hold time), reverting the bfd-detection overlay,
	//   3. wait for both BGP sessions to be Established again.
	AfterAll(func() {
		By("bringing the VPN gateway VLAN subinterfaces back up")
		for _, rc := range bfdRouterCases {
			_, _ = utils.Run(vpnGatewayCommand("ip", "link", "set", rc.vlanIf, "up"))
		}

		By("re-applying the original separate-appnetwork-v4 routing")
		cmd := exec.Command("kubectl", "apply", "-n", bfdNamespace, "-f", baseRoutingPath())
		out, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "failed to restore base routing: %s", out)

		By("waiting for both BGP sessions to re-establish")
		for _, rc := range bfdRouterCases {
			Eventually(func(g Gomega) {
				pod, err := lbPodName(rc.gwName)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(pod).NotTo(BeEmpty())
				established, err := bgpEstablished(pod, rc.routerName)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(established).To(BeTrue())
			}).WithTimeout(2 * time.Minute).Should(Succeed())
		}
	})

	Context("router configuration reflects the overlay", func() {
		It("should have BFD enabled only on the gw-a1 router", func() {
			for _, rc := range bfdRouterCases {
				Eventually(func(g Gomega) {
					cmd := exec.Command("kubectl", "exec", "-n", bfdNamespace, podFor[rc.gwName],
						"-c", "router", "--", "cat", "/etc/bird/bird.conf")
					out, err := utils.Run(cmd)
					g.Expect(err).NotTo(HaveOccurred())

					// Locate the BGP protocol section for this router and check its bfd line.
					section := extractBGPProtocolSection(out, rc.routerName)
					g.Expect(section).NotTo(BeEmpty(), "BGP protocol section for NBR-%s not found", rc.routerName)
					if rc.bfdEnabled {
						g.Expect(section).To(ContainSubstring("bfd {"),
							"expected BFD parameters on router %s", rc.routerName)
					} else {
						g.Expect(section).To(ContainSubstring("bfd off;"),
							"expected BFD disabled on router %s", rc.routerName)
					}
				}).Should(Succeed())
			}
		})
	})

	Context("both BGP sessions are established", func() {
		It("should reach Established on every router", func() {
			for _, rc := range bfdRouterCases {
				Eventually(func(g Gomega) {
					established, err := bgpEstablished(podFor[rc.gwName], rc.routerName)
					g.Expect(err).NotTo(HaveOccurred())
					g.Expect(established).To(BeTrue(),
						"BGP session NBR-%s should be Established", rc.routerName)
				}).Should(Succeed())
			}
		})
	})

	Context("abrupt link failure detection timing", func() {
		It("detects failure via BFD much faster than via the BGP hold time", func() {
			By("abruptly bringing both VPN gateway VLAN subinterfaces down (silent failure)")
			// Capture each router's start time immediately before its own
			// link-down command, so the loop iteration and shell round-trip of
			// the other router don't inflate the measured detection time.
			linkDownStart := map[string]time.Time{}
			for _, rc := range bfdRouterCases {
				linkDownStart[rc.routerName] = time.Now()
				_, err := utils.Run(vpnGatewayCommand("ip", "link", "set", rc.vlanIf, "down"))
				Expect(err).NotTo(HaveOccurred(), "failed to bring %s down", rc.vlanIf)
			}

			By("measuring how long each router takes to leave Established")
			detect := map[string]time.Duration{}
			for _, rc := range bfdRouterCases {
				d := measureSessionDown(podFor[rc.gwName], rc.routerName, linkDownStart[rc.routerName], noBfdMaxDetect)
				detect[rc.routerName] = d
				GinkgoWriter.Printf("router %s (bfd=%v) detected session down after %s\n",
					rc.routerName, rc.bfdEnabled, d.Round(time.Millisecond))
			}

			bfdDetect := detect["gw-a1-router-v4"]
			noBfdDetect := detect["gw-a2-router-v4"]

			By("asserting the BFD router detected the failure quickly")
			Expect(bfdDetect).To(BeNumerically("<=", bfdMaxDetect),
				"BFD router should detect the failure within ~minRx*multiplier, got %s", bfdDetect)

			By("asserting the no-BFD router detected the failure only around the hold time")
			Expect(noBfdDetect).To(BeNumerically(">=", noBfdMinDetect),
				"no-BFD router should rely on the BGP hold timer (~%s), got %s", bfdHoldTime, noBfdDetect)
			Expect(noBfdDetect).To(BeNumerically("<=", noBfdMaxDetect),
				"no-BFD router should still detect within holdTime + slack, got %s", noBfdDetect)

			By("asserting BFD is meaningfully faster than the hold-time path")
			Expect(bfdDetect).To(BeNumerically("<", noBfdDetect),
				"BFD detection (%s) should be faster than hold-time detection (%s)", bfdDetect, noBfdDetect)
		})
	})
})

// extractBGPProtocolSection extracts the `protocol bgp 'NBR-<routerName>' { ... }`
// section from a bird.conf so BFD assertions target the right session.
func extractBGPProtocolSection(conf, routerName string) string {
	marker := fmt.Sprintf("protocol bgp 'NBR-%s'", routerName)
	start := strings.Index(conf, marker)
	if start < 0 {
		return ""
	}
	rest := conf[start:]
	end := strings.Index(rest, "\n}")
	if end < 0 {
		return rest
	}
	return rest[:end]
}
