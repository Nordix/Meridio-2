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
	"fmt"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/gomega" //nolint:revive,staticcheck // Gomega DSL is idiomatic in Ginkgo test helpers

	"github.com/nordix/meridio-2/test/utils"
)

// birdSocketPath is the BIRD control socket path inside the router container,
// matching the mount configured in config/templates/lb-deployment.yaml.
const birdSocketPath = "/var/run/bird/bird.ctl"

// GatewayHealth describes the expected steady-state of a single Gateway's
// LB-pod deployment and BGP sessions. This is the unit that varies most
// between suites (replica count, VIPs, BGP protocol names), so ServiceHealth
// takes a slice of these rather than assuming exactly one gateway.
type GatewayHealth struct {
	// Name is the Gateway resource name, e.g. "gw-ds".
	Name string
	// LBReplicas is the expected number of Ready LB Pods for this gateway.
	LBReplicas int
	// VIPs are checked with ICMP reachability, one entry per IP family
	// advertised by this gateway.
	VIPs []string
	// BGPProtocols are BIRD protocol names (as shown by "birdc show protocols",
	// e.g. "NBR-gw-ds-router-v4") to assert as Established. These are derived
	// from L34Route resource names, which don't follow one fixed pattern
	// across suites, so callers must supply them explicitly. Leave nil to
	// skip the BGP check for this gateway.
	BGPProtocols []string
}

// TargetHealth describes the expected steady-state of a target/backend group
// selected by a pod label. Kept separate from GatewayHealth because targets
// are not always 1:1 with a single gateway (e.g. shared-appnetwork's targets
// serve two gateways; pod-cache-label has labeled vs. unlabeled targets under
// one gateway).
type TargetHealth struct {
	// Label is a pod label selector, e.g. "app=target-ds".
	Label string
	// Count is the expected number of Ready target Pods matching Label,
	// verified via their ENC resources being Ready.
	Count int
}

// ServiceHealth composes gateway(s) and target(s) into one health baseline
// for a namespace. It models the common core that applies to nearly every
// suite: Gateway status, LB Pod readiness, BGP session state, target ENC
// readiness, and VIP reachability. Suite- or protocol-specific checks (SCTP
// associations, TCP-AO key rotation, etc.) are intentionally not modeled
// here — layer those on top locally within the test that needs them.
type ServiceHealth struct {
	Namespace string
	Gateways  []GatewayHealth
	Targets   []TargetHealth
}

// VerifyHealthy asserts the full ServiceHealth as a sequence of independent
// Eventually blocks — one per gateway condition/target group — rather than
// one big retry loop. Each sub-check converges and reports on its own, so a
// slow-to-establish BGP session (for example) fails with its own clear
// message and stops retrying unrelated, already-satisfied checks (Gateway
// conditions, LB readiness, ENC readiness) on every poll cycle. Call sites
// still get a single VerifyHealthy call as "one check-do-it-all" for a
// gateway+target combination; only the internal retry granularity changed.
func VerifyHealthy(health ServiceHealth, timeout, polling time.Duration) {
	for _, gw := range health.Gateways {
		Eventually(func(g Gomega) {
			g.Expect(gatewayConditionTrue(health.Namespace, gw.Name, "Accepted")).
				To(BeTrue(), "gateway %s should be Accepted", gw.Name)
		}).WithTimeout(timeout).WithPolling(polling).Should(Succeed())

		Eventually(func(g Gomega) {
			g.Expect(gatewayConditionTrue(health.Namespace, gw.Name, "Programmed")).
				To(BeTrue(), "gateway %s should be Programmed", gw.Name)
		}).WithTimeout(timeout).WithPolling(polling).Should(Succeed())

		Eventually(func(g Gomega) {
			g.Expect(lbPodsReadyCount(health.Namespace, gw.Name)).
				To(Equal(gw.LBReplicas), "gateway %s should have %d Ready LB Pods", gw.Name, gw.LBReplicas)
		}).WithTimeout(timeout).WithPolling(polling).Should(Succeed())

		for _, proto := range gw.BGPProtocols {
			Eventually(func(g Gomega) {
				g.Expect(bgpEstablished(health.Namespace, gw.Name, proto)).
					To(BeTrue(), "BGP protocol %s for gateway %s should be Established", proto, gw.Name)
			}).WithTimeout(timeout).WithPolling(polling).Should(Succeed())
		}

		for _, vip := range gw.VIPs {
			Eventually(func() error { return Ping(vip) }).
				WithTimeout(timeout).WithPolling(polling).
				Should(Succeed(), "VIP %s (gateway %s) should be reachable", vip, gw.Name)
		}
	}

	for _, tgt := range health.Targets {
		Eventually(func(g Gomega) {
			g.Expect(encsReadyCount(health.Namespace, tgt.Label)).
				To(Equal(tgt.Count), "target selector %q should have %d Ready ENCs", tgt.Label, tgt.Count)
		}).WithTimeout(timeout).WithPolling(polling).Should(Succeed())
	}
}

