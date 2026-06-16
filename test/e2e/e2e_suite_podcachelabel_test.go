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

var _ = Describe("Pod Cache Label", func() {
	const (
		namespace   = "e2e-pod-cache-label"
		gatewayName = "gw-pcl"
		vip         = "60.0.0.1"
		cacheLabel  = "meridio-2.nordix.org/managed"
		cacheLabelV = "true"
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

	It("distributes TCP traffic only to labeled target", func() {
		lastingConn, lostConn, err := e2eutils.SendTraffic(vip, 5000, "tcp", 50)
		Expect(err).NotTo(HaveOccurred())
		Expect(lostConn).To(BeZero(), "no connections should be lost")
		Expect(lastingConn).To(HaveLen(1),
			"traffic should reach exactly 1 target (labeled only), got: %v", lastingConn)
		Expect(lastingConn).To(HaveKey(labeledPodName),
			"traffic should only reach the labeled Pod %q, got: %v", labeledPodName, lastingConn)
	})
})
