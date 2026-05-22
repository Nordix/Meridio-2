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

var _ = Describe("getOffset", func() {
	It("should return startingOffset when no instances exist", func() {
		offset, err := getOffset(5000, map[string]*Instance{}, 32)
		Expect(err).ToNot(HaveOccurred())
		Expect(offset).To(Equal(5000))
	})

	It("should skip past existing instance", func() {
		instances := map[string]*Instance{
			"svc-a": {
				offset:              5000,
				nfqlbInstanceConfig: &nfqlbInstanceConfig{maxTargets: 32},
			},
		}
		offset, err := getOffset(5000, instances, 32)
		Expect(err).ToNot(HaveOccurred())
		Expect(offset).To(Equal(5032))
	})

	It("should find gap between instances", func() {
		instances := map[string]*Instance{
			"svc-a": {
				offset:              5000,
				nfqlbInstanceConfig: &nfqlbInstanceConfig{maxTargets: 10},
			},
			"svc-b": {
				offset:              5050,
				nfqlbInstanceConfig: &nfqlbInstanceConfig{maxTargets: 10},
			},
		}
		// Gap at 5010-5049 (40 slots), requesting 32 — should fit
		offset, err := getOffset(5000, instances, 32)
		Expect(err).ToNot(HaveOccurred())
		Expect(offset).To(Equal(5010))
	})

	It("should skip gap too small and use next available", func() {
		instances := map[string]*Instance{
			"svc-a": {
				offset:              5000,
				nfqlbInstanceConfig: &nfqlbInstanceConfig{maxTargets: 10},
			},
			"svc-b": {
				offset:              5015,
				nfqlbInstanceConfig: &nfqlbInstanceConfig{maxTargets: 10},
			},
		}
		// Gap at 5010-5014 (5 slots), requesting 32 — doesn't fit, goes after svc-b
		offset, err := getOffset(5000, instances, 32)
		Expect(err).ToNot(HaveOccurred())
		Expect(offset).To(Equal(5025))
	})

	It("should handle multiple non-overlapping instances", func() {
		instances := map[string]*Instance{
			"svc-a": {
				offset:              5000,
				nfqlbInstanceConfig: &nfqlbInstanceConfig{maxTargets: 100},
			},
			"svc-b": {
				offset:              5100,
				nfqlbInstanceConfig: &nfqlbInstanceConfig{maxTargets: 100},
			},
			"svc-c": {
				offset:              5200,
				nfqlbInstanceConfig: &nfqlbInstanceConfig{maxTargets: 100},
			},
		}
		offset, err := getOffset(5000, instances, 100)
		Expect(err).ToNot(HaveOccurred())
		Expect(offset).To(Equal(5300))
	})

	It("should use custom starting offset", func() {
		offset, err := getOffset(10000, map[string]*Instance{}, 32)
		Expect(err).ToNot(HaveOccurred())
		Expect(offset).To(Equal(10000))
	})
})
