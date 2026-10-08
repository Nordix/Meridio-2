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

package nfqlb

import (
	"errors"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/vishvananda/netlink"
)

var _ = Describe("PolicyRouteCounts", func() {
	var origRuleList func(int) ([]netlink.Rule, error)

	BeforeEach(func() {
		origRuleList = ruleListFunc
	})
	AfterEach(func() {
		ruleListFunc = origRuleList
	})

	const startingOffset = 5002

	It("counts LB-owned rules per family and ignores non-LB rules", func() {
		ruleListFunc = func(int) ([]netlink.Rule, error) {
			return []netlink.Rule{
				{Family: netlink.FAMILY_V4, Mark: 5002}, // LB v4
				{Family: netlink.FAMILY_V4, Mark: 5003}, // LB v4
				{Family: netlink.FAMILY_V6, Mark: 5004}, // LB v6
				{Family: netlink.FAMILY_V4, Mark: 100},  // below offset: ignored
				{Family: netlink.FAMILY_V4, Mark: 0},    // no mark: ignored
			}, nil
		}
		got, err := PolicyRouteCounts(startingOffset)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(map[string]int{ipFamilyV4: 2, ipFamilyV6: 1}))
	})

	It("returns zero for both families when no LB rules exist", func() {
		ruleListFunc = func(int) ([]netlink.Rule, error) {
			return []netlink.Rule{{Family: netlink.FAMILY_V4, Mark: 50}}, nil
		}
		got, err := PolicyRouteCounts(startingOffset)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(map[string]int{ipFamilyV4: 0, ipFamilyV6: 0}))
	})

	It("includes a rule exactly at startingOffset (>= boundary)", func() {
		ruleListFunc = func(int) ([]netlink.Rule, error) {
			return []netlink.Rule{{Family: netlink.FAMILY_V6, Mark: startingOffset}}, nil
		}
		got, err := PolicyRouteCounts(startingOffset)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(map[string]int{ipFamilyV4: 0, ipFamilyV6: 1}))
	})

	It("propagates list errors", func() {
		ruleListFunc = func(int) ([]netlink.Rule, error) {
			return nil, errors.New("rule list failed")
		}
		_, err := PolicyRouteCounts(startingOffset)
		Expect(err).To(HaveOccurred())
	})
})
