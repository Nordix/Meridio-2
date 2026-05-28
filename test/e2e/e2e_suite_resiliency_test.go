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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

var _ = Describe("Resiliency", Serial, Ordered, func() {
	const (
		namespace   = "e2e-low-mtu"
		gatewayName = "gw-m1"
		vip         = "40.0.0.1"
	)

	var (
		clientset *kubernetes.Clientset
		sllbPod   string
	)

	BeforeAll(func() {
		config, err := clientcmd.BuildConfigFromFlags("", clientcmd.RecommendedHomeFile)
		Expect(err).NotTo(HaveOccurred())
		clientset, err = kubernetes.NewForConfig(config)
		Expect(err).NotTo(HaveOccurred())

		// Find the single LB Pod for the gateway (low-mtu has replicas=1)
		pods, err := clientset.CoreV1().Pods(namespace).List(context.Background(), metav1.ListOptions{
			LabelSelector: fmt.Sprintf("gateway.networking.k8s.io/gateway-name=%s", gatewayName),
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(pods.Items).To(HaveLen(1), "Expected exactly 1 LB Pod for gateway %s", gatewayName)
		sllbPod = pods.Items[0].Name

		// Verify the LB Pod is Ready before starting tests
		Expect(isPodReady(clientset, namespace, sllbPod)).To(BeTrue(),
			"LB Pod %s should be Ready before test starts", sllbPod)

		// Verify baseline traffic via the single LB Pod
		Eventually(func() error { return e2eutils.Ping(vip) }).
			WithTimeout(30 * time.Second).Should(Succeed())
	})

	It("recovers after NFQLB process kill", func() {
		By("confirming Pod is Ready and traffic works before disruption")
		Expect(isPodReady(clientset, namespace, sllbPod)).To(BeTrue())
		Expect(e2eutils.Ping(vip)).To(Succeed())

		By("getting current restart count")
		restartsBefore := getContainerRestarts(clientset, namespace, sllbPod, "loadbalancer")

		By("killing NFQLB process inside LB container")
		cmd := exec.Command("kubectl", "exec", "-n", namespace, sllbPod,
			"-c", "loadbalancer", "--", "sh", "-c", "kill -9 $(pidof nfqlb)")
		out, err := cmd.CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), "kill command failed: %s", string(out))

		By("verifying LB container restarts")
		Eventually(func() int32 {
			return getContainerRestarts(clientset, namespace, sllbPod, "loadbalancer")
		}).WithTimeout(15 * time.Second).WithPolling(1 * time.Second).
			Should(BeNumerically(">", restartsBefore))

		By("verifying Pod transitions through not-Ready state")
		notReadySeen := false
		for i := 0; i < 3; i++ {
			time.Sleep(3 * time.Second)
			if !isPodReady(clientset, namespace, sllbPod) {
				notReadySeen = true
				break
			}
		}
		if !notReadySeen {
			GinkgoWriter.Println("Note: Pod recovered too fast to observe not-Ready state")
		}

		By("capturing LB container logs from crashed instance")
		logCmd := exec.Command("kubectl", "logs", "-n", namespace, sllbPod,
			"-c", "loadbalancer", "--previous", "--tail=10")
		if logOut, err := logCmd.CombinedOutput(); err == nil {
			GinkgoWriter.Printf("Previous LB container logs:\n%s\n", string(logOut))
		}

		By("waiting for Pod to become Ready again")
		Eventually(func() bool {
			return isPodReady(clientset, namespace, sllbPod)
		}).WithTimeout(60 * time.Second).WithPolling(2 * time.Second).
			Should(BeTrue())

		By("verifying traffic resumes after recovery")
		Eventually(func() error { return e2eutils.Ping(vip) }).
			WithTimeout(60 * time.Second).WithPolling(2 * time.Second).
			Should(Succeed())
	})
})

func getContainerRestarts(clientset *kubernetes.Clientset, namespace, podName, containerName string) int32 {
	pod, err := clientset.CoreV1().Pods(namespace).Get(context.Background(), podName, metav1.GetOptions{})
	if err != nil {
		return -1
	}
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.Name == containerName {
			return cs.RestartCount
		}
	}
	return -1
}

func isPodReady(clientset *kubernetes.Clientset, namespace, podName string) bool {
	pod, err := clientset.CoreV1().Pods(namespace).Get(context.Background(), podName, metav1.GetOptions{})
	if err != nil {
		return false
	}
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}
