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
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/nordix/meridio-2/test/utils"
)

const (
	rgNamespace   = "e2e-dual-stack"
	rgGateway     = "gw-ds"
	rgInterface   = "net-ds"
	rgGateIPv4    = "meridio-2.nordix.org/ipv4-connectivity"
	rgGateIPv6    = "meridio-2.nordix.org/ipv6-connectivity"
	rgBirdSocket  = "/var/run/bird/bird.ctl"
	rgProtocolV4  = "NBR-gw-ds-router-v4"
	rgProtocolV6  = "NBR-gw-ds-router-v6"
	rgTargetLabel = "app=target-ds"
)

var _ = Describe("Readiness Gates", Label("dual-stack"), Serial, Ordered, func() {
	var lbPods []string

	BeforeAll(func() {
		cmd := exec.Command("kubectl", "get", "pods", "-n", rgNamespace,
			"-l", "gateway.networking.k8s.io/gateway-name="+rgGateway,
			"-o", "jsonpath={.items[*].metadata.name}")
		out, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())
		lbPods = strings.Fields(out)
		Expect(lbPods).To(HaveLen(2), "Expected 2 LB Pods")
	})

	It("LB Pods should have both readiness gates declared and conditions True", func() {
		for _, pod := range lbPods {
			cmd := exec.Command("kubectl", "get", "pod", pod, "-n", rgNamespace, "-o", "json")
			out, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			var p struct {
				Spec struct {
					ReadinessGates []struct {
						ConditionType string `json:"conditionType"`
					} `json:"readinessGates"`
				} `json:"spec"`
				Status struct {
					Conditions []struct {
						Type   string `json:"type"`
						Status string `json:"status"`
					} `json:"conditions"`
				} `json:"status"`
			}
			Expect(utils.ParseJSON(out, &p)).To(Succeed())

			gateTypes := []string{}
			for _, g := range p.Spec.ReadinessGates {
				gateTypes = append(gateTypes, g.ConditionType)
			}
			Expect(gateTypes).To(ContainElement(rgGateIPv4))
			Expect(gateTypes).To(ContainElement(rgGateIPv6))

			// Gates should already be True (deployment succeeded)
			for _, c := range p.Status.Conditions {
				if c.Type == rgGateIPv4 || c.Type == rgGateIPv6 {
					Expect(c.Status).To(Equal("True"),
						"Pod %s gate %s should be True", pod, c.Type)
				}
			}
		}
	})

	It("ENC next-hops should include all healthy LB Pod IPs for both IP families", func() {
		ipv4IPs := rgGetLBIPs(rgInterface, false)
		ipv6IPs := rgGetLBIPs(rgInterface, true)
		Expect(ipv4IPs).To(HaveLen(2), "Expected 2 LB IPv4 IPs")
		Expect(ipv6IPs).To(HaveLen(2), "Expected 2 LB IPv6 IPs")

		targetPod := rgGetTargetPod()
		hops := rgGetENCNextHops(targetPod)
		Expect(hops).To(ContainElements(ipv4IPs))
		Expect(hops).To(ContainElements(ipv6IPs))
	})

	It("should set ipv4 gate False and exclude Pod from IPv4 next-hops when IPv4 BGP drops", func() {
		disruptedPod := lbPods[0]
		podIPv4 := rgGetSinglePodIP(disruptedPod, rgInterface, false)
		podIPv6 := rgGetSinglePodIP(disruptedPod, rgInterface, true)
		Expect(podIPv4).NotTo(BeEmpty())
		Expect(podIPv6).NotTo(BeEmpty())

		rgBirdctl(disruptedPod, "disable", rgProtocolV4)

		Eventually(func() string {
			return rgGetGateCondition(disruptedPod, rgGateIPv4)
		}).WithTimeout(15 * time.Second).WithPolling(1 * time.Second).
			Should(Equal("False"))

		Expect(rgGetGateCondition(disruptedPod, rgGateIPv6)).To(Equal("True"))

		targetPod := rgGetTargetPod()
		Eventually(func() []string {
			return rgGetENCNextHops(targetPod)
		}).WithTimeout(30 * time.Second).WithPolling(2 * time.Second).
			ShouldNot(ContainElement(podIPv4))

		hops := rgGetENCNextHops(targetPod)
		Expect(hops).To(ContainElement(podIPv6))
	})

	It("link flap should not change ipv4 gate status", func() {
		disruptedPod := lbPods[0]

		rgBirdctl(disruptedPod, "enable", rgProtocolV4)
		time.Sleep(1 * time.Second)
		rgBirdctl(disruptedPod, "disable", rgProtocolV4)

		Consistently(func() string {
			return rgGetGateCondition(disruptedPod, rgGateIPv4)
		}).WithTimeout(5 * time.Second).WithPolling(1 * time.Second).
			Should(Equal("False"))
	})

	It("should set ipv6 gate False when IPv6 BGP drops", func() {
		disruptedPod := lbPods[0]
		podIPv6 := rgGetSinglePodIP(disruptedPod, rgInterface, true)

		rgBirdctl(disruptedPod, "disable", rgProtocolV6)

		Eventually(func() string {
			return rgGetGateCondition(disruptedPod, rgGateIPv6)
		}).WithTimeout(15 * time.Second).WithPolling(1 * time.Second).
			Should(Equal("False"))

		Expect(rgGetGateCondition(disruptedPod, rgGateIPv4)).To(Equal("False"))

		targetPod := rgGetTargetPod()
		Eventually(func() []string {
			return rgGetENCNextHops(targetPod)
		}).WithTimeout(30 * time.Second).WithPolling(2 * time.Second).
			ShouldNot(ContainElement(podIPv6))
	})

	It("link flap should not change ipv6 gate status", func() {
		disruptedPod := lbPods[0]

		rgBirdctl(disruptedPod, "enable", rgProtocolV6)
		time.Sleep(1 * time.Second)
		rgBirdctl(disruptedPod, "disable", rgProtocolV6)

		Consistently(func() string {
			return rgGetGateCondition(disruptedPod, rgGateIPv6)
		}).WithTimeout(5 * time.Second).WithPolling(1 * time.Second).
			Should(Equal("False"))
	})

	It("should restore ipv4 gate True and re-include Pod after IPv4 BGP recovery", func() {
		disruptedPod := lbPods[0]
		podIPv4 := rgGetSinglePodIP(disruptedPod, rgInterface, false)

		rgBirdctl(disruptedPod, "enable", rgProtocolV4)

		Eventually(func() string {
			return rgGetGateCondition(disruptedPod, rgGateIPv4)
		}).WithTimeout(60 * time.Second).WithPolling(2 * time.Second).
			Should(Equal("True"))

		Expect(rgGetGateCondition(disruptedPod, rgGateIPv6)).To(Equal("False"))

		targetPod := rgGetTargetPod()
		Eventually(func() []string {
			return rgGetENCNextHops(targetPod)
		}).WithTimeout(30 * time.Second).WithPolling(2 * time.Second).
			Should(ContainElement(podIPv4))
	})

	It("should restore ipv6 gate True and re-include Pod after IPv6 BGP recovery", func() {
		disruptedPod := lbPods[0]
		podIPv6 := rgGetSinglePodIP(disruptedPod, rgInterface, true)

		rgBirdctl(disruptedPod, "enable", rgProtocolV6)

		Eventually(func() string {
			return rgGetGateCondition(disruptedPod, rgGateIPv6)
		}).WithTimeout(60 * time.Second).WithPolling(2 * time.Second).
			Should(Equal("True"))

		Expect(rgGetGateCondition(disruptedPod, rgGateIPv4)).To(Equal("True"))

		targetPod := rgGetTargetPod()
		Eventually(func() []string {
			return rgGetENCNextHops(targetPod)
		}).WithTimeout(30 * time.Second).WithPolling(2 * time.Second).
			Should(ContainElement(podIPv6))
	})
})

