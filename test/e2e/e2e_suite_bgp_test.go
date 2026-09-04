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
	"fmt"
	"os/exec"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	e2eutils "github.com/nordix/meridio-2/test/e2e/utils"
	"github.com/nordix/meridio-2/test/utils"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// BGP VIP advertisement lifecycle, verified from the simulated DCGW (VPN
// gateway) side. Covers:
//   - VIP advertised to the DCGW over BGP with LB Pod next-hops (ECMP)
//   - VIP withdrawn when the app is scaled to 0 replicas
//   - VIP re-advertised when replicas are restored
//   - VIP withdrawn when all backing pods become NotReady (readiness probe fails
//     on the running pods — no rollout)
//   - VIP re-advertised when pods become Ready again
//
// The LB writes a per-DG readiness file (lb-ready-<dg>) only while at least one
// target endpoint is Ready; the router sidecar gates BGP VIP advertisement on
// that file. So removing all ready targets (via replicas=0 or an all-pods-
// NotReady state) withdraws the VIP from the DCGW, and restoring readiness
// re-advertises it.
//
// Pinned to the ipv4-simple topology (reused, not deployed here — same topology
// used by the Low MTU and Resiliency suites). These specs are disruptive and
// drive the shared VPN gateway, so the tree is Serial (never concurrent with
// other specs under `ginkgo -p`) and Ordered (deterministic sequence). State is
// fully restored via DeferCleanup.
var _ = Describe("E2E BGP VIP Advertisement", Label("ipv4"), Serial, Ordered, func() {
	const (
		namespace      = "e2e-ipv4-simple"
		targetApp      = "target-m"
		targetLabel    = "app=target-m"
		targetReplicas = 2
		vip            = "40.0.0.1"
		prefix         = vip + "/32"
	)

	var clientset *kubernetes.Clientset

	BeforeAll(func() {
		config, err := clientcmd.BuildConfigFromFlags("", clientcmd.RecommendedHomeFile)
		Expect(err).NotTo(HaveOccurred())
		clientset, err = kubernetes.NewForConfig(config)
		Expect(err).NotTo(HaveOccurred())

		// Guarantee the target Deployment is restored to a healthy, fully-ready
		// state even if an assertion below fails mid-sequence: scale back and
		// re-create the readiness file on every pod.
		DeferCleanup(func() {
			By("restoring target Deployment (scale back, re-mark ready)")
			_ = bgpScaleDeployment(clientset, namespace, targetApp, targetReplicas)
			Eventually(func() int {
				return bgpCountReadyTargets(clientset, namespace, targetLabel)
			}).WithTimeout(120 * time.Second).WithPolling(2 * time.Second).Should(Equal(targetReplicas))
			_ = bgpExecAllTargets(clientset, namespace, targetLabel, "touch", "/tmp/ready")
			Eventually(func() bool {
				ok, _ := e2eutils.VPNGatewayHasRoute(prefix)
				return ok
			}).WithTimeout(90 * time.Second).WithPolling(2 * time.Second).Should(BeTrue())
		})

		By("verifying the VIP is advertised to the DCGW at start")
		Eventually(func() bool {
			ok, _ := e2eutils.VPNGatewayHasRoute(prefix)
			return ok
		}).WithTimeout(60*time.Second).WithPolling(2*time.Second).
			Should(BeTrue(), "%s should be advertised before disruption", prefix)
	})

	It("advertises the VIP to the DCGW over BGP with LB Pod next-hops", func() {
		By("verifying the VIP is received on the DCGW via BGP")
		Eventually(func() bool {
			ok, _ := e2eutils.VPNGatewayHasRoute(prefix)
			return ok
		}).WithTimeout(60*time.Second).WithPolling(2*time.Second).
			Should(BeTrue(), "VIP %s should be received on the DCGW via BGP", prefix)

		By("verifying the VIP is installed with at least one ECMP next-hop")
		// One next-hop per LB Pod (ECMP). We assert at least one rather than an
		// exact count, since LB replica count is independent of the application
		// target count.
		Eventually(func() int {
			nextHops, _ := e2eutils.VPNGatewayRouteNextHops(prefix)
			return len(nextHops)
		}).WithTimeout(60*time.Second).WithPolling(2*time.Second).
			Should(BeNumerically(">=", 1), "expected at least one ECMP next-hop for %s", prefix)
	})

	It("withdraws VIP from DCGW when app replicas=0", func() {
		By("scaling the target Deployment to 0 replicas")
		Expect(bgpScaleDeployment(clientset, namespace, targetApp, 0)).To(Succeed())

		By("waiting for all target pods to terminate")
		Eventually(func() int {
			return bgpCountRunningTargets(clientset, namespace, targetLabel)
		}).WithTimeout(60 * time.Second).WithPolling(2 * time.Second).Should(BeZero())

		By("verifying the VIP is withdrawn from the DCGW")
		Eventually(func() bool {
			ok, _ := e2eutils.VPNGatewayHasRoute(prefix)
			return ok
		}).WithTimeout(90*time.Second).WithPolling(2*time.Second).
			Should(BeFalse(), "%s should be withdrawn once no targets exist", prefix)
	})

	It("re-advertises VIP to DCGW when app replicas are restored", func() {
		By("restoring replicas so pods exist and are Ready again")
		Expect(bgpScaleDeployment(clientset, namespace, targetApp, targetReplicas)).To(Succeed())
		Eventually(func() int {
			return bgpCountReadyTargets(clientset, namespace, targetLabel)
		}).WithTimeout(120 * time.Second).WithPolling(2 * time.Second).Should(Equal(targetReplicas))

		By("verifying the VIP is advertised again with ready pods")
		Eventually(func() bool {
			ok, _ := e2eutils.VPNGatewayHasRoute(prefix)
			return ok
		}).WithTimeout(90*time.Second).WithPolling(2*time.Second).
			Should(BeTrue(), "%s should be re-advertised once targets exist again", prefix)
	})

	It("withdraws VIP from DCGW when all backing pods become NotReady", func() {
		// Flip the running pods to NotReady in place by removing the readiness
		// file the probe checks (see targets.yaml). This simulates the app's
		// readiness probe starting to fail at runtime — no rollout, same pods.
		// All endpoints go Ready=false, so the LB removes the lb-ready file and
		// the router withdraws the VIP.
		By("removing /tmp/ready on all target pods to fail their readiness probe")
		Expect(bgpExecAllTargets(clientset, namespace, targetLabel, "rm", "-f", "/tmp/ready")).To(Succeed())

		By("waiting for all target pods to become NotReady (still Running)")
		Eventually(func() int {
			return bgpCountReadyTargets(clientset, namespace, targetLabel)
		}).WithTimeout(60 * time.Second).WithPolling(2 * time.Second).Should(BeZero())

		By("verifying the VIP is withdrawn from the DCGW")
		Eventually(func() bool {
			ok, _ := e2eutils.VPNGatewayHasRoute(prefix)
			return ok
		}).WithTimeout(90*time.Second).WithPolling(2*time.Second).
			Should(BeFalse(), "%s should be withdrawn when all pods are NotReady", prefix)
	})

	It("re-advertises VIP to DCGW when pods become Ready again", func() {
		By("re-creating /tmp/ready on all target pods to pass their readiness probe")
		Expect(bgpExecAllTargets(clientset, namespace, targetLabel, "touch", "/tmp/ready")).To(Succeed())

		By("waiting for all target pods to become Ready")
		Eventually(func() int {
			return bgpCountReadyTargets(clientset, namespace, targetLabel)
		}).WithTimeout(90 * time.Second).WithPolling(2 * time.Second).Should(Equal(targetReplicas))

		By("verifying the VIP is re-advertised to the DCGW")
		Eventually(func() bool {
			ok, _ := e2eutils.VPNGatewayHasRoute(prefix)
			return ok
		}).WithTimeout(90*time.Second).WithPolling(2*time.Second).
			Should(BeTrue(), "%s should be re-advertised once pods are Ready", prefix)

		By("verifying ECMP next-hops are present again on the DCGW")
		Eventually(func() int {
			nextHops, _ := e2eutils.VPNGatewayRouteNextHops(prefix)
			return len(nextHops)
		}).WithTimeout(90*time.Second).WithPolling(2*time.Second).
			Should(BeNumerically(">=", 1), "expected at least one ECMP next-hop for %s after recovery", prefix)
	})
})

