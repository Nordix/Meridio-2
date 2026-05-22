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

var _ = Describe("parseFlows", func() {
	It("should parse valid JSON flow list", func() {
		input := `[{
			"Name": "flow-1",
			"user_ref": "svc-a",
			"matches_count": 5,
			"srcs": ["10.0.0.0/8"],
			"dests": ["20.0.0.1/32"],
			"sports": ["1024-65535"],
			"dports": ["80"],
			"protocols": ["TCP"],
			"priority": 100,
			"match": ["0x06/0xff+9"]
		}]`
		flows, err := parseFlows(input)
		Expect(err).ToNot(HaveOccurred())
		Expect(flows).To(HaveLen(1))
		Expect(flows[0].GetName()).To(Equal("flow-1"))
		Expect(flows[0].ServerName).To(Equal("svc-a"))
		Expect(flows[0].GetDestinationCIDRs()).To(ConsistOf("20.0.0.1/32"))
		Expect(flows[0].GetSourceCIDRs()).To(ConsistOf("10.0.0.0/8"))
		Expect(flows[0].GetProtocols()).To(ConsistOf("TCP"))
		Expect(flows[0].GetPriority()).To(Equal(int32(100)))
		Expect(flows[0].GetDestinationPortRanges()).To(ConsistOf("80"))
		Expect(flows[0].GetSourcePortRanges()).To(ConsistOf("1024-65535"))
		Expect(flows[0].GetByteMatches()).To(ConsistOf("0x06/0xff+9"))
	})

	It("should parse empty list", func() {
		flows, err := parseFlows("[]")
		Expect(err).ToNot(HaveOccurred())
		Expect(flows).To(BeEmpty())
	})

	It("should parse multiple flows", func() {
		input := `[{"Name":"f1"},{"Name":"f2"}]`
		flows, err := parseFlows(input)
		Expect(err).ToNot(HaveOccurred())
		Expect(flows).To(HaveLen(2))
		Expect(flows[0].GetName()).To(Equal("f1"))
		Expect(flows[1].GetName()).To(Equal("f2"))
	})

	It("should return error for invalid JSON", func() {
		_, err := parseFlows("not json")
		Expect(err).To(HaveOccurred())
	})
})

var _ = Describe("nfqlbInstanceConfig", func() {
	It("should calculate M as maxTargets * 100", func() {
		cfg := &nfqlbInstanceConfig{maxTargets: 32}
		Expect(cfg.getM()).To(Equal(3200))
	})

	It("should use default maxTargets", func() {
		cfg := newNFQLBInstanceConfig()
		Expect(cfg.maxTargets).To(Equal(defaultMaxTargets))
		Expect(cfg.getM()).To(Equal(defaultMaxTargets * maglevMMultiplier))
	})
})

var _ = Describe("Options", func() {
	It("should apply WithQueue", func() {
		cfg := newNFQLBConfig()
		WithQueue("1:4")(cfg)
		Expect(cfg.queue).To(Equal("1:4"))
	})

	It("should apply WithQLength", func() {
		cfg := newNFQLBConfig()
		WithQLength(2048)(cfg)
		Expect(cfg.qlength).To(Equal(uint(2048)))
	})

	It("should apply WithStartingOffset", func() {
		cfg := newNFQLBConfig()
		WithStartingOffset(10000)(cfg)
		Expect(cfg.startingOffset).To(Equal(10000))
	})

	It("should apply WithNFQLBPath", func() {
		cfg := newNFQLBConfig()
		WithNFQLBPath("/usr/bin/nfqlb")(cfg)
		Expect(cfg.nfqlbPath).To(Equal("/usr/bin/nfqlb"))
	})

	It("should apply WithMaxTargets", func() {
		cfg := newNFQLBInstanceConfig()
		WithMaxTargets(64)(cfg)
		Expect(cfg.maxTargets).To(Equal(64))
	})
})

var _ = Describe("New", func() {
	It("should create with defaults", func() {
		lb, err := New()
		Expect(err).ToNot(HaveOccurred())
		Expect(lb).ToNot(BeNil())
		Expect(lb.queue).To(Equal(defaultQueue))
		Expect(lb.qlength).To(Equal(uint(defaultQLength)))
		Expect(lb.startingOffset).To(Equal(defaultStartingOffset))
		Expect(lb.instances).To(BeEmpty())
	})

	It("should apply options", func() {
		lb, err := New(WithQueue("2:5"), WithQLength(512), WithStartingOffset(8000))
		Expect(err).ToNot(HaveOccurred())
		Expect(lb.queue).To(Equal("2:5"))
		Expect(lb.qlength).To(Equal(uint(512)))
		Expect(lb.startingOffset).To(Equal(8000))
	})

	It("should reject invalid queue format", func() {
		_, err := New(WithQueue("bad"))
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("invalid queue"))
	})
})
