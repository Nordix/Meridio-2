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
	"bytes"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	ctrlzap "sigs.k8s.io/controller-runtime/pkg/log/zap"

	meridio2v1alpha1 "github.com/nordix/meridio-2/api/v1alpha1"
)

var _ = Describe("l34RouteFlow any-port conversion", func() {
	newFlow := func(sports, dports []string) *l34RouteFlow {
		return newL34RouteFlow("test-flow", &meridio2v1alpha1.L34Route{
			Spec: meridio2v1alpha1.L34RouteSpec{
				SourcePorts:      sports,
				DestinationPorts: dports,
			},
		})
	}

	It("converts the \"any\" spelling to the explicit full range", func() {
		// nfqlb's port parser only understands numeric ranges; the literal "any"
		// must never reach it. It must be converted to "0-65535" so anyPortRange
		// recognizes it (and omits the flag) and nfqlb never sees "any".
		f := newFlow([]string{"any"}, []string{"any"})
		Expect(f.GetSourcePortRanges()).To(Equal([]string{"0-65535"}))
		Expect(f.GetDestinationPortRanges()).To(Equal([]string{"0-65535"}))
	})

	It("collapses a set containing a full-range entry to a single element", func() {
		// Any entry covering all ports means the whole set covers all ports, so
		// the other entries are redundant and the set collapses to one element.
		f := newFlow([]string{"80", "any", "8080-8090"}, nil)
		Expect(f.GetSourcePortRanges()).To(Equal([]string{"0-65535"}))
	})

	It("collapses an explicit \"0-65535\" set to a single element", func() {
		f := newFlow([]string{"80", "0-65535"}, nil)
		Expect(f.GetSourcePortRanges()).To(Equal([]string{"0-65535"}))
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

var _ = Describe("flow adapters as log values", func() {
	// The adapters have only unexported fields, so without MarshalLog zap's
	// reflection encoder would log them as {}. Log through the same zap-backed
	// logger the binaries use and assert the content actually shows up.
	logged := func(flow any) string {
		var buf bytes.Buffer
		ctrlzap.New(ctrlzap.WriteTo(&buf), ctrlzap.UseDevMode(false)).Info("msg", "flow", flow)
		return buf.String()
	}

	It("logs the l34RouteFlow content, not {}", func() {
		out := logged(newL34RouteFlow("flow-a", &meridio2v1alpha1.L34Route{
			Spec: meridio2v1alpha1.L34RouteSpec{
				SourceCIDRs:      []string{"10.0.0.0/24"},
				DestinationPorts: []string{"any"},
				Priority:         5,
			},
		}))
		Expect(out).NotTo(ContainSubstring(`"flow":{}`))
		Expect(out).To(ContainSubstring(`"name":"flow-a"`))
		Expect(out).To(ContainSubstring(`"srcs":["10.0.0.0/24"]`))
		// The ports nfqlb receives, i.e. after the any -> explicit conversion.
		Expect(out).To(ContainSubstring(`"dports":["0-65535"]`))
		Expect(out).To(ContainSubstring(`"priority":5`))
	})

	It("logs the name of a nameOnlyFlow, not {}", func() {
		out := logged(&nameOnlyFlow{name: "flow-b"})
		Expect(out).NotTo(ContainSubstring(`"flow":{}`))
		Expect(out).To(ContainSubstring(`"name":"flow-b"`))
	})
})
