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
	"context"
	"encoding/json"
	"strings"
	"os/exec"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	rgNamespace      = "e2e-common-appnet"
	rgGatewayName    = "gw-b1"
	rgGateIPv4       = "meridio-2.nordix.org/ipv4-connectivity"
	rgGateIPv6       = "meridio-2.nordix.org/ipv6-connectivity"
	rgGatewayLabel   = "gateway.networking.k8s.io/gateway-name"
)

var _ = Describe("Readiness Gates", Serial, Ordered, func() {
	var (
		clientset *kubernetes.Clientset
		lbPods    []corev1.Pod
	)

	BeforeAll(func() {
		config, err := clientcmd.BuildConfigFromFlags("", clientcmd.RecommendedHomeFile)
		Expect(err).NotTo(HaveOccurred())
		clientset, err = kubernetes.NewForConfig(config)
		Expect(err).NotTo(HaveOccurred())

		pods, err := clientset.CoreV1().Pods(rgNamespace).List(context.Background(), metav1.ListOptions{
			LabelSelector: rgGatewayLabel + "=" + rgGatewayName,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(pods.Items).NotTo(BeEmpty(), "No LB Pods found for gateway %s", rgGatewayName)
		lbPods = pods.Items
	})

	It("LB Pods should have readiness gates declared in spec", func() {
		for _, pod := range lbPods {
			var hasIPv4Gate bool
			for _, gate := range pod.Spec.ReadinessGates {
				if string(gate.ConditionType) == rgGateIPv4 {
					hasIPv4Gate = true
				}
			}
			Expect(hasIPv4Gate).To(BeTrue(),
				"Pod %s missing readiness gate %s", pod.Name, rgGateIPv4)
		}
	})

	It("LB Pods should have ipv4-connectivity condition True after BGP establishes", func() {
		// The gate should already be True by this point — deployment succeeded which
		// requires Pod readiness (including gates). Eventually is used defensively for
		// CI timing edge cases, but typically passes on first poll.
		for _, pod := range lbPods {
			Eventually(func() corev1.ConditionStatus {
				p, err := clientset.CoreV1().Pods(rgNamespace).Get(context.Background(), pod.Name, metav1.GetOptions{})
				if err != nil {
					return corev1.ConditionFalse
				}
				for _, c := range p.Status.Conditions {
					if string(c.Type) == rgGateIPv4 {
						return c.Status
					}
				}
				return corev1.ConditionFalse
			}).WithTimeout(30 * time.Second).WithPolling(1 * time.Second).
				Should(Equal(corev1.ConditionTrue),
					"Pod %s gate %s should be True", pod.Name, rgGateIPv4)
		}
	})

	It("ENC next-hops should include all healthy LB Pod IPs", func() {
		// Get LB Pod IPs on the internal subnet (from network-status annotation)
		lbIPs := getLBPodNetworkIPs(rgNamespace, rgGatewayName)
		Expect(lbIPs).NotTo(BeEmpty(), "No LB Pod IPs found")

		// Get a target Pod's ENC and extract next-hops for this Gateway
		targetPods, err := clientset.CoreV1().Pods(rgNamespace).List(context.Background(), metav1.ListOptions{
			LabelSelector: "app=target-b",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(targetPods.Items).NotTo(BeEmpty())
		targetPod := targetPods.Items[0].Name

		Eventually(func() []string {
			return getENCNextHops(rgNamespace, targetPod, rgGatewayName)
		}).WithTimeout(30 * time.Second).WithPolling(2 * time.Second).
			Should(ContainElements(lbIPs),
				"ENC next-hops should contain all LB Pod IPs")
	})

	It("should exclude LB Pod from next-hops when BGP drops", func() {
		disruptedPod := lbPods[0].Name

		// Get the IP of the disrupted Pod on the internal network
		podIP := getSinglePodNetworkIP(rgNamespace, disruptedPod, "net-b")
		Expect(podIP).NotTo(BeEmpty())

		// Disable BGP on the disrupted Pod
		cmd := exec.Command("kubectl", "exec", "-n", rgNamespace, disruptedPod,
			"-c", "router", "--", "birdc", "-s", "/var/run/bird/bird.ctl", "disable", "all")
		out, err := cmd.CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), "birdc disable failed: %s", string(out))

		// Get a target Pod for ENC lookup
		targetPods, err := clientset.CoreV1().Pods(rgNamespace).List(context.Background(), metav1.ListOptions{
			LabelSelector: "app=target-b",
		})
		Expect(err).NotTo(HaveOccurred())
		targetPod := targetPods.Items[0].Name

		// Verify disrupted Pod IP is excluded from ENC next-hops
		Eventually(func() []string {
			return getENCNextHops(rgNamespace, targetPod, rgGatewayName)
		}).WithTimeout(30 * time.Second).WithPolling(2 * time.Second).
			ShouldNot(ContainElement(podIP),
				"Disrupted Pod IP should be excluded from next-hops")

		// Re-enable BGP to restore state
		cmd = exec.Command("kubectl", "exec", "-n", rgNamespace, disruptedPod,
			"-c", "router", "--", "birdc", "-s", "/var/run/bird/bird.ctl", "enable", "all")
		_, _ = cmd.CombinedOutput()

		// Wait for gate to recover (BGP + hold time)
		Eventually(func() []string {
			return getENCNextHops(rgNamespace, targetPod, rgGatewayName)
		}).WithTimeout(30 * time.Second).WithPolling(2 * time.Second).
			Should(ContainElement(podIP),
				"Pod IP should be re-included after BGP recovery")
	})
})

// getLBPodNetworkIPs returns the internal network IPs of LB Pods for a gateway.
func getLBPodNetworkIPs(namespace, gatewayName string) []string {
	// Get pod names first, then fetch network-status per pod
	cmd := exec.Command("kubectl", "get", "pods", "-n", namespace,
		"-l", rgGatewayLabel+"="+gatewayName,
		"-o", "jsonpath={.items[*].metadata.name}")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil
	}

	var ips []string
	for _, podName := range strings.Fields(string(out)) {
		netCmd := exec.Command("kubectl", "get", "pod", podName, "-n", namespace,
			"-o", `jsonpath={.metadata.annotations.k8s\.v1\.cni\.cncf\.io/network-status}`)
		netOut, err := netCmd.CombinedOutput()
		if err != nil {
			continue
		}
		var networks []struct {
			Interface string   `json:"interface"`
			IPs       []string `json:"ips"`
		}
		if err := json.Unmarshal(netOut, &networks); err != nil {
			continue
		}
		for _, net := range networks {
			if net.Interface == "net-b" {
				ips = append(ips, net.IPs...)
			}
		}
	}
	return ips
}


// getSinglePodNetworkIP returns the IP of a specific Pod on a given interface.
func getSinglePodNetworkIP(namespace, podName, iface string) string {
	cmd := exec.Command("kubectl", "get", "pod", podName, "-n", namespace,
		"-o", `jsonpath={.metadata.annotations.k8s\.v1\.cni\.cncf\.io/network-status}`)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return ""
	}
	var networks []struct {
		Interface string   `json:"interface"`
		IPs       []string `json:"ips"`
	}
	if err := json.Unmarshal(out, &networks); err != nil {
		return ""
	}
	for _, net := range networks {
		if net.Interface == iface && len(net.IPs) > 0 {
			return net.IPs[0]
		}
	}
	return ""
}
// getENCNextHops returns the next-hops from an ENC for a specific gateway.
func getENCNextHops(namespace, encName, gatewayName string) []string {
	cmd := exec.Command("kubectl", "get", "endpointnetworkconfigurations.meridio-2.nordix.org",
		encName, "-n", namespace, "-o", "json")
	out, err := cmd.CombinedOutput()
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
	if err := json.Unmarshal(out, &enc); err != nil {
		return nil
	}

	var hops []string
	for _, gw := range enc.Spec.Gateways {
		if gw.Name == gatewayName {
			for _, domain := range gw.Domains {
				hops = append(hops, domain.NextHops...)
			}
		}
	}
	return hops
}