// bgpScaleDeployment sets the replica count of a Deployment via the scale subresource.
func bgpScaleDeployment(clientset *kubernetes.Clientset, namespace, name string, replicas int32) error {
	scale, err := clientset.AppsV1().Deployments(namespace).GetScale(
		context.Background(), name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	scale.Spec.Replicas = replicas
	_, err = clientset.AppsV1().Deployments(namespace).UpdateScale(
		context.Background(), name, scale, metav1.UpdateOptions{})
	return err
}

// bgpCountRunningTargets returns the number of Pods matching the selector in the
// Running phase.
func bgpCountRunningTargets(clientset *kubernetes.Clientset, namespace, selector string) int {
	pods, err := clientset.CoreV1().Pods(namespace).List(context.Background(), metav1.ListOptions{
		LabelSelector: selector,
	})
	if err != nil {
		return -1
	}
	count := 0
	for i := range pods.Items {
		if pods.Items[i].Status.Phase == corev1.PodRunning {
			count++
		}
	}
	return count
}

// bgpCountReadyTargets returns the number of Pods matching the selector whose
// PodReady condition is True.
func bgpCountReadyTargets(clientset *kubernetes.Clientset, namespace, selector string) int {
	pods, err := clientset.CoreV1().Pods(namespace).List(context.Background(), metav1.ListOptions{
		LabelSelector: selector,
	})
	if err != nil {
		return -1
	}
	count := 0
	for i := range pods.Items {
		for _, cond := range pods.Items[i].Status.Conditions {
			if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
				count++
				break
			}
		}
	}
	return count
}

// bgpExecAllTargets runs a command in the example-target container of every Pod
// matching the selector, returning an error if any exec fails.
func bgpExecAllTargets(clientset *kubernetes.Clientset, namespace, selector string, args ...string) error {
	pods, err := clientset.CoreV1().Pods(namespace).List(context.Background(), metav1.ListOptions{
		LabelSelector: selector,
	})
	if err != nil {
		return err
	}
	for i := range pods.Items {
		pod := pods.Items[i].Name
		cmdArgs := append([]string{"exec", "-n", namespace, pod, "-c", "example-target", "--"}, args...)
		if _, err := utils.Run(exec.Command("kubectl", cmdArgs...)); err != nil {
			return fmt.Errorf("exec on %s failed: %w", pod, err)
		}
	}
	return nil
}