// gatewayConditionTrue reports whether the given Gateway condition type is "True".
func gatewayConditionTrue(namespace, gatewayName, conditionType string) (bool, error) {
	cmd := exec.Command("kubectl", "get", "gateway", gatewayName, "-n", namespace,
		"-o", fmt.Sprintf("jsonpath={.status.conditions[?(@.type=='%s')].status}", conditionType))
	out, err := utils.Run(cmd)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) == "True", nil
}

// lbPodsReadyCount returns how many LB Pods for the given gateway have all
// containers Ready.
func lbPodsReadyCount(namespace, gatewayName string) (int, error) {
	cmd := exec.Command("kubectl", "get", "pods", "-n", namespace,
		"-l", fmt.Sprintf("gateway.networking.k8s.io/gateway-name=%s", gatewayName),
		"-o", "jsonpath={range .items[*]}{range .status.containerStatuses[*]}{.ready}{\" \"}{end}{\"\\n\"}{end}")
	out, err := utils.Run(cmd)
	if err != nil {
		return 0, err
	}

	ready := 0
	for _, line := range utils.GetNonEmptyLines(out) {
		allReady := true
		fields := strings.Fields(line)
		if len(fields) == 0 {
			allReady = false
		}
		for _, f := range fields {
			if f != "true" {
				allReady = false
				break
			}
		}
		if allReady {
			ready++
		}
	}
	return ready, nil
}

// bgpEstablished reports whether the named BIRD protocol on the LB Pod(s) of
// the given gateway shows "Established". If multiple LB Pods back the
// gateway, all of them must report Established.
func bgpEstablished(namespace, gatewayName, protocol string) (bool, error) {
	cmd := exec.Command("kubectl", "get", "pods", "-n", namespace,
		"-l", fmt.Sprintf("gateway.networking.k8s.io/gateway-name=%s", gatewayName),
		"-o", "jsonpath={.items[*].metadata.name}")
	out, err := utils.Run(cmd)
	if err != nil {
		return false, err
	}
	pods := utils.GetNonEmptyLines(strings.ReplaceAll(strings.TrimSpace(out), " ", "\n"))
	if len(pods) == 0 {
		return false, fmt.Errorf("no LB Pods found for gateway %s", gatewayName)
	}

	for _, pod := range pods {
		cmd := exec.Command("kubectl", "exec", "-n", namespace, pod,
			"-c", "router", "--", "birdc", "-s", birdSocketPath, "show", "protocols",
			fmt.Sprintf("'%s'", protocol))
		out, err := utils.Run(cmd)
		if err != nil {
			return false, err
		}
		if !strings.Contains(out, "Established") {
			return false, nil
		}
	}
	return true, nil
}

// encsReadyCount returns how many ENC resources for target Pods matching
// label have condition type Ready == True.
func encsReadyCount(namespace, label string) (int, error) {
	cmd := exec.Command("kubectl", "get", "pods", "-n", namespace,
		"-l", label, "--field-selector=status.phase=Running",
		"-o", "jsonpath={.items[*].metadata.name}")
	out, err := utils.Run(cmd)
	if err != nil {
		return 0, err
	}
	pods := utils.GetNonEmptyLines(strings.ReplaceAll(strings.TrimSpace(out), " ", "\n"))
	if len(pods) == 0 {
		return 0, nil
	}

	ready := 0
	for _, pod := range pods {
		cmd := exec.Command("kubectl", "get", "enc", pod, "-n", namespace,
			"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}")
		out, err := utils.Run(cmd)
		if err != nil {
			continue // ENC may not exist yet; counts as not-ready rather than erroring the whole check
		}
		if strings.TrimSpace(out) == "True" {
			ready++
		}
	}
	return ready, nil
}
