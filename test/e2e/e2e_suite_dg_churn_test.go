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
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// DistributionGroup churn test: repeatedly creates and deletes a
// DistributionGroup under a new name each iteration (dg-churn-1, dg-churn-2,
// ...), all pointing at the same target Pods and the same Gateway.
//
// Why identity changes, not just count: each new name maps 1:1 to a new NFQLB
// shared-memory segment (nfqlb init --shm=<DG-name>) and a new contiguous
// fwmark range. Deleting the DG only releases those if DeleteInstance runs its
// cleanup steps in the right order. Reusing one DG name across iterations
// would not exercise this: identical successive names collapse onto the same
// segment, which is routine idempotent re-init, not the leak path.
//
// What this guards (ENP2 nVIP SLLBR incident / issue #270): DeleteInstance
// used to unlink the shm segment *before* deactivating targets. Since
// `nfqlb deactivate` must map the segment to work, every deactivate then
// failed with "FAILED mapSharedData", and because deleteTargetNoLock returns
// early on that error it never reached policy-route cleanup — leaking an
// ip rule + routing table per target on every DG deletion.
//
// Note on assertion choice: shm segment count alone does NOT catch that bug.
// `nfqlb delete --shm=` still unlinks the segment even when the preceding
// deactivate failed, so /dev/shm looks clean either way. The discriminating
// signals are the leaked policy rules and the controller's error-level logs,
// both asserted below.
var _ = Describe("DistributionGroup Churn", Label("ipv4"), Serial, Ordered, func() {
	const (
		namespace    = "e2e-ipv4-simple"
		gatewayName  = "gw-m1"
		targetApp    = "target-m"
		vip          = "40.0.0.1"
		churnRounds  = 30
		dgNamePrefix = "dg-churn-"
		maxEndpoints = 32

		// Explicit per-assertion timeouts. Deliberately not using
		// SetDefaultEventuallyTimeout: those calls run at tree-construction
		// time and mutate global Gomega state for the entire suite, so
		// whichever spec file is constructed last wins. Several suites here
		// set 5 minutes, which would silently override a shorter value set
		// from this file and make a stuck step hang for minutes per round.
		stepTimeout = 60 * time.Second
		stepPolling = 500 * time.Millisecond
	)

	var (
		k8sClient client.Client
		sllbPod   string
	)

	BeforeAll(func() {
		config, err := clientcmd.BuildConfigFromFlags("", clientcmd.RecommendedHomeFile)
		Expect(err).NotTo(HaveOccurred())

		scheme := runtime.NewScheme()
		Expect(meridio2v1alpha1.AddToScheme(scheme)).To(Succeed())
		Expect(corev1.AddToScheme(scheme)).To(Succeed())

		k8sClient, err = client.New(config, client.Options{Scheme: scheme})
		Expect(err).NotTo(HaveOccurred())
	})

	// Resolved per spec rather than once in BeforeAll: other Serial ipv4
	// suites sharing this namespace can restart the LB container, and a
	// cached Pod name would go stale if the Pod itself were ever recreated.
	BeforeEach(func() {
		sllbPod = e2eutils.GetPodName(namespace, fmt.Sprintf("gateway.networking.k8s.io/gateway-name=%s", gatewayName))
		Expect(sllbPod).NotTo(BeEmpty(), "an LB Pod should exist for gateway %s", gatewayName)
		Expect(e2eutils.IsPodReady(namespace, sllbPod)).To(BeTrue(),
			"LB Pod %s should be Ready before churning DistributionGroups", sllbPod)
	})

	// Any DG left over from a failed run must not pollute a later run's
	// baseline measurements.
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

	It("releases NFQLB shm segments and policy routes for every "+
		"DistributionGroup created and deleted under a new name", func() {
		baselineShmCount := shmSegmentCount(sllbPod, namespace)
		baselineRuleCount := policyRuleCount(sllbPod, namespace)
		testStartTime := time.Now()
		GinkgoWriter.Printf("baseline: %d shm segments, %d policy rules\n", baselineShmCount, baselineRuleCount)

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
						MatchLabels: map[string]string{"app": targetApp},
					},
					Maglev: &meridio2v1alpha1.MaglevConfig{MaxEndpoints: maxEndpoints},
					ParentRefs: []meridio2v1alpha1.ParentReference{
						{Name: gatewayName},
					},
				},
			}
			Expect(k8sClient.Create(context.Background(), dg)).To(Succeed())

			// The NFQLB instance (shm segment) is created by AddInstance on the
			// first reconcile.
			Eventually(func() bool {
				return shmSegmentExists(sllbPod, namespace, name)
			}).WithTimeout(stepTimeout).WithPolling(stepPolling).Should(BeTrue(),
				"nfqlb init should have created a shm segment for %s", name)

			// Then wait for its targets to actually be programmed. This step is
			// load-bearing, not just a settle: the bug under test only manifests
			// when DeleteInstance has active targets to deactivate. Deleting as
			// soon as the segment appears is a race — if the delete lands before
			// reconcileTargets runs, there are no targets, no deactivate call,
			// and the test would pass against buggy code (false negative).
			// A programmed target means its policy route exists, so the rule
			// count must have risen above baseline.
			Eventually(func() int {
				return policyRuleCount(sllbPod, namespace)
			}).WithTimeout(stepTimeout).WithPolling(stepPolling).Should(BeNumerically(">", baselineRuleCount),
				"%s must have at least one target programmed (policy route created) "+
					"before deletion, otherwise DeleteInstance has no targets to "+
					"deactivate and the ordering bug this test guards is never exercised", name)

			By(fmt.Sprintf("deleting %s", name))
			Expect(k8sClient.Delete(context.Background(), dg)).To(Succeed())

			Eventually(func() bool {
				err := k8sClient.Get(context.Background(),
					types.NamespacedName{Name: name, Namespace: namespace},
					&meridio2v1alpha1.DistributionGroup{})
				return err != nil
			}).WithTimeout(stepTimeout).WithPolling(stepPolling).Should(BeTrue(),
				"%s should be gone from the API", name)

			Eventually(func() bool {
				return !shmSegmentExists(sllbPod, namespace, name)
			}).WithTimeout(stepTimeout).WithPolling(stepPolling).Should(BeTrue(),
				"shm segment for %s should be unlinked after its DG is deleted", name)

			// Checked per round rather than only in aggregate so a leak is
			// attributed to the round that caused it, and so the test fails
			// within seconds instead of after all 30 rounds.
			Eventually(func() int {
				return policyRuleCount(sllbPod, namespace)
			}).WithTimeout(stepTimeout).WithPolling(stepPolling).Should(Equal(baselineRuleCount),
				"policy rules must return to baseline after %s is deleted — leftover "+
					"rules mean DeleteTarget's route cleanup was skipped (the shm "+
					"segment was unlinked before deactivate, so deactivate failed "+
					"with FAILED mapSharedData and returned before deleting routes)", name)
		}

		finalShmCount := shmSegmentCount(sllbPod, namespace)
		finalRuleCount := policyRuleCount(sllbPod, namespace)
		GinkgoWriter.Printf("final: %d shm segments, %d policy rules\n", finalShmCount, finalRuleCount)

		Expect(finalShmCount).To(Equal(baselineShmCount),
			"shm segment count must return to baseline after %d create/delete rounds "+
				"with a unique DG name each round — growth means orphaned NFQLB instances", churnRounds)
		Expect(finalRuleCount).To(Equal(baselineRuleCount),
			"policy rule count must return to baseline after %d create/delete rounds", churnRounds)

		// A correctly-ordered DeleteInstance has no reason to fail target
		// deactivation against a segment it has not unlinked yet, so any
		// error-level line here is a real defect. The actual lines are
		// asserted (not just a count) so a failure is diagnosable from the
		// test output alone.
		errorLines := lbErrorLogLines(sllbPod, namespace, testStartTime)
		Expect(errorLines).To(BeEmpty(),
			"the loadbalancer controller must not log error-level lines while "+
				"DistributionGroups are created and deleted under normal conditions")
	})

	It("still serves traffic after sustained DG churn (the suite's own dg-m1 untouched throughout)", func() {
		Eventually(func() error { return e2eutils.Ping(vip) }).
			WithTimeout(stepTimeout).WithPolling(stepPolling).Should(Succeed())
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

// policyRuleCount returns the number of policy routing rules in the
// loadbalancer container's network namespace, counting both address families.
// NFQLB creates one rule per target IP (getRule picks the family from the IP),
// so an IPv6 or dual-stack target's rules only show up in `ip -6 rule`.
// Counting both keeps this helper correct if the suite is ever pointed at a
// dual-stack topology.
func policyRuleCount(podName, namespace string) int {
	cmd := exec.Command("kubectl", "exec", "-n", namespace, podName,
		"-c", "loadbalancer", "--", "sh", "-c",
		"{ ip rule list; ip -6 rule list; } | wc -l")
	out, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred())
	count, err := strconv.Atoi(strings.TrimSpace(out))
	Expect(err).NotTo(HaveOccurred())
	return count
}

// lbErrorLogLines returns the error-level log lines emitted by the
// loadbalancer container since the given time. The controller logs structured
// JSON with a "level" field; matching on that rather than on specific messages
// means this catches any failure in the create/delete path, not only the one
// known today. Returns the lines themselves so a failed assertion shows what
// actually went wrong.
func lbErrorLogLines(podName, namespace string, since time.Time) []string {
	cmd := exec.Command("kubectl", "logs", "-n", namespace, podName,
		"-c", "loadbalancer", "--since-time="+since.UTC().Format(time.RFC3339))
	out, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred())

	var lines []string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, `"level":"error"`) {
			lines = append(lines, line)
		}
	}
	return lines
}
