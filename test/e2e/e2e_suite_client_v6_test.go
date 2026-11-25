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
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	e2eutils "github.com/nordix/meridio-2/test/e2e/utils"
)

// The client-v6 suite covers target-initiated (client) traffic over IPv6: each
// application Pod opens connections from the VIP to an external server. Before
// the first packet the Pod has no neighbor entry for its LB next hop, so the
// kernel solicits it with the VIP as source and the LB's Neighbor Advertisement
// is addressed to the VIP. That advertisement must reach the soliciting Pod, not
// be load-balanced by nfqlb to some target.
//
// Nothing may send traffic to the VIP before the client phase (no VIP ping in
// the health check): replying to inbound traffic makes the targets resolve their
// LB next hop early, which would hide the problem.
//
// Client success alone is not a reliable detector. Besides the VIP-sourced NS,
// the Pod's kernel may send a router reachability probe from its link-local
// address, which the LB answers directly. That probe is rate-limited per route
// (router_probe_interval, 60 s by default, and a new route counts as just
// probed), so whether clients pass on an LB without the ICMPv6 type filter
// depends on timing. The LB's IPv6 output-rule counter is the deterministic check.
var _ = Describe("Client IPv6", Label("ipv6", "client-v6"), Ordered, func() {
	const (
		namespace     = "e2e-client-v6"
		gatewayName   = "gw-c6"
		bgpProtocol   = "NBR-gw-c6-router-v6"
		vip           = "fd00:cafe:9::1"
		serverAddress = "[fd00:200:90::100]:5003"
		targetLabel   = "app=target-c6"
		instances     = 3
		nconn         = 2
		// clientDuration is how long each ctraffic client runs; clientExecTimeout
		// caps the whole kubectl exec in case the exec or the client hangs.
		clientDuration    = 20 * time.Second
		clientExecTimeout = clientDuration + 20*time.Second
	)

	var (
		lbPod          string
		serverPID      string
		queuedBaseline uint64
	)

	BeforeAll(func() {
		e2eutils.VerifyHealthy(e2eutils.ServiceHealth{
			Namespace: namespace,
			Gateways: []e2eutils.GatewayHealth{{
				Name:         gatewayName,
				LBReplicas:   1,
				BGPProtocols: []string{bgpProtocol},
				// VIPs deliberately left empty: replying to an inbound ping would
				// make a target resolve its LB next hop before the client phase.
			}},
			Targets: []e2eutils.TargetHealth{{Label: targetLabel, Count: instances}},
		}, 3*time.Minute, 2*time.Second)

		// VIP advertised and installed on the external router, without sending traffic to it.
		Eventually(func() (bool, error) { return e2eutils.VPNGatewayHasRoute(vip + "/128") }).
			WithTimeout(2*time.Minute).WithPolling(2*time.Second).
			Should(BeTrue(), "VPN gateway should learn the VIP route via BGP")

		lbPod = e2eutils.GetPodName(namespace, "gateway.networking.k8s.io/gateway-name="+gatewayName)
		Expect(lbPod).NotTo(BeEmpty())

		// Precondition: no target may already have a neighbor entry for its LB
		// next hop, in any state. Otherwise the client phase would not exercise
		// next-hop resolution from the VIP (for example after rerunning the test
		// without redeploying the suite).
		//
		// If the sidecar ever creates managed neighbor entries for LB next hops,
		// Pods no longer resolve them from the VIP, so both this precondition and
		// the purpose of the client phase need revisiting.
		for i := range instances {
			pod := e2eutils.GetPodName(namespace, fmt.Sprintf("instance=i%d", i))
			Expect(pod).NotTo(BeEmpty(), "no running Pod for instance i%d", i)

			var nextHops []string
			Eventually(func() (err error) {
				nextHops, err = e2eutils.AppliedIPv6NextHops(namespace, pod)
				return err
			}).WithTimeout(2*time.Minute).WithPolling(2*time.Second).Should(Succeed(),
				"ENC of %s should list applied IPv6 next hops", pod)

			for _, hop := range nextHops {
				entry, err := e2eutils.NeighborEntry(namespace, pod, "example-target", hop)
				Expect(err).NotTo(HaveOccurred())
				Expect(entry).To(BeEmpty(),
					"%s already has a neighbor entry for LB next hop %s; redeploy the suite for a clean run", pod, hop)
			}
		}

		var err error
		queuedBaseline, err = e2eutils.LBOutputIPv6QueuedPackets(namespace, lbPod)
		Expect(err).NotTo(HaveOccurred())

		serverPID, err = e2eutils.StartVPNGatewayCtrafficServer(serverAddress)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() {
			Expect(e2eutils.StopVPNGatewayProcess(serverPID)).To(Succeed())
		})
	})

	It("all targets reach the external server from the VIP concurrently, without ICMPv6 queued to nfqlb", func() {
		// All clients start at the same time, as independent application Pods
		// would. Their first packets trigger concurrent VIP-sourced Neighbor
		// Solicitations, and each Pod must still get its own Neighbor
		// Advertisement (possibly after an ND retransmit).
		type result struct {
			hosts map[string]int
			lost  int
			err   error
		}
		pods := make([]string, instances)
		for i := range instances {
			pods[i] = e2eutils.GetPodName(namespace, fmt.Sprintf("instance=i%d", i))
			Expect(pods[i]).NotTo(BeEmpty(), "no running Pod for instance i%d", i)
		}

		results := make([]result, instances)
		var wg sync.WaitGroup
		for i, pod := range pods {
			wg.Add(1)
			go func() {
				defer GinkgoRecover()
				defer wg.Done()
				h, l, err := e2eutils.RunCtrafficClientInPod(namespace, pod, "example-target",
					serverAddress, vip+"/128", nconn, clientDuration, clientExecTimeout)
				results[i] = result{hosts: h, lost: l, err: err}
			}()
		}
		wg.Wait()

		// Report every target's outcome, not just the first failure.
		var failures []string
		for i, r := range results {
			switch {
			case r.err != nil:
				failures = append(failures, fmt.Sprintf("i%d (%s): %v", i, pods[i], r.err))
			case r.lost > 0:
				failures = append(failures, fmt.Sprintf("i%d (%s): %d failed connects", i, pods[i], r.lost))
			case len(r.hosts) == 0:
				failures = append(failures, fmt.Sprintf("i%d (%s): no connections established", i, pods[i]))
			}
		}

		// No ICMPv6 errors or Echo Replies are expected in this suite, so the IPv6
		// queue rule must not have counted anything. Without the ICMPv6 type filter
		// it counts the Neighbor Advertisements and probes the LB sends to the VIP.
		// Checked in the same spec so one run reports both symptoms.
		queued, err := e2eutils.LBOutputIPv6QueuedPackets(namespace, lbPod)
		Expect(err).NotTo(HaveOccurred())
		if queued != queuedBaseline {
			failures = append(failures, fmt.Sprintf(
				"LB queued %d locally generated ICMPv6 packets addressed to the VIP to nfqlb",
				queued-queuedBaseline))
		}

		Expect(failures).To(BeEmpty(), "client IPv6 checks failed")
	})
})
