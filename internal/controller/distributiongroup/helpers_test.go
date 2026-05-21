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
)

func TestEncodeCIDRForLabel(t *testing.T) {
	tests := []struct {
		cidr     string
		expected string
	}{
		{"192.168.100.0/24", "192.168.100.0-24"},
		{"2001:db8::/32", "2001_db8__-32"},
		{"10.0.0.1/32", "10.0.0.1-32"},
		{"2001:db8::1/128", "2001_db8__1-128"},
	}

	for _, tt := range tests {
		result := encodeCIDRForLabel(tt.cidr)
		if result != tt.expected {
			t.Errorf("encodeCIDRForLabel(%q) = %q, want %q", tt.cidr, result, tt.expected)
		}
	}
}

func TestDecodeCIDRFromLabel(t *testing.T) {
	tests := []struct {
		encoded  string
		expected string
	}{
		{"192.168.100.0-24", "192.168.100.0/24"},
		{"2001_db8__-32", "2001:db8::/32"},
		{"2001_db8_1_2__-64", "2001:db8:1:2::/64"},
		{"invalid", "invalid"},
	}

	for _, tt := range tests {
		result := decodeCIDRFromLabel(tt.encoded)
		if result != tt.expected {
			t.Errorf("decodeCIDRFromLabel(%q) = %q, want %q", tt.encoded, result, tt.expected)
		}
	}
}

func TestEncodeDecode_RoundTrip(t *testing.T) {
	cidrs := []string{
		"192.168.1.0/24",
		"10.0.0.0/8",
		"2001:db8::/32",
		"2001:db8:1:2::/64",
	}

	for _, cidr := range cidrs {
		encoded := encodeCIDRForLabel(cidr)
		decoded := decodeCIDRFromLabel(encoded)
		if decoded != cidr {
			t.Errorf("Round-trip failed: %q → %q → %q", cidr, encoded, decoded)
		}
	}
}

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

func TestHashCIDR_Deterministic(t *testing.T) {
	cidr := "192.168.1.0/24"
	hash1 := hashCIDR(cidr)
	hash2 := hashCIDR(cidr)

	if hash1 != hash2 {
		t.Errorf("hashCIDR not deterministic: %q vs %q", hash1, hash2)
	}
	if len(hash1) == 0 {
		t.Error("hashCIDR returned empty string")
	}
}

func TestHashCIDR_Uniqueness(t *testing.T) {
	cidrs := []string{
		"192.168.1.0/24",
		"192.168.2.0/24",
		"10.0.0.0/8",
		"2001:db8::/32",
	}

	hashes := make(map[string]string)
	for _, cidr := range cidrs {
		hash := hashCIDR(cidr)
		if existing, exists := hashes[hash]; exists {
			t.Errorf("Hash collision: %q and %q both hash to %q", cidr, existing, hash)
		}
		hashes[hash] = cidr
	}
}

func TestPtr(t *testing.T) {
	t.Run("int", func(t *testing.T) {
		val := 42
		p := ptr(val)
		if p == nil || *p != val {
			t.Errorf("ptr(42) failed")
		}
	})

	t.Run("string", func(t *testing.T) {
		val := "test"
		p := ptr(val)
		if p == nil || *p != val {
			t.Errorf("ptr(\"test\") failed")
		}
	})

	t.Run("bool", func(t *testing.T) {
		val := true
		p := ptr(val)
		if p == nil || *p != val {
			t.Errorf("ptr(true) failed")
		}
	})
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
