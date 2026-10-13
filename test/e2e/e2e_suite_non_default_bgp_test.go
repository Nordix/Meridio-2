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

	"github.com/nordix/meridio-2/test/utils"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Non-Default BGP Routes suite.
//
// Accepting only default routes (0.0.0.0/0, ::/0) from the external site router
// is considered a security concern; operators want to attract external breakout
// traffic via specific, non-default prefixes. The LB router's BIRD import filter
// (`gateway_routes`, see internal/bird/config.go) already accepts any
// BGP-learned prefix:
//
//	filter gateway_routes {
//	    if ( net ~ [ 0.0.0.0/0 ] ) then accept;
//	    if ( net ~ [ 0::/0 ] ) then accept;
//	    if source = RTS_BGP then accept;   // <- admits non-default prefixes
//	    else reject;
//	}
//
// and the same filter is the kernel export filter, so accepted BGP routes are
// installed into the BIRD-managed kernel table (default 4096).
//
// This suite confirms that end-to-end on a running setup. It reuses the
// ipv4-simple topology (Gateway gw-m1 on VLAN 700, ASN 64518). The simulated
// DCGW (vpn-gateway) is wired to announce a set of specific blackhole prefixes
// from hack/vpn-gateway/bird-filler-net.conf alongside the default route (see
// hack/vpn-gateway/bird-gw.conf). The test asserts, on the LB router container,
// that one representative non-default prefix is:
//
//  1. accepted into BIRD as a BGP-learned route, and
//  2. installed into the kernel breakout table (4096).
//
// The default route remaining present is also checked, documenting that the
// router accepts both default and non-default announcements simultaneously.
const (
	nonDefaultNamespace = "e2e-ipv4-simple"
	nonDefaultGateway   = "gw-m1"
	nonDefaultRouter    = "gw-m1-router-v4"

	// Router container details (see docs/controllers/router.md).
	nonDefaultRouterContainer = "router"
	nonDefaultBirdSocket      = "/var/run/bird/bird.ctl"
	nonDefaultKernelTable     = "4096" // --table-id default

	// Representative non-default prefix announced by the DCGW
	// (FILLER_NET4_100006 in hack/vpn-gateway/bird-filler-net.conf). A /24 is a
	// clear "specific, non-default" route (as opposed to a /32 host route or the
	// 0.0.0.0/0 default).
	nonDefaultPrefix = "128.0.2.0/24"

	// Default route the DCGW also announces (baseline).
	defaultPrefix = "0.0.0.0/0"
)

// nonDefaultLBPod returns the SLLBR Pod name for the gw-m1 gateway.
func nonDefaultLBPod() (string, error) {
	cmd := exec.Command("kubectl", "get", "pods", "-n", nonDefaultNamespace,
		"-l", fmt.Sprintf("gateway.networking.k8s.io/gateway-name=%s", nonDefaultGateway),
		"-o", "jsonpath={.items[0].metadata.name}")
	return utils.Run(cmd)
}

// routerBirdcShowRoute runs `birdc show route for <prefix> all` inside the LB
// router container and returns its output.
func routerBirdcShowRoute(podName, prefix string) (string, error) {
	cmd := exec.Command("kubectl", "exec", "-n", nonDefaultNamespace, podName,
		"-c", nonDefaultRouterContainer, "--",
		"birdc", "-s", nonDefaultBirdSocket, "show", "route", "for", prefix, "all")
	return utils.Run(cmd)
}

// routerKernelTableRoute runs `ip route show table <table> <prefix>` inside the
// LB router container and returns its output.
func routerKernelTableRoute(podName, prefix string) (string, error) {
	cmd := exec.Command("kubectl", "exec", "-n", nonDefaultNamespace, podName,
		"-c", nonDefaultRouterContainer, "--",
		"ip", "route", "show", "table", nonDefaultKernelTable, prefix)
	return utils.Run(cmd)
}

var _ = Describe("Non-Default BGP Routes", Label("ipv4", "non-default-bgp"), Ordered, func() {
	SetDefaultEventuallyTimeout(2 * time.Minute)
	SetDefaultEventuallyPollingInterval(2 * time.Second)

	var pod string

	BeforeAll(func() {
		By("resolving the SLLBR Pod for the gw-m1 gateway")
		Eventually(func(g Gomega) {
			p, err := nonDefaultLBPod()
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(p).NotTo(BeEmpty())
			pod = p
		}).Should(Succeed())
	})

	Context("deployment", func() {
		It("should have the Gateway Programmed", func() {
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "gateway", nonDefaultGateway, "-n", nonDefaultNamespace,
					"-o", "jsonpath={.status.conditions[?(@.type=='Programmed')].status}")
				out, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(out).To(Equal("True"))
			}).Should(Succeed())
		})

		It("should have the BGP session Established on the router", func() {
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "exec", "-n", nonDefaultNamespace, pod,
					"-c", nonDefaultRouterContainer, "--",
					"birdc", "-s", nonDefaultBirdSocket, "show", "protocols",
					fmt.Sprintf(`"NBR-%s"`, nonDefaultRouter))
				out, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(out).To(ContainSubstring("Established"))
			}).Should(Succeed())
		})
	})

	Context("non-default route acceptance", func() {
		It("should accept the non-default prefix into BIRD as a BGP-learned route", func() {
			Eventually(func(g Gomega) {
				out, err := routerBirdcShowRoute(pod, nonDefaultPrefix)
				g.Expect(err).NotTo(HaveOccurred())
				// `show route for` returns the longest match; require the exact
				// prefix to be present (not a fallback to 0.0.0.0/0) AND learned
				// via BGP.
				g.Expect(out).To(ContainSubstring(nonDefaultPrefix),
					"BIRD should hold the exact non-default prefix %s", nonDefaultPrefix)
				g.Expect(out).To(ContainSubstring("source: BGP"),
					"non-default prefix %s should be BGP-learned, not local", nonDefaultPrefix)
			}).Should(Succeed())
		})

		It("should install the non-default prefix into the kernel breakout table", func() {
			Eventually(func(g Gomega) {
				out, err := routerKernelTableRoute(pod, nonDefaultPrefix)
				g.Expect(err).NotTo(HaveOccurred())
				// `ip route show table 4096 128.0.2.0/24` prints the matching
				// route (empty output means it is not installed).
				g.Expect(strings.TrimSpace(out)).NotTo(BeEmpty(),
					"non-default prefix %s should be installed into kernel table %s",
					nonDefaultPrefix, nonDefaultKernelTable)
				g.Expect(out).To(ContainSubstring("128.0.2.0/24"))
			}).Should(Succeed())
		})
	})

	Context("default route coexistence", func() {
		It("should still accept the default route from the same peer", func() {
			Eventually(func(g Gomega) {
				out, err := routerBirdcShowRoute(pod, defaultPrefix)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(out).To(ContainSubstring(defaultPrefix),
					"BIRD should still hold the default route %s", defaultPrefix)
				g.Expect(out).To(ContainSubstring("source: BGP"),
					"default route %s should be BGP-learned", defaultPrefix)
			}).Should(Succeed())
		})

		It("should install the default route into the kernel breakout table", func() {
			Eventually(func(g Gomega) {
				out, err := routerKernelTableRoute(pod, defaultPrefix)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(strings.TrimSpace(out)).NotTo(BeEmpty(),
					"default route %s should be installed into kernel table %s",
					defaultPrefix, nonDefaultKernelTable)
			}).Should(Succeed())
		})
	})
})
