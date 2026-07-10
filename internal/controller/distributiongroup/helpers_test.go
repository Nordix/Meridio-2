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

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestNormalizeCIDR(t *testing.T) {
	tests := []struct {
		name     string
		cidr     string
		expected string
		wantErr  bool
	}{
		{"IPv4 canonical", "192.168.1.0/24", "192.168.1.0/24", false},
		{"IPv4 non-canonical", "192.168.1.5/24", "192.168.1.0/24", false},
		{"IPv4 /32", "10.0.0.1/32", "10.0.0.1/32", false},
		{"IPv6 canonical", "2001:db8::/32", "2001:db8::/32", false},
		{"IPv6 expanded", "2001:db8:0:0::/32", "2001:db8::/32", false},
		{"IPv6 non-canonical", "2001:db8::5/32", "2001:db8::/32", false},
		{"Invalid", "not-a-cidr", "", true},
		{"Missing prefix", "192.168.1.0", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := normalizeCIDR(tt.cidr)
			if tt.wantErr {
				if err == nil {
					t.Errorf("normalizeCIDR(%q) expected error, got nil", tt.cidr)
				}
			} else {
				if err != nil {
					t.Errorf("normalizeCIDR(%q) unexpected error: %v", tt.cidr, err)
				}
				if result != tt.expected {
					t.Errorf("normalizeCIDR(%q) = %q, want %q", tt.cidr, result, tt.expected)
				}
			}
		})
	}
}

func TestSortPodsByCreationTime(t *testing.T) {
	now := metav1.Now()
	earlier := metav1.NewTime(now.Add(-60000000000))
	later := metav1.NewTime(now.Add(60000000000))

	pods := []corev1.Pod{
		{ObjectMeta: metav1.ObjectMeta{Name: "pod-c", Namespace: "ns", CreationTimestamp: now}},
		{ObjectMeta: metav1.ObjectMeta{Name: "pod-a", Namespace: "ns", CreationTimestamp: earlier}},
		{ObjectMeta: metav1.ObjectMeta{Name: "pod-b", Namespace: "ns", CreationTimestamp: later}},
	}

	sortPodsByCreationTime(pods)

	if pods[0].Name != "pod-a" || pods[1].Name != "pod-c" || pods[2].Name != "pod-b" {
		t.Errorf("Sort order incorrect: got %v, %v, %v", pods[0].Name, pods[1].Name, pods[2].Name)
	}
}

func TestSortPodsByCreationTime_Tiebreak(t *testing.T) {
	now := metav1.Now()

	pods := []corev1.Pod{
		{ObjectMeta: metav1.ObjectMeta{Name: "pod-z", Namespace: "ns-b", CreationTimestamp: now}},
		{ObjectMeta: metav1.ObjectMeta{Name: "pod-a", Namespace: "ns-a", CreationTimestamp: now}},
		{ObjectMeta: metav1.ObjectMeta{Name: "pod-b", Namespace: "ns-a", CreationTimestamp: now}},
	}

	sortPodsByCreationTime(pods)

	if pods[0].Namespace+"/"+pods[0].Name != "ns-a/pod-a" {
		t.Errorf("First pod should be ns-a/pod-a, got %s/%s", pods[0].Namespace, pods[0].Name)
	}
	if pods[1].Namespace+"/"+pods[1].Name != "ns-a/pod-b" {
		t.Errorf("Second pod should be ns-a/pod-b, got %s/%s", pods[1].Namespace, pods[1].Name)
	}
	if pods[2].Namespace+"/"+pods[2].Name != "ns-b/pod-z" {
		t.Errorf("Third pod should be ns-b/pod-z, got %s/%s", pods[2].Namespace, pods[2].Name)
	}
}

func TestSliceBaseName_Short(t *testing.T) {
	gw := client.ObjectKey{Name: "my-gateway", Namespace: "meridio-2"}
	name := sliceBaseName("test-dg", gw)

	// Should be "test-dg-<16 hex chars>"
	if len(name) != len("test-dg-")+16 {
		t.Errorf("Expected 'test-dg-' + 16 hex chars, got %q (len=%d)", name, len(name))
	}
	if name[:8] != "test-dg-" {
		t.Errorf("Expected prefix 'test-dg-', got %q", name[:8])
	}
}

func TestSliceBaseName_Deterministic(t *testing.T) {
	gw := client.ObjectKey{Name: "my-gateway", Namespace: "meridio-2"}
	name1 := sliceBaseName("test-dg", gw)
	name2 := sliceBaseName("test-dg", gw)
	if name1 != name2 {
		t.Errorf("sliceBaseName should be deterministic: %q != %q", name1, name2)
	}
}

func TestSliceBaseName_LongNameTruncatedWithHash(t *testing.T) {
	// Create a DG name that exceeds 240 chars when combined with hash
	longDG := make([]byte, 250)
	for i := range longDG {
		longDG[i] = byte('a' + i%26)
	}
	gw := client.ObjectKey{Name: "my-gateway", Namespace: "some-namespace"}
	name := sliceBaseName(string(longDG), gw)

	if len(name) > 240 {
		t.Errorf("Expected name <= 240 chars, got %d", len(name))
	}
	// Should end with a 16-char hex hash
	suffix := name[len(name)-16:]
	for _, c := range suffix {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			t.Errorf("Expected hex suffix, got %q", suffix)
			break
		}
	}
	// Truncated prefix should match the beginning of longDG
	// Name format: <truncated-dg>-<hash>, so prefix = name[:len(name)-17]
	prefix := name[:len(name)-17] // strip "-" + 16-char hash
	if prefix != string(longDG[:len(prefix)]) {
		t.Errorf("Truncated prefix should match start of DG name.\nGot:      %q\nExpected: %q", prefix, string(longDG[:len(prefix)]))
	}
}

func TestSliceBaseName_DifferentGatewaysDifferentNames(t *testing.T) {
	gw1 := client.ObjectKey{Name: "gateway-a", Namespace: "ns-1"}
	gw2 := client.ObjectKey{Name: "gateway-b", Namespace: "ns-1"}
	gw3 := client.ObjectKey{Name: "gateway-a", Namespace: "ns-2"}

	name1 := sliceBaseName("dg", gw1)
	name2 := sliceBaseName("dg", gw2)
	name3 := sliceBaseName("dg", gw3)

	if name1 == name2 {
		t.Error("Different gateway names should produce different slice base names")
	}
	if name1 == name3 {
		t.Error("Different gateway namespaces should produce different slice base names")
	}
}

func TestTruncateLabelValue(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"short", "test-dg", "test-dg"},
		{"exactly 63", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{"over 63", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := truncateLabelValue(tt.input)
			if result != tt.expected {
				t.Errorf("truncateLabelValue(%q) = %q (len=%d), want %q (len=%d)",
					tt.input, result, len(result), tt.expected, len(tt.expected))
			}
			if len(result) > 63 {
				t.Errorf("result exceeds 63 chars: len=%d", len(result))
			}
		})
	}
}
