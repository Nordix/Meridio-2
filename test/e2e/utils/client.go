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

package utils

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	meridio2v1alpha1 "github.com/nordix/meridio-2/api/v1alpha1"
	"github.com/nordix/meridio-2/test/utils"
)

// StartVPNGatewayCtrafficServer starts a TCP ctraffic echo server in the
// background on the VPN gateway, listening on address (e.g. "[fd00::1]:5003"),
// and returns its PID for StopVPNGatewayProcess. It simulates an external
// server that application Pods connect to (client use case).
func StartVPNGatewayCtrafficServer(address string) (string, error) {
	inner := fmt.Sprintf("ctraffic -server -address %s >/dev/null 2>&1 & echo $!", address)
	cmdStr := fmt.Sprintf("%s sh -c '%s'", vpnGatewayExecPrefix(), inner)
	out, err := utils.Run(exec.Command("/bin/sh", "-c", cmdStr))
	if err != nil {
		return "", fmt.Errorf("failed to start ctraffic server on VPN gateway: %w\noutput: %s", err, out)
	}
	pid := strings.TrimSpace(out)
	if _, err := strconv.Atoi(pid); err != nil {
		return "", fmt.Errorf("unexpected PID %q from VPN gateway", pid)
	}
	return pid, nil
}

// StopVPNGatewayProcess kills the process with the given PID on the VPN gateway.
func StopVPNGatewayProcess(pid string) error {
	cmdStr := fmt.Sprintf("%s kill %s", vpnGatewayExecPrefix(), pid)
	if out, err := utils.Run(exec.Command("/bin/sh", "-c", cmdStr)); err != nil {
		return fmt.Errorf("failed to kill PID %s on VPN gateway: %w\noutput: %s", pid, err, out)
	}
	return nil
}

// RunCtrafficClientInPod runs a TCP ctraffic client inside an application Pod
// (container must ship ./ctraffic, like the example-target image), connecting to
// address with nconn connections sourced from srcCIDR (typically the VIP) for
// duration. Returns a map of server hostname → connection count and the number
// of failed connects, like SendTraffic.
//
// execTimeout caps the whole kubectl exec, so a hung exec or client cannot block
// the suite. It must exceed duration to leave room for exec startup and
// ctraffic's final stats.
func RunCtrafficClientInPod(namespace, pod, container, address, srcCIDR string, nconn int,
	duration, execTimeout time.Duration) (map[string]int, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), execTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "kubectl", "exec", "-n", namespace, pod, "-c", container, "--",
		"./ctraffic", "-address", address, "-srccidr", srcCIDR,
		"-nconn", strconv.Itoa(nconn), "-timeout", duration.String(), "-stats", "all")
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return nil, 0, fmt.Errorf("ctraffic client in %s/%s did not finish within %s: %w\noutput: %s",
			namespace, pod, execTimeout, ctx.Err(), out)
	}
	// ctraffic may exit non-zero after failed connects but still print its JSON
	// stats; prefer the parsed result and surface the exec error only if the
	// output cannot be parsed.
	hosts, lost, parseErr := parseCtrafficOutput(extractJSONObject(out))
	if parseErr != nil {
		if err != nil {
			return nil, 0, fmt.Errorf("ctraffic client in %s/%s failed: %w\noutput: %s", namespace, pod, err, out)
		}
		return nil, 0, parseErr
	}
	return hosts, lost, nil
}

// ipv6OutputQueueRuleRe matches the IPv6 rule of the LB's nftables output chain
// (the rule that queues locally generated ICMPv6 addressed to a VIP to nfqlb)
// and captures its packet counter. It covers both the unfiltered form
// ("meta l4proto ipv6-icmp ip6 daddr @ipv6-vips counter ...") and the form
// restricted by ICMPv6 type ("ip6 daddr @ipv6-vips icmpv6 type { ... } counter ...").
var ipv6OutputQueueRuleRe = regexp.MustCompile(`ip6 daddr @ipv6-vips .*counter packets (\d+) bytes \d+ queue`)

