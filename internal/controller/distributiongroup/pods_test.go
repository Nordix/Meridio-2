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
	"testing"

	netdefv1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const testIP = "192.168.100.10"

func TestIsPodReady_Ready(t *testing.T) {
	pod := &corev1.Pod{
		Status: corev1.PodStatus{
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionTrue},
			},
		},
	}
	if !isPodReady(pod) {
		t.Error("Expected Pod to be ready")
	}
}

func TestIsPodReady_NotReady(t *testing.T) {
	pod := &corev1.Pod{
		Status: corev1.PodStatus{
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionFalse},
			},
		},
	}
	if isPodReady(pod) {
		t.Error("Expected Pod to not be ready")
	}
}

func TestIsPodReady_NoCondition(t *testing.T) {
	pod := &corev1.Pod{
		Status: corev1.PodStatus{
			Conditions: []corev1.PodCondition{},
		},
	}
	if isPodReady(pod) {
		t.Error("Expected Pod without Ready condition to not be ready")
	}
}

func TestIsPodReady_BeingDeleted(t *testing.T) {
	now := metav1.Now()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			DeletionTimestamp: &now,
		},
		Status: corev1.PodStatus{
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionTrue},
			},
		},
	}
	if isPodReady(pod) {
		t.Error("Expected Pod being deleted to not be ready")
	}
}

func TestScrapeNADIP_ValidAnnotation(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				netdefv1.NetworkStatusAnnot: `[
					{"name":"default","interface":"eth0","ips":["10.244.0.5"],"default":true},
					{"name":"net1","interface":"net1","ips":["` + testIP + `"]}
				]`,
			},
		},
	}

	ip := scrapeNADIP(pod, "192.168.100.0/24")
	if ip != testIP {
		t.Errorf("Expected %s, got %q", testIP, ip)
	}
}

func TestScrapeNADIP_SkipPrimaryInterface(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				netdefv1.NetworkStatusAnnot: `[
					{"name":"default","interface":"eth0","ips":["192.168.100.5"],"default":true},
					{"name":"net1","interface":"net1","ips":["` + testIP + `"]}
				]`,
			},
		},
	}

	ip := scrapeNADIP(pod, "192.168.100.0/24")
	if ip != testIP {
		t.Errorf("Expected secondary IP %s, got %q (should skip primary)", testIP, ip)
	}
}

func TestScrapeNADIP_IPv6(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				netdefv1.NetworkStatusAnnot: `[
					{"name":"default","interface":"eth0","ips":["10.244.0.5"],"default":true},
					{"name":"net1","interface":"net1","ips":["2001:db8::10"]}
				]`,
			},
		},
	}

	ip := scrapeNADIP(pod, "2001:db8::/32")
	if ip != "2001:db8::10" {
		t.Errorf("Expected 2001:db8::10, got %q", ip)
	}
}

func TestScrapeNADIP_MissingAnnotation(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{},
		},
	}

	ip := scrapeNADIP(pod, "192.168.100.0/24")
	if ip != "" {
		t.Errorf("Expected empty string, got %q", ip)
	}
}

func TestScrapeNADIP_InvalidJSON(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				netdefv1.NetworkStatusAnnot: `invalid json`,
			},
		},
	}

	ip := scrapeNADIP(pod, "192.168.100.0/24")
	if ip != "" {
		t.Errorf("Expected empty string for invalid JSON, got %q", ip)
	}
}

func TestScrapeNADIP_NoMatchingIP(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				netdefv1.NetworkStatusAnnot: `[
					{"name":"default","interface":"eth0","ips":["10.244.0.5"],"default":true},
					{"name":"net1","interface":"net1","ips":["192.168.200.10"]}
				]`,
			},
		},
	}

	ip := scrapeNADIP(pod, "192.168.100.0/24")
	if ip != "" {
		t.Errorf("Expected empty string (no match), got %q", ip)
	}
}

func TestScrapeNADIP_InvalidCIDR(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				netdefv1.NetworkStatusAnnot: `[
					{"name":"net1","interface":"net1","ips":["192.168.100.10"]}
				]`,
			},
		},
	}

	ip := scrapeNADIP(pod, "not-a-cidr")
	if ip != "" {
		t.Errorf("Expected empty string for invalid CIDR, got %q", ip)
	}
}

func TestScrapeNADIP_MultipleIPs(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				netdefv1.NetworkStatusAnnot: `[
					{"name":"net1","interface":"net1","ips":["192.168.100.10","192.168.100.11"]}
				]`,
			},
		},
	}

	ip := scrapeNADIP(pod, "192.168.100.0/24")
	if ip != "192.168.100.10" {
		t.Errorf("Expected first matching IP 192.168.100.10, got %q", ip)
	}
}

func TestDefaultIPScraper_NAD(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				netdefv1.NetworkStatusAnnot: `[
					{"name":"net1","interface":"net1","ips":["` + testIP + `"]}
				]`,
			},
		},
	}

	ip := defaultIPScraper(pod, "192.168.100.0/24", "NAD")
	if ip != testIP {
		t.Errorf("Expected %s, got %q", testIP, ip)
	}
}

func TestDefaultIPScraper_UnknownType(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				netdefv1.NetworkStatusAnnot: `[
					{"name":"net1","interface":"net1","ips":["192.168.100.10"]}
				]`,
			},
		},
	}

	ip := defaultIPScraper(pod, "192.168.100.0/24", "UNKNOWN")
	if ip != "" {
		t.Errorf("Expected empty string for unknown attachment type, got %q", ip)
	}
}