// rgBirdctl runs a birdc command on the router container of an LB Pod.
func rgBirdctl(pod, action, protocol string) {
	cmd := exec.Command("kubectl", "exec", "-n", rgNamespace, pod,
		"-c", "router", "--", "birdc", "-s", rgBirdSocket,
		fmt.Sprintf("%s '%s'", action, protocol))
	out, err := utils.Run(cmd)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "birdc %s %s failed: %s", action, protocol, out)
}

// rgGetGateCondition returns the status of a readiness gate condition on a Pod.
func rgGetGateCondition(pod, condType string) string {
	cmd := exec.Command("kubectl", "get", "pod", pod, "-n", rgNamespace,
		"-o", fmt.Sprintf("jsonpath={.status.conditions[?(@.type=='%s')].status}", condType))
	out, _ := utils.Run(cmd)
	return out
}

// rgGetLBIPs returns LB Pod IPs on the given interface (IPv4 or IPv6).
func rgGetLBIPs(iface string, ipv6 bool) []string {
	cmd := exec.Command("kubectl", "get", "pods", "-n", rgNamespace,
		"-l", "gateway.networking.k8s.io/gateway-name="+rgGateway,
		"-o", "jsonpath={.items[*].metadata.name}")
	out, err := utils.Run(cmd)
	if err != nil {
		return nil
	}
	var ips []string
	for _, pod := range strings.Fields(out) {
		ip := rgGetSinglePodIP(pod, iface, ipv6)
		if ip != "" {
			ips = append(ips, ip)
		}
	}
	return ips
}

