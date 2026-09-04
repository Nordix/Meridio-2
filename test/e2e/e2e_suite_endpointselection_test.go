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

	meridio2v1alpha1 "github.com/nordix/meridio-2/api/v1alpha1"
	e2eutils "github.com/nordix/meridio-2/test/e2e/utils"
	"github.com/nordix/meridio-2/test/utils"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("Endpoint Selection", Ordered, Label("ipv4"), func() {
	const (
		namespace   = "e2e-endpoint-selection"
		gatewayName = "gw-eps"
		vip         = "50.0.0.1"
		cacheLabel  = "meridio-2.nordix.org/managed"
		cacheLabelV = "true"
		// labeledSelector discovers the labeled target Pods for the
		// readiness-flip cases. It is scoped to variant=labeled because the
		// unlabeled deployment also carries app=target-eps.
		labeledSelector = "app=target-eps,variant=labeled"
		// readyFile is the file-existence readiness marker checked by the
		// target container's readiness probe. Removing it makes the Pod
		// not-Ready; recreating it makes it Ready again.
		readyFile = "/tmp/ready"
		// nconn is the connection count for each SendTraffic run. High enough
		// that both of the 2 Maglev backends reliably appear in the per-target
		// distribution (the "both backends appear" property is statistical).
		nconn = 100
	)

	SetDefaultEventuallyTimeout(2 * time.Minute)
	SetDefaultEventuallyPollingInterval(2 * time.Second)

	var (
		k8sClient      client.Client
		clientset      *kubernetes.Clientset
		labeledPodName string
	)

	BeforeEach(func() {
		config, err := clientcmd.BuildConfigFromFlags("", clientcmd.RecommendedHomeFile)
		Expect(err).NotTo(HaveOccurred())

		scheme := runtime.NewScheme()
		Expect(meridio2v1alpha1.AddToScheme(scheme)).To(Succeed())
		Expect(corev1.AddToScheme(scheme)).To(Succeed())
		Expect(appsv1.AddToScheme(scheme)).To(Succeed())

		k8sClient, err = client.New(config, client.Options{Scheme: scheme})
		Expect(err).NotTo(HaveOccurred())

		clientset, err = kubernetes.NewForConfig(config)
		Expect(err).NotTo(HaveOccurred())
	})

	It("Gateway should be Accepted", func() {
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "gateway", gatewayName, "-n", namespace,
				"-o", "jsonpath={.status.conditions[?(@.type=='Accepted')].status}")
			out, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(out).To(Equal("True"))
		}).Should(Succeed())
	})

	It("Gateway should be Programmed", func() {
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "gateway", gatewayName, "-n", namespace,
				"-o", "jsonpath={.status.conditions[?(@.type=='Programmed')].status}")
			out, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(out).To(Equal("True"))
		}).Should(Succeed())
	})

	It("LB Deployment pods have the cache label", func() {
		ctx := context.Background()

		// Find LB Pods for the gateway
		Eventually(func() bool {
			pods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
				LabelSelector: fmt.Sprintf("gateway.networking.k8s.io/gateway-name=%s", gatewayName),
			})
			if err != nil || len(pods.Items) == 0 {
				return false
			}
			for _, pod := range pods.Items {
				if pod.Labels[cacheLabel] != cacheLabelV {
					return false
				}
			}
			return pods.Items[0].Status.Phase == corev1.PodRunning
		}).Should(BeTrue(),
			"LB Pods should be Running and have the cache label")
	})

	It("creates ENC for labeled app Pod with non-empty next-hops", func() {
		ctx := context.Background()

		// Wait for labeled Pod to be Running
		Eventually(func() bool {
			pods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
				LabelSelector: "variant=labeled",
			})
			if err != nil || len(pods.Items) == 0 {
				return false
			}
			labeledPodName = pods.Items[0].Name
			return pods.Items[0].Status.Phase == corev1.PodRunning
		}).Should(BeTrue(),
			"Labeled app Pod should be Running")

		// Verify ENC exists with non-empty next-hops (proves LB Pod is visible)
		Eventually(func() bool {
			enc := &meridio2v1alpha1.EndpointNetworkConfiguration{}
			if err := k8sClient.Get(ctx, types.NamespacedName{
				Name: labeledPodName, Namespace: namespace,
			}, enc); err != nil {
				return false
			}
			for _, gw := range enc.Spec.Gateways {
				for _, domain := range gw.Domains {
					if len(domain.NextHops) > 0 {
						return true
					}
				}
			}
			return false
		}).Should(BeTrue(),
			"ENC should exist for labeled Pod with non-empty next-hops")
	})

	It("does NOT create ENC for unlabeled app Pod", func() {
		ctx := context.Background()

		// Wait for unlabeled Pod to be Running
		var unlabeledPodName string
		Eventually(func() bool {
			pods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
				LabelSelector: "variant=unlabeled",
			})
			if err != nil || len(pods.Items) == 0 {
				return false
			}
			unlabeledPodName = pods.Items[0].Name
			return pods.Items[0].Status.Phase == corev1.PodRunning
		}).Should(BeTrue(),
			"Unlabeled app Pod should be Running")

		// Verify ENC does NOT exist
		Consistently(func() error {
			enc := &meridio2v1alpha1.EndpointNetworkConfiguration{}
			return k8sClient.Get(ctx, types.NamespacedName{
				Name: unlabeledPodName, Namespace: namespace,
			}, enc)
		}).WithTimeout(10*time.Second).WithPolling(2*time.Second).ShouldNot(Succeed(),
			"ENC should NOT be created for unlabeled Pod")
	})

	It("VIP is reachable via ICMP", func() {
		Eventually(func() error { return e2eutils.Ping(vip) }).
			Should(Succeed(), "VIP should be reachable from VPN gateway")
	})

	It("distributes TCP traffic only to labeled targets", func() {
		// With replicas: 2 on the labeled deployment, traffic is distributed
		// across the (ready) labeled Pods only. The unlabeled Pod is excluded
		// by the pod-cache-label filter and must never receive traffic.
		labeled := listLabeledPodNames(clientset, namespace, labeledSelector)
		Expect(len(labeled)).To(BeNumerically(">=", 2),
			"expected at least 2 labeled target Pods, got: %v", labeled)

		labeledSet := map[string]struct{}{}
		for _, name := range labeled {
			labeledSet[name] = struct{}{}
		}

		lastingConn, lostConn, err := e2eutils.SendTraffic(vip, 5000, "tcp", nconn)
		Expect(err).NotTo(HaveOccurred())
		Expect(lostConn).To(BeZero(), "no connections should be lost")
		Expect(lastingConn).NotTo(BeEmpty(), "traffic should reach at least one labeled target")
		for host := range lastingConn {
			Expect(labeledSet).To(HaveKey(host),
				"traffic reached non-labeled target %q, got: %v", host, lastingConn)
		}
	})

	// Readiness -> traffic distribution.
	//
	// A target Pod's PodReady condition (driven by the file-existence readiness
	// probe) is mirrored into LoadBalancerEndpointSlice.spec.endpoints[].ready
	// by the DistributionGroup controller; the LB controller skips !Ready
	// endpoints when reconciling nfqlb targets. Flipping readiness therefore
	// (de)activates traffic to that Pod. We assert this end to end using
	// converge-by-probing: wrap fresh, short SendTraffic runs in Eventually
	// (converge) and Consistently (stable) so we never measure a run that
	// straddles the flip.

	Context("readiness controls traffic distribution", func() {
		var pickedPod string

		It("stops sending traffic to a not-ready target", func() {
			labeled := listLabeledPodNames(clientset, namespace, labeledSelector)
			Expect(len(labeled)).To(BeNumerically(">=", 2),
				"expected at least 2 labeled target Pods, got: %v", labeled)
			pickedPod = labeled[0]

			By(fmt.Sprintf("making labeled target %q not-ready", pickedPod))
			setTargetReady(namespace, pickedPod, false)

			// Optional confirmation that P leaves PodReady; the traffic
			// convergence below already proves the effect end to end.
			Eventually(func() bool {
				return e2eutils.IsPodReady(clientset, namespace, pickedPod)
			}).WithTimeout(30 * time.Second).WithPolling(1 * time.Second).
				Should(BeFalse(), "picked Pod %q should become not-Ready", pickedPod)

			By("converging: fresh traffic runs should stop reaching the not-ready Pod")
			Eventually(func(g Gomega) {
				lastingConn, lostConn, err := e2eutils.SendTraffic(vip, 5000, "tcp", nconn)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(lostConn).To(BeZero())
				g.Expect(lastingConn).NotTo(HaveKey(pickedPod),
					"traffic still reaching not-ready Pod %q, got: %v", pickedPod, lastingConn)
			}).WithTimeout(60 * time.Second).WithPolling(2 * time.Second).Should(Succeed())

			By("verifying the not-ready Pod stays absent while the other Pod(s) still serve")
			Consistently(func(g Gomega) {
				lastingConn, lostConn, err := e2eutils.SendTraffic(vip, 5000, "tcp", nconn)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(lostConn).To(BeZero(), "no connections should be lost")
				g.Expect(lastingConn).NotTo(HaveKey(pickedPod),
					"not-ready Pod %q should receive no traffic, got: %v", pickedPod, lastingConn)
				g.Expect(lastingConn).NotTo(BeEmpty(),
					"other ready Pod(s) should still receive traffic, got: %v", lastingConn)
			}).WithTimeout(15 * time.Second).WithPolling(2 * time.Second).Should(Succeed())
		})

		It("resumes sending traffic to a restored target", func() {
			Expect(pickedPod).NotTo(BeEmpty(), "flip to not-ready must run first (Ordered)")

			By(fmt.Sprintf("making labeled target %q ready again", pickedPod))
			setTargetReady(namespace, pickedPod, true)
			waitPodReady(clientset, namespace, pickedPod)

			By("converging: fresh traffic runs should reach the restored Pod again")
			Eventually(func(g Gomega) {
				lastingConn, lostConn, err := e2eutils.SendTraffic(vip, 5000, "tcp", nconn)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(lostConn).To(BeZero())
				g.Expect(lastingConn).To(HaveKey(pickedPod),
					"traffic should reach restored Pod %q again, got: %v", pickedPod, lastingConn)
			}).WithTimeout(60 * time.Second).WithPolling(2 * time.Second).Should(Succeed())
		})
	})

	// Leave a clean state even on mid-sequence failure: unconditionally
	// recreate the readiness file on all labeled target Pods.
	AfterAll(func() {
		for _, name := range listLabeledPodNames(clientset, namespace, labeledSelector) {
			// Best-effort: the Pod may already be ready or gone.
			cmd := exec.Command("kubectl", "exec", "-n", namespace, name,
				"-c", "example-target", "--", "sh", "-c", "touch "+readyFile)
			_, _ = utils.Run(cmd)
		}
	})
})

