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
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	meridio2v1alpha1 "github.com/nordix/meridio-2/api/v1alpha1"
	e2eutils "github.com/nordix/meridio-2/test/e2e/utils"
	"github.com/nordix/meridio-2/test/utils"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// DistributionGroup churn test: repeatedly creates and deletes a
// DistributionGroup under a new name each iteration (dg-churn-1, dg-churn-2,
// ...), all pointing at the same target Pods and the same Gateway.
//
// Why identity changes, not just count: this specifically targets the
// per-restart orphaned-shm-segment leak (ENP2 nVIP SLLBR incident / issue
// #270). Each new name maps 1:1 to a new NFQLB shared-memory segment
// (nfqlb init --shm=<DG-name>); deleting the DG only cleans up that segment
// if DeleteInstance actually runs and succeeds in the same process lifetime.
// Reusing one DG name across iterations would not exercise this: identical
// successive names collapse onto the same segment, which is routine
// idempotent re-init, not the leak path. The number of distinct shm segments
// that accumulate on the LB Pod should track the number of *currently live*
// DGs, not the number of DGs that have ever existed.
var _ = Describe("DistributionGroup Churn", Label("ipv4"), Serial, Ordered, func() {
	const (
		namespace    = "e2e-ipv4-simple"
		gatewayName  = "gw-m1"
		targetLabel  = "app=target-m"
		churnRounds  = 30
		dgNamePrefix = "dg-churn-"
	)

	var (
		k8sClient client.Client
		clientset *kubernetes.Clientset
		sllbPod   string
	)

	SetDefaultEventuallyTimeout(30 * time.Second)
	SetDefaultEventuallyPollingInterval(1 * time.Second)

	BeforeAll(func() {
		config, err := clientcmd.BuildConfigFromFlags("", clientcmd.RecommendedHomeFile)
		Expect(err).NotTo(HaveOccurred())

		scheme := runtime.NewScheme()
		Expect(meridio2v1alpha1.AddToScheme(scheme)).To(Succeed())
		Expect(corev1.AddToScheme(scheme)).To(Succeed())

		k8sClient, err = client.New(config, client.Options{Scheme: scheme})
		Expect(err).NotTo(HaveOccurred())

		clientset, err = kubernetes.NewForConfig(config)
		Expect(err).NotTo(HaveOccurred())

		pods, err := clientset.CoreV1().Pods(namespace).List(context.Background(), metav1.ListOptions{
			LabelSelector: fmt.Sprintf("gateway.networking.k8s.io/gateway-name=%s", gatewayName),
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(pods.Items).To(HaveLen(1), "Expected exactly 1 LB Pod for gateway %s", gatewayName)
		sllbPod = pods.Items[0].Name
	})

	// Any DG left over from a prior failed run (same prefix) must not pollute
	// the leak-detection assertion.
	AfterAll(func() {
		for i := 1; i <= churnRounds; i++ {
			dg := &meridio2v1alpha1.DistributionGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      dgNamePrefix + strconv.Itoa(i),
					Namespace: namespace,
				},
			}
			_ = k8sClient.Delete(context.Background(), dg)
		}
	})

	It("does not accumulate stale NFQLB shm segments as DistributionGroups are created and deleted under new names", func() {
		baselineShmCount := shmSegmentCount(sllbPod, namespace)
		baselineRuleCount := ipRuleCount(sllbPod, namespace)
		testStartTime := time.Now()
		GinkgoWriter.Printf("baseline shm segment count: %d, baseline ip rule count: %d\n", baselineShmCount, baselineRuleCount)

		for i := 1; i <= churnRounds; i++ {
			name := dgNamePrefix + strconv.Itoa(i)

			By(fmt.Sprintf("creating %s (round %d/%d)", name, i, churnRounds))
			dg := &meridio2v1alpha1.DistributionGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: namespace,
				},
				Spec: meridio2v1alpha1.DistributionGroupSpec{
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"app": "target-m"},
					},
					Maglev: &meridio2v1alpha1.MaglevConfig{MaxEndpoints: 32},
					ParentRefs: []meridio2v1alpha1.ParentReference{
						{Name: gatewayName},
					},
				},
			}
			Expect(k8sClient.Create(context.Background(), dg)).To(Succeed())

			// Give the LB controller a chance to actually call nfqlb init
			// before tearing the DG down again — otherwise this would only
			// exercise API-server churn, not NFQLB instance lifecycle churn.
			Eventually(func() bool {
				return shmSegmentExists(sllbPod, namespace, name)
			}).Should(BeTrue(), "nfqlb init should have created a shm segment for %s", name)

			By(fmt.Sprintf("deleting %s", name))
			Expect(k8sClient.Delete(context.Background(), dg)).To(Succeed())

			Eventually(func() bool {
				err := k8sClient.Get(context.Background(), types.NamespacedName{Name: name, Namespace: namespace}, &meridio2v1alpha1.DistributionGroup{})
				return err != nil
			}).Should(BeTrue(), "%s should be gone from the API", name)

			// This is the actual regression check: DeleteInstance must have
			// unlinked the shm segment before the next iteration creates a
			// differently-named DG. Without the DeleteInstance ordering fix,
			// this either leaks immediately (shm never cleaned up) or, under
			// GC-only recovery, only gets cleaned on the next container
			// restart — which this loop never does.
			Eventually(func() bool {
				return !shmSegmentExists(sllbPod, namespace, name)
			}).Should(BeTrue(), "shm segment for %s should be removed after DG deletion", name)
		}

		finalShmCount := shmSegmentCount(sllbPod, namespace)
		GinkgoWriter.Printf("final shm segment count: %d\n", finalShmCount)
		Expect(finalShmCount).To(Equal(baselineShmCount),
			"shm segment count must return to baseline after %d create/delete rounds "+
				"with per-iteration unique DG names — any growth indicates orphaned "+
				"NFQLB instances (see ENP2 nVIP SLLBR incident)", churnRounds)

		// shm segment count alone does not catch the DeleteInstance ordering
		// bug: `nfqlb delete --shm=` still unlinks the segment even when the
		// preceding target deactivation failed, so the shm directory looks
		// clean either way. The actual signal is twofold:
		//
		// 1. Policy routes (ip rule / ip route) for a deleted target are only
		//    removed if DeleteTarget's deactivate call succeeds — on the buggy
		//    ordering, deactivate always fails ("FAILED mapSharedData") because
		//    the shm segment was already unlinked by the time it runs, so route
		//    cleanup is skipped and rules accumulate across iterations.
		finalRuleCount := ipRuleCount(sllbPod, namespace)
		GinkgoWriter.Printf("final ip rule count: %d\n", finalRuleCount)
		Expect(finalRuleCount).To(Equal(baselineRuleCount),
			"ip rule count must return to baseline after %d create/delete rounds — "+
				"leftover rules indicate DeleteTarget's route cleanup was skipped "+
				"(see DeleteInstance ordering bug: shm unlinked before deactivate, "+
				"causing every deactivate call to fail with FAILED mapSharedData)", churnRounds)

		// 2. The controller logs an error on every failed cleanup. Zero
		//    tolerance here: a correctly-ordered DeleteInstance has no reason
		//    to ever fail target deactivation against a segment it hasn't
		//    unlinked yet.
		errorCount := controllerErrorLogCount(sllbPod, namespace, testStartTime)
		GinkgoWriter.Printf("controller error-level log lines during test: %d\n", errorCount)
		Expect(errorCount).To(BeZero(),
			"the loadbalancer controller must not log any error-level lines while "+
				"creating and deleting DistributionGroups under normal conditions — "+
				"see kubectl logs -n %s %s -c loadbalancer for the actual errors "+
				"(e.g. \"Failed to delete NFQLB service\" / \"FAILED mapSharedData\")",
			namespace, sllbPod)
	})

	It("still serves traffic correctly after sustained DG churn (dg-m1 untouched throughout)", func() {
		Eventually(func() error { return e2eutils.Ping("40.0.0.1") }).Should(Succeed())
	})
})