// LBOutputIPv6QueuedPackets returns the packet counter of the IPv6 queue rule in
// the LB Pod's nftables output chain, i.e. how many locally generated ICMPv6
// packets addressed to a VIP were queued to nfqlb.
//
// nft is run directly as the exec command: with allowPrivilegeEscalation=false
// its file capabilities only take effect when it is the first program executed,
// not when started from a shell.
func LBOutputIPv6QueuedPackets(namespace, lbPod string) (uint64, error) {
	cmd := exec.Command("kubectl", "exec", "-n", namespace, lbPod, "-c", "loadbalancer", "--",
		"nft", "list", "chain", "inet", "meridio-lb", "output")
	out, err := utils.Run(cmd)
	if err != nil {
		return 0, fmt.Errorf("failed to list LB output chain: %w\noutput: %s", err, out)
	}
	return parseIPv6OutputQueuedPackets(out)
}

// parseIPv6OutputQueuedPackets extracts the IPv6 queue rule's packet counter
// from `nft list chain inet meridio-lb output` output.
func parseIPv6OutputQueuedPackets(out string) (uint64, error) {
	m := ipv6OutputQueueRuleRe.FindStringSubmatch(out)
	if m == nil {
		return 0, fmt.Errorf("IPv6 queue rule not found in output chain:\n%s", out)
	}
	return strconv.ParseUint(m[1], 10, 64)
}

// AppliedIPv6NextHops returns the IPv6 next hops in the ENC of the given Pod,
// once the sidecar has applied the current spec: condition Ready is True and
// its observedGeneration matches the ENC generation. It returns an error while
// that is not the case, or while the ENC lists no IPv6 next hop, so callers can
// poll it with Eventually.
func AppliedIPv6NextHops(namespace, pod string) ([]string, error) {
	out, err := utils.Run(exec.Command("kubectl", "get", "enc", pod, "-n", namespace, "-o", "json"))
	if err != nil {
		return nil, fmt.Errorf("failed to get ENC %s/%s: %w", namespace, pod, err)
	}
	return parseAppliedIPv6NextHops([]byte(out))
}

// parseAppliedIPv6NextHops implements AppliedIPv6NextHops on ENC JSON.
func parseAppliedIPv6NextHops(encJSON []byte) ([]string, error) {
	var enc meridio2v1alpha1.EndpointNetworkConfiguration
	if err := json.Unmarshal(encJSON, &enc); err != nil {
		return nil, fmt.Errorf("failed to parse ENC: %w", err)
	}
	ready := meta.FindStatusCondition(enc.Status.Conditions, "Ready")
	if ready == nil || ready.Status != metav1.ConditionTrue || ready.ObservedGeneration != enc.Generation {
		return nil, fmt.Errorf("ENC %s not applied for generation %d (Ready: %+v)", enc.Name, enc.Generation, ready)
	}
	var hops []string
	for _, gw := range enc.Spec.Gateways {
		for _, d := range gw.Domains {
			if d.IPFamily == "IPv6" {
				hops = append(hops, d.NextHops...)
			}
		}
	}
	if len(hops) == 0 {
		return nil, fmt.Errorf("ENC %s has no IPv6 next hops", enc.Name)
	}
	return hops, nil
}

// NeighborEntry returns the IPv6 neighbor entry for addr in the given Pod
// container (`ip -6 neigh show <addr>`), or an empty string if there is none.
// It only reads state and sends no traffic.
func NeighborEntry(namespace, pod, container, addr string) (string, error) {
	out, err := utils.Run(exec.Command("kubectl", "exec", "-n", namespace, pod, "-c", container, "--",
		"ip", "-6", "neigh", "show", addr))
	if err != nil {
		return "", fmt.Errorf("failed to read neighbor %s in %s/%s: %w", addr, namespace, pod, err)
	}
	return strings.TrimSpace(out), nil
}
