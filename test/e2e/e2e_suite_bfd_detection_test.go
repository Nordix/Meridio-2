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
	"strings"
	"time"

	"github.com/nordix/meridio-2/test/utils"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Standalone bfd-detection suite: a single gateway (gw-bfd) on VLAN 1500, whose
// GatewayRouter (gw-bfd-router-v4) starts WITH BFD enabled. The test compares
// how quickly the SLLBR router detects an abrupt, silent peer failure in two
// sequential phases against the same router:
//
//	Phase A (BFD):    minRx * multiplier = 300ms * 3 ≈ 0.9s
//	Phase B (no BFD): only when the BGP hold timer (15s) expires
//
// The failure is injected by bringing the VPN gateway's VLAN subinterface down
// (ip link set vlan15 down) — no BGP NOTIFICATION or TCP teardown reaches the
// peer, so the receiver must rely on its own timers. Between the phases the
// router is patched at runtime to remove the `bfd` block; the original config
// (with BFD) is restored in AfterAll.
const (
	bfdNamespace = "e2e-bfd-detection"
	bfdGateway   = "gw-bfd"
	bfdRouter    = "gw-bfd-router-v4"
	bfdVlanIf    = "vlan15" // VPN gateway VLAN 1500 subinterface

	// BGP hold time configured on the router (routing.yaml holdTime: 15s).
	bfdHoldTime = 15 * time.Second

	// Phase A: BFD detects within ~minRx*multiplier (0.9s) plus poll/scheduling
	// slack.
	bfdMaxDetect = 5 * time.Second

	// Phase B: hold-timer detection is holdTime minus the age of the last
	// received message when the link drops, so it varies (roughly
	// holdTime - one keepalive interval - jitter) and can land a few seconds
	// below holdTime. This floor is well below holdTime to tolerate that
	// spread, while staying far above the BFD path so the two regimes remain
	// clearly distinguishable.
	noBfdMinDetect = 8 * time.Second

	// Upper bound for the no-BFD detection (holdTime + convergence slack).
	noBfdMaxDetect = 25 * time.Second

	// How often we poll birdc while waiting for the session to drop.
	bfdPollInterval = 250 * time.Millisecond

	// JSON patches toggling the BFD block on the GatewayRouter at runtime.
	bfdRemovePatch = `[{"op":"remove","path":"/spec/bgp/bfd"}]`
	bfdAddPatch    = `[{"op":"add","path":"/spec/bgp/bfd",` +
		`"value":{"minTx":"300ms","minRx":"300ms","multiplier":3}}]`
)

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

// lbPodName returns the name of the SLLBR Pod for the bfd gateway.
func lbPodName() (string, error) {
	cmd := exec.Command("kubectl", "get", "pods", "-n", bfdNamespace,
		"-l", fmt.Sprintf("gateway.networking.k8s.io/gateway-name=%s", bfdGateway),
		"-o", "jsonpath={.items[0].metadata.name}")
	return utils.Run(cmd)
}

// bgpEstablished reports whether the NBR-<bfdRouter> BGP session on the given
// SLLBR router Pod is currently Established.
func bgpEstablished(podName string) (bool, error) {
	cmd := exec.Command("kubectl", "exec", "-n", bfdNamespace, podName, "-c", "router", "--",
		"birdc", "-s", "/var/run/bird/bird.ctl", "show", "protocols", fmt.Sprintf(`"NBR-%s"`, bfdRouter))
	out, err := utils.Run(cmd)
	if err != nil {
		return false, err
	}
	return strings.Contains(out, "Established"), nil
}

// birdConf returns the rendered bird.conf from the SLLBR router container.
func birdConf(podName string) (string, error) {
	cmd := exec.Command("kubectl", "exec", "-n", bfdNamespace, podName,
		"-c", "router", "--", "cat", "/etc/bird/bird.conf")
	return utils.Run(cmd)
}

// patchRouterBFD applies a JSON patch to the GatewayRouter (add/remove the bfd
// block).
func patchRouterBFD(patch string) error {
	cmd := exec.Command("kubectl", "patch", "gatewayrouter", bfdRouter,
		"-n", bfdNamespace, "--type=json", "-p", patch)
	_, err := utils.Run(cmd)
	return err
}

// measureSessionDown returns how long after linkDownStart the NBR-<bfdRouter>
// session on podName first leaves the Established state. It polls quickly and
// fails the spec if the session has not dropped within maxWait.
func measureSessionDown(podName string, linkDownStart time.Time, maxWait time.Duration) time.Duration {
	deadline := time.Now().Add(maxWait)
	for time.Now().Before(deadline) {
		established, err := bgpEstablished(podName)
		// Tolerate transient birdc errors while the session flaps.
		if err == nil && !established {
			return time.Since(linkDownStart)
		}
		time.Sleep(bfdPollInterval)
	}
	Fail(fmt.Sprintf("session NBR-%s on %s did not leave Established within %s", bfdRouter, podName, maxWait))
	return 0
}

