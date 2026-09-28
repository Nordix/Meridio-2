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

package loadbalancer

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	meridio2v1alpha1 "github.com/nordix/meridio-2/api/v1alpha1"
)

var _ = Describe("l34RouteFlow port normalization", func() {
	newFlow := func(sports, dports []string) *l34RouteFlow {
		return newL34RouteFlow("test-flow", &meridio2v1alpha1.L34Route{
			Spec: meridio2v1alpha1.L34RouteSpec{
				SourcePorts:      sports,
				DestinationPorts: dports,
			},
		})
	}

	It("normalizes the \"any\" spelling to the explicit full range", func() {
		// nfqlb's port parser only understands numeric ranges; the literal "any"
		// must never reach it. It must be converted to "0-65535" so anyPortRange
		// recognizes it (and omits the flag) and nfqlb never sees "any".
		f := newFlow([]string{"any"}, []string{"any"})
		Expect(f.GetSourcePortRanges()).To(Equal([]string{"0-65535"}))
		Expect(f.GetDestinationPortRanges()).To(Equal([]string{"0-65535"}))
	})

	It("normalizes \"any\" only, leaving other entries unchanged", func() {
		f := newFlow([]string{"80", "any", "8080-8090"}, nil)
		Expect(f.GetSourcePortRanges()).To(Equal([]string{"80", "0-65535", "8080-8090"}))
	})

	It("passes numeric ports and ranges through unchanged", func() {
		f := newFlow([]string{"80", "443", "8080-8090"}, []string{"5000"})
		Expect(f.GetSourcePortRanges()).To(Equal([]string{"80", "443", "8080-8090"}))
		Expect(f.GetDestinationPortRanges()).To(Equal([]string{"5000"}))
	})

	It("preserves nil (no ports specified)", func() {
		f := newFlow(nil, nil)
		Expect(f.GetSourcePortRanges()).To(BeNil())
		Expect(f.GetDestinationPortRanges()).To(BeNil())
	})
})