// rgGetSinglePodIP returns one IP from a Pod's network-status for the given interface.
func rgGetSinglePodIP(pod, iface string, ipv6 bool) string {
	cmd := exec.Command("kubectl", "get", "pod", pod, "-n", rgNamespace,
		"-o", `jsonpath={.metadata.annotations.k8s\.v1\.cni\.cncf\.io/network-status}`)
	out, err := utils.Run(cmd)
	if err != nil {
		return ""
	}
	var networks []struct {
		Interface string   `json:"interface"`
		IPs       []string `json:"ips"`
	}
	if err := json.Unmarshal([]byte(out), &networks); err != nil {
		return ""
	}
	for _, net := range networks {
		if net.Interface == iface {
			for _, ip := range net.IPs {
				isV6 := strings.Contains(ip, ":")
				if isV6 == ipv6 {
					return ip
				}
			}
		}
	}
	return ""
}

// rgGetTargetPod returns the name of the first target Pod.
func rgGetTargetPod() string {
	cmd := exec.Command("kubectl", "get", "pods", "-n", rgNamespace,
		"-l", rgTargetLabel, "-o", "jsonpath={.items[0].metadata.name}")
	out, _ := utils.Run(cmd)
	return strings.TrimSpace(out)
}

// rgGetENCNextHops returns all nextHops from a target Pod's ENC for gw-ds.
func rgGetENCNextHops(targetPod string) []string {
	cmd := exec.Command("kubectl", "get", "endpointnetworkconfigurations.meridio-2.nordix.org",
		targetPod, "-n", rgNamespace, "-o", "json")
	out, err := utils.Run(cmd)
	if err != nil {
		return nil
	}
	var enc struct {
		Spec struct {
			Gateways []struct {
				Name    string `json:"name"`
				Domains []struct {
					NextHops []string `json:"nextHops"`
				} `json:"domains"`
			} `json:"gateways"`
		} `json:"spec"`
	}
	if err := utils.ParseJSON(out, &enc); err != nil {
		return nil
	}
	var hops []string
	for _, gw := range enc.Spec.Gateways {
		if gw.Name == rgGateway {
			for _, domain := range gw.Domains {
				hops = append(hops, domain.NextHops...)
			}
		}
	}
	return hops
}