// injectAndMeasure brings the VPN gateway VLAN subinterface down (capturing the
// start time immediately before the command so shell/loop overhead does not
// inflate the measurement), measures the detection time, then restores the link
// and waits for the session to re-establish.
func injectAndMeasure(podName string) time.Duration {
	linkDownStart := time.Now()
	_, err := utils.Run(vpnGatewayCommand("ip", "link", "set", bfdVlanIf, "down"))
	Expect(err).NotTo(HaveOccurred(), "failed to bring %s down", bfdVlanIf)

	detect := measureSessionDown(podName, linkDownStart, noBfdMaxDetect)

	_, err = utils.Run(vpnGatewayCommand("ip", "link", "set", bfdVlanIf, "up"))
	Expect(err).NotTo(HaveOccurred(), "failed to bring %s back up", bfdVlanIf)

	Eventually(func(g Gomega) {
		established, err := bgpEstablished(podName)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(established).To(BeTrue())
	}).WithTimeout(2 * time.Minute).Should(Succeed())

	return detect
}

var _ = Describe("BFD Detection", Label("ipv4", "bfd-detection"), Serial, Ordered, func() {
	SetDefaultEventuallyTimeout(2 * time.Minute)
	SetDefaultEventuallyPollingInterval(2 * time.Second)

	var (
		pod         string
		bfdDetect   time.Duration
		noBfdDetect time.Duration
	)

	BeforeAll(func() {
		By("resolving the SLLBR Pod for the bfd gateway")
		Eventually(func(g Gomega) {
			p, err := lbPodName()
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(p).NotTo(BeEmpty())
			pod = p
		}).Should(Succeed())
	})

	// AfterAll restores the router to its original state (BFD enabled) so the
	// suite leaves the cluster as it found it: bring the link up, re-add the
	// bfd block, and wait for the session to re-establish.
	AfterAll(func() {
		By("bringing the VPN gateway VLAN subinterface back up")
		_, _ = utils.Run(vpnGatewayCommand("ip", "link", "set", bfdVlanIf, "up"))

		By("restoring BFD on the GatewayRouter")
		// Ignore the error: BFD may already be present if the test failed before
		// removing it. The subsequent bird.conf check is the real assertion.
		_ = patchRouterBFD(bfdAddPatch)

		By("waiting for BFD to be re-enabled and the session Established")
		Eventually(func(g Gomega) {
			conf, err := birdConf(pod)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(extractBGPProtocolSection(conf, bfdRouter)).To(ContainSubstring("bfd {"))
			established, err := bgpEstablished(pod)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(established).To(BeTrue())
		}).Should(Succeed())
	})

	Context("Phase A: BFD enabled", func() {
		It("should have BFD enabled in bird.conf", func() {
			Eventually(func(g Gomega) {
				conf, err := birdConf(pod)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(extractBGPProtocolSection(conf, bfdRouter)).To(ContainSubstring("bfd {"),
					"expected BFD parameters on router %s", bfdRouter)
			}).Should(Succeed())
		})

		It("should have the BGP session Established", func() {
			Eventually(func(g Gomega) {
				established, err := bgpEstablished(pod)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(established).To(BeTrue())
			}).Should(Succeed())
		})

		It("should detect an abrupt link failure quickly via BFD", func() {
			By("abruptly bringing the VPN gateway VLAN subinterface down (silent failure)")
			bfdDetect = injectAndMeasure(pod)
			GinkgoWriter.Printf("BFD detected session down after %s\n", bfdDetect.Round(time.Millisecond))

			Expect(bfdDetect).To(BeNumerically("<=", bfdMaxDetect),
				"BFD should detect the failure within ~minRx*multiplier, got %s", bfdDetect)
		})
	})

	Context("Phase B: BFD removed", func() {
		It("should remove BFD from the router and keep the session Established", func() {
			By("patching the GatewayRouter to remove the bfd block")
			Expect(patchRouterBFD(bfdRemovePatch)).To(Succeed())

			By("waiting for bird.conf to show BFD disabled and the session Established")
			Eventually(func(g Gomega) {
				conf, err := birdConf(pod)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(extractBGPProtocolSection(conf, bfdRouter)).To(ContainSubstring("bfd off;"),
					"expected BFD disabled on router %s", bfdRouter)
				established, err := bgpEstablished(pod)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(established).To(BeTrue())
			}).Should(Succeed())
		})

		It("should detect an abrupt link failure only around the hold time", func() {
			By("abruptly bringing the VPN gateway VLAN subinterface down (silent failure)")
			noBfdDetect = injectAndMeasure(pod)
			GinkgoWriter.Printf("no-BFD detected session down after %s\n", noBfdDetect.Round(time.Millisecond))

			Expect(noBfdDetect).To(BeNumerically(">=", noBfdMinDetect),
				"no-BFD should rely on the BGP hold timer (~%s), got %s", bfdHoldTime, noBfdDetect)
			Expect(noBfdDetect).To(BeNumerically("<=", noBfdMaxDetect),
				"no-BFD should still detect within holdTime + slack, got %s", noBfdDetect)
		})
	})

	Context("Comparison", func() {
		It("should detect much faster with BFD than with the hold timer", func() {
			Expect(bfdDetect).To(BeNumerically(">", 0), "phase A did not run")
			Expect(noBfdDetect).To(BeNumerically(">", 0), "phase B did not run")
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