// listLabeledPodNames returns the names of Pods matching selector in namespace.
func listLabeledPodNames(clientset *kubernetes.Clientset, namespace, selector string) []string {
	pods, err := clientset.CoreV1().Pods(namespace).List(context.Background(), metav1.ListOptions{
		LabelSelector: selector,
	})
	Expect(err).NotTo(HaveOccurred())
	names := make([]string, 0, len(pods.Items))
	for _, pod := range pods.Items {
		names = append(names, pod.Name)
	}
	return names
}

// setTargetReady flips the readiness file on the target container via a single
// unconditional kubectl exec (touch to make ready, rm -f to make not-ready).
func setTargetReady(namespace, podName string, ready bool) {
	op := "touch"
	if !ready {
		op = "rm -f"
	}
	cmd := exec.Command("kubectl", "exec", "-n", namespace, podName,
		"-c", "example-target", "--", "sh", "-c", op+" /tmp/ready")
	out, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred(), "kubectl exec failed: %s", out)
}

// waitPodReady waits until the named Pod reports the PodReady condition.
func waitPodReady(clientset *kubernetes.Clientset, namespace, podName string) {
	Eventually(func() bool {
		return e2eutils.IsPodReady(clientset, namespace, podName)
	}).WithTimeout(60 * time.Second).WithPolling(1 * time.Second).
		Should(BeTrue(), "Pod %q should become Ready", podName)
}
