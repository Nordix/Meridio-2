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
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("validateName", func() {
	It("should accept valid names", func() {
		Expect(validateName("my-instance")).To(Succeed())
		Expect(validateName("tshm-distgroup-1")).To(Succeed())
		Expect(validateName("a")).To(Succeed())
		Expect(validateName("abc_123-def.ghi")).To(Succeed())
	})

	It("should reject empty name", func() {
		Expect(validateName("")).To(MatchError(ContainSubstring("must not be empty")))
	})

	It("should reject names with spaces", func() {
		Expect(validateName("my instance")).To(MatchError(ContainSubstring("invalid character")))
	})

	It("should reject names with shell metacharacters", func() {
		Expect(validateName("name;rm -rf /")).To(MatchError(ContainSubstring("invalid character")))
		Expect(validateName("name$(cmd)")).To(MatchError(ContainSubstring("invalid character")))
		Expect(validateName("name`cmd`")).To(MatchError(ContainSubstring("invalid character")))
		Expect(validateName("name|pipe")).To(MatchError(ContainSubstring("invalid character")))
		Expect(validateName("name&bg")).To(MatchError(ContainSubstring("invalid character")))
	})

	It("should reject names starting with dash", func() {
		Expect(validateName("-badname")).To(MatchError(ContainSubstring("must not start with")))
	})
})

var _ = Describe("validateCIDRs", func() {
	It("should accept valid IPv4 CIDRs", func() {
		Expect(validateCIDRs([]string{"192.168.1.0/24", "10.0.0.0/8"})).To(Succeed())
	})

	It("should accept valid IPv6 CIDRs", func() {
		Expect(validateCIDRs([]string{"2001:db8::/32", "fe80::/10"})).To(Succeed())
	})

	It("should accept host CIDRs", func() {
		Expect(validateCIDRs([]string{"20.0.0.1/32", "2000::1/128"})).To(Succeed())
	})

	It("should accept nil or empty slice", func() {
		Expect(validateCIDRs(nil)).To(Succeed())
		Expect(validateCIDRs([]string{})).To(Succeed())
	})

	It("should reject invalid CIDRs", func() {
		Expect(validateCIDRs([]string{"not-a-cidr"})).To(MatchError(ContainSubstring("invalid CIDR")))
		Expect(validateCIDRs([]string{"192.168.1.1"})).To(MatchError(ContainSubstring("invalid CIDR")))
	})

	It("should reject CIDRs with shell metacharacters", func() {
		Expect(validateCIDRs([]string{"192.168.1.0/24;rm -rf /"})).To(MatchError(ContainSubstring("invalid CIDR")))
	})
})

var _ = Describe("validatePortRanges", func() {
	It("should accept single ports", func() {
		Expect(validatePortRanges([]string{"80", "443", "8080"})).To(Succeed())
	})

	It("should accept port ranges", func() {
		Expect(validatePortRanges([]string{"1024-65535", "80-90"})).To(Succeed())
	})

	It("should accept 'any' port range", func() {
		Expect(validatePortRanges([]string{"0-65535"})).To(Succeed())
	})

	It("should accept nil or empty slice", func() {
		Expect(validatePortRanges(nil)).To(Succeed())
		Expect(validatePortRanges([]string{})).To(Succeed())
	})

	It("should reject invalid port strings", func() {
		Expect(validatePortRanges([]string{"abc"})).To(MatchError(ContainSubstring("invalid port")))
		Expect(validatePortRanges([]string{"80;rm"})).To(MatchError(ContainSubstring("invalid port")))
	})

	It("should reject out-of-range ports", func() {
		Expect(validatePortRanges([]string{"99999"})).To(MatchError(ContainSubstring("out of range")))
		Expect(validatePortRanges([]string{"-1"})).To(MatchError(ContainSubstring("invalid port")))
	})

	It("should reject reversed ranges", func() {
		Expect(validatePortRanges([]string{"443-80"})).To(MatchError(ContainSubstring("start must be <= end")))
	})
})

var _ = Describe("validateProtocols", func() {
	It("should accept valid protocols", func() {
		Expect(validateProtocols([]string{"tcp", "udp", "sctp"})).To(Succeed())
		Expect(validateProtocols([]string{"TCP", "UDP", "SCTP"})).To(Succeed())
	})

	It("should accept nil or empty slice", func() {
		Expect(validateProtocols(nil)).To(Succeed())
		Expect(validateProtocols([]string{})).To(Succeed())
	})

	It("should reject invalid protocols", func() {
		Expect(validateProtocols([]string{"icmp"})).To(MatchError(ContainSubstring("unsupported protocol")))
		Expect(validateProtocols([]string{"tcp;rm"})).To(MatchError(ContainSubstring("unsupported protocol")))
	})
})