// shmSegmentCount returns the number of entries under /dev/shm in the
// loadbalancer container of the given Pod.
func shmSegmentCount(podName, namespace string) int {
	cmd := exec.Command("kubectl", "exec", "-n", namespace, podName,
		"-c", "loadbalancer", "--", "sh", "-c", "ls -1 /dev/shm | wc -l")
	out, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred())
	count, err := strconv.Atoi(strings.TrimSpace(out))
	Expect(err).NotTo(HaveOccurred())
	return count
}

// shmSegmentExists checks whether a shm segment with the exact given name
// exists under /dev/shm in the loadbalancer container of the given Pod.
func shmSegmentExists(podName, namespace, name string) bool {
	cmd := exec.Command("kubectl", "exec", "-n", namespace, podName,
		"-c", "loadbalancer", "--", "sh", "-c",
		fmt.Sprintf("test -e /dev/shm/%s && echo yes || echo no", name))
	out, err := utils.Run(cmd)
	if err != nil {
		return false
	}
	return strings.TrimSpace(out) == "yes"
}

// ipRuleCount returns the number of policy routing rules (ip rule list) in
// the loadbalancer container's network namespace. Used as a leak detector:
// a correctly-ordered DeleteInstance removes a target's rule when its
// DistributionGroup is deleted, so this count should return to baseline
// after a create/delete cycle completes.
func ipRuleCount(podName, namespace string) int {
	cmd := exec.Command("kubectl", "exec", "-n", namespace, podName,
		"-c", "loadbalancer", "--", "sh", "-c", "ip rule list | wc -l")
	out, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred())
	count, err := strconv.Atoi(strings.TrimSpace(out))
	Expect(err).NotTo(HaveOccurred())
	return count
}

// controllerErrorLogCount returns the number of error-level log lines
// emitted by the loadbalancer container since the given time. The
// loadbalancer controller logs structured JSON with a "level" field; this
// counts lines where that field is "error", regardless of message content,
// so it catches any failure mode in the create/delete path, not just the
// specific message known today.
func controllerErrorLogCount(podName, namespace string, since time.Time) int {
	cmd := exec.Command("kubectl", "logs", "-n", namespace, podName,
		"-c", "loadbalancer", "--since-time="+since.UTC().Format(time.RFC3339))
	out, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred())

	count := 0
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, `"level":"error"`) {
			count++
		}
	}
	return count
}
