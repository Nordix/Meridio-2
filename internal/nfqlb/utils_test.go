/*
Copyright (c) 2024-2026 OpenInfra Foundation Europe

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

package nfqlb

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestNfqlb(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "NFQLB Suite")
}

var _ = Describe("getQueue", func() {
	It("should parse single queue number", func() {
		start, end, err := getQueue("2")
		Expect(err).ToNot(HaveOccurred())
		Expect(start).To(Equal(2))
		Expect(end).To(Equal(2))
	})

	It("should parse queue range", func() {
		start, end, err := getQueue("0:3")
		Expect(err).ToNot(HaveOccurred())
		Expect(start).To(Equal(0))
		Expect(end).To(Equal(3))
	})

	It("should reject non-numeric input", func() {
		_, _, err := getQueue("abc")
		Expect(err).To(MatchError(errQueueFormat))
	})

	It("should reject non-numeric range end", func() {
		_, _, err := getQueue("0:abc")
		Expect(err).To(MatchError(errQueueFormat))
	})

	It("should reject triple-colon format", func() {
		_, _, err := getQueue("0:1:2")
		Expect(err).To(MatchError(errQueueFormat))
	})

	It("should reject empty string", func() {
		_, _, err := getQueue("")
		Expect(err).To(MatchError(errQueueFormat))
	})
})

var _ = Describe("anyIPRange", func() {
	It("should return true when all CIDRs are /0", func() {
		Expect(anyIPRange([]string{"0.0.0.0/0", "::/0"})).To(BeTrue())
	})

	It("should return false when any CIDR is not /0", func() {
		Expect(anyIPRange([]string{"0.0.0.0/0", "10.0.0.0/8"})).To(BeFalse())
	})

	It("should return false for specific CIDRs", func() {
		Expect(anyIPRange([]string{"192.168.1.0/24"})).To(BeFalse())
	})

	It("should return false for bare IPs without mask", func() {
		Expect(anyIPRange([]string{"10.0.0.1"})).To(BeFalse())
	})

	It("should return false for invalid mask", func() {
		Expect(anyIPRange([]string{"10.0.0.0/abc"})).To(BeFalse())
	})

	It("should return true for empty slice", func() {
		Expect(anyIPRange([]string{})).To(BeTrue())
	})
})

var _ = Describe("anyPortRange", func() {
	It("should return true when 0-65535 is present", func() {
		Expect(anyPortRange([]string{"80", "0-65535"})).To(BeTrue())
	})

	It("should return false for specific ports", func() {
		Expect(anyPortRange([]string{"80", "443"})).To(BeFalse())
	})

	It("should return false for empty slice", func() {
		Expect(anyPortRange([]string{})).To(BeFalse())
	})
})
