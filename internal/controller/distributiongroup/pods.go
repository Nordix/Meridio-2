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

package distributiongroup

import (
	"context"
	"encoding/json"
	"net"

	netdefv1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	meridio2v1alpha1 "github.com/nordix/meridio-2/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// endpointConditions derives an endpoint's conditions from its backing Pod,
// mirroring how the Kubernetes EndpointSlice controller projects Pod state:
//
//   - terminating: the Pod has a deletion timestamp
//   - serving:     the Pod's Ready condition is True, regardless of termination
//   - ready:       serving and not terminating
//
// Keeping serving separate from ready is what lets a draining endpoint be
// represented as still able to serve. Collapsing both into a single boolean made
// a terminating Pod indistinguishable from an unhealthy one, so it left the data
// path the instant termination began — before the application could complete a
// graceful close handshake.
//
// Pod phase is deliberately not represented here: listMatchingPods only admits
// Running Pods, so a Pending, Succeeded or Failed Pod has no endpoint at all.
// Absence, rather than a condition, is how those states reach consumers.
func endpointConditions(pod *corev1.Pod) discoveryv1.EndpointConditions {
	serving := isPodReadyConditionTrue(pod)
	terminating := pod.DeletionTimestamp != nil
	ready := serving && !terminating

	return discoveryv1.EndpointConditions{
		Ready:       &ready,
		Serving:     &serving,
		Terminating: &terminating,
	}
}

// isPodReadyConditionTrue reports whether the Pod's Ready condition is True.
// A missing or Unknown condition counts as not ready.
//
// Unlike the endpoint-level conditions above, this ignores termination: a
// terminating Pod whose readiness probe still passes is Pod-ready. Mirrors
// k8s.io/kubernetes/pkg/api/v1/pod.IsPodReady, which cannot be imported here
// because k8s.io/kubernetes is not a dependency.
//
// Note this is the aggregate Pod condition, so any container dragging it down —
// including one unrelated to the served endpoint, such as a restarting
// auxiliary container — makes the endpoint not serving.
func isPodReadyConditionTrue(pod *corev1.Pod) bool {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}

// defaultIPScraper is the production implementation of IP scraping
func defaultIPScraper(pod *corev1.Pod, cidr, attachmentType string) string {
	switch attachmentType {
	case "NAD":
		return scrapeNADIP(pod, cidr)
	default:
		return ""
	}
}

// scrapeNADIP extracts secondary IP from Multus network-status annotation
func scrapeNADIP(pod *corev1.Pod, cidr string) string {
	annotation, ok := pod.Annotations[netdefv1.NetworkStatusAnnot]
	if !ok {
		return ""
	}

	var networkStatus []netdefv1.NetworkStatus
	if err := json.Unmarshal([]byte(annotation), &networkStatus); err != nil {
		return ""
	}

	_, targetNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return ""
	}

	for _, netif := range networkStatus {
		// Skip primary interface - we only want secondary network IPs
		if netif.Default {
			continue
		}
		for _, ipStr := range netif.IPs {
			ip := net.ParseIP(ipStr)
			if ip != nil && targetNet.Contains(ip) {
				return ipStr
			}
		}
	}

	return ""
}

// listMatchingPods returns Pods matching the DistributionGroup selector
func (r *DistributionGroupReconciler) listMatchingPods(ctx context.Context, dg *meridio2v1alpha1.DistributionGroup) ([]corev1.Pod, error) {
	if dg.Spec.Selector == nil {
		return nil, nil
	}

	selector, err := metav1.LabelSelectorAsSelector(dg.Spec.Selector)
	if err != nil {
		return nil, err
	}

	var podList corev1.PodList
	listOpts := []client.ListOption{
		client.InNamespace(dg.Namespace),
		client.MatchingLabelsSelector{Selector: selector},
	}

	if err := r.List(ctx, &podList, listOpts...); err != nil {
		return nil, err
	}

	// Filter to Running pods only (secondary IP extraction happens during EndpointSlice creation)
	var pods []corev1.Pod
	for _, pod := range podList.Items {
		if pod.Status.Phase == corev1.PodRunning {
			pods = append(pods, pod)
		}
	}

	return pods, nil
}
