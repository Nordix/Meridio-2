/*
Copyright (c) 2025 OpenInfra Foundation Europe. All rights reserved.

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

package v1alpha1

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	meridio2v1alpha1 "github.com/nordix/meridio-2/api/v1alpha1"
	// TODO (user): Add any additional imports if needed
)

var _ = Describe("L34Route Webhook", func() {
	var (
		obj       *meridio2v1alpha1.L34Route
		validator L34RouteCustomValidator
	)

	BeforeEach(func() {
		obj = &meridio2v1alpha1.L34Route{}
		validator = L34RouteCustomValidator{}
	})

	Context("When validating protocols", func() {
		It("Should accept unique protocols", func() {
			obj.Spec.Protocols = []meridio2v1alpha1.TransportProtocol{
				meridio2v1alpha1.TCP,
				meridio2v1alpha1.UDP,
			}
			obj.Spec.DestinationCIDRs = []string{"192.168.1.1/32"}
			obj.Spec.Priority = 1
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).NotTo(HaveOccurred())
		})

		It("Should reject duplicate protocols", func() {
			obj.Spec.Protocols = []meridio2v1alpha1.TransportProtocol{
				meridio2v1alpha1.TCP,
				meridio2v1alpha1.TCP,
			}
			obj.Spec.DestinationCIDRs = []string{"192.168.1.1/32"}
			obj.Spec.Priority = 1
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("duplicated protocols"))
		})
	})

	Context("When validating source CIDRs", func() {
		It("Should accept non-overlapping IPv4 CIDRs", func() {
			obj.Spec.SourceCIDRs = []string{"192.168.1.0/24", "10.0.0.0/24"}
			obj.Spec.DestinationCIDRs = []string{"192.168.1.1/32"}
			obj.Spec.Protocols = []meridio2v1alpha1.TransportProtocol{meridio2v1alpha1.TCP}
			obj.Spec.Priority = 1
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).NotTo(HaveOccurred())
		})

		It("Should accept non-overlapping IPv6 CIDRs", func() {
			obj.Spec.SourceCIDRs = []string{"2001:db8::/32", "2001:db9::/32"}
			obj.Spec.DestinationCIDRs = []string{"192.168.1.1/32"}
			obj.Spec.Protocols = []meridio2v1alpha1.TransportProtocol{meridio2v1alpha1.TCP}
			obj.Spec.Priority = 1
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).NotTo(HaveOccurred())
		})

		It("Should accept mixed IPv4 and IPv6 CIDRs", func() {
			obj.Spec.SourceCIDRs = []string{"192.168.1.0/24", "2001:db8::/32"}
			obj.Spec.DestinationCIDRs = []string{"192.168.1.1/32"}
			obj.Spec.Protocols = []meridio2v1alpha1.TransportProtocol{meridio2v1alpha1.TCP}
			obj.Spec.Priority = 1
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).NotTo(HaveOccurred())
		})

		It("Should reject overlapping IPv4 CIDRs", func() {
			obj.Spec.SourceCIDRs = []string{"192.168.1.0/24", "192.168.1.0/25"}
			obj.Spec.DestinationCIDRs = []string{"192.168.1.1/32"}
			obj.Spec.Protocols = []meridio2v1alpha1.TransportProtocol{meridio2v1alpha1.TCP}
			obj.Spec.Priority = 1
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("overlapping CIDR"))
		})

		It("Should reject overlapping IPv6 CIDRs", func() {
			obj.Spec.SourceCIDRs = []string{"2001:db8::/32", "2001:db8::/48"}
			obj.Spec.DestinationCIDRs = []string{"192.168.1.1/32"}
			obj.Spec.Protocols = []meridio2v1alpha1.TransportProtocol{meridio2v1alpha1.TCP}
			obj.Spec.Priority = 1
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("overlapping CIDR"))
		})

		It("Should reject identical IPv4 CIDRs", func() {
			obj.Spec.SourceCIDRs = []string{"192.168.1.0/24", "192.168.1.0/24"}
			obj.Spec.DestinationCIDRs = []string{"192.168.1.1/32"}
			obj.Spec.Protocols = []meridio2v1alpha1.TransportProtocol{meridio2v1alpha1.TCP}
			obj.Spec.Priority = 1
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("overlapping CIDR"))
		})

		It("Should reject identical IPv6 CIDRs", func() {
			obj.Spec.SourceCIDRs = []string{"2001:db8::/32", "2001:db8::/32"}
			obj.Spec.DestinationCIDRs = []string{"192.168.1.1/32"}
			obj.Spec.Protocols = []meridio2v1alpha1.TransportProtocol{meridio2v1alpha1.TCP}
			obj.Spec.Priority = 1
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("overlapping CIDR"))
		})
	})

	Context("When validating destination CIDRs", func() {
		It("Should accept non-overlapping IPv4 /32 CIDRs", func() {
			obj.Spec.DestinationCIDRs = []string{"192.168.1.1/32", "192.168.1.2/32"}
			obj.Spec.Protocols = []meridio2v1alpha1.TransportProtocol{meridio2v1alpha1.TCP}
			obj.Spec.Priority = 1
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).NotTo(HaveOccurred())
		})

		It("Should accept non-overlapping IPv6 /128 CIDRs", func() {
			obj.Spec.DestinationCIDRs = []string{"2001:db8::1/128", "2001:db8::2/128"}
			obj.Spec.Protocols = []meridio2v1alpha1.TransportProtocol{meridio2v1alpha1.TCP}
			obj.Spec.Priority = 1
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).NotTo(HaveOccurred())
		})

		It("Should reject identical IPv4 /32 CIDRs", func() {
			obj.Spec.DestinationCIDRs = []string{"192.168.1.1/32", "192.168.1.1/32"}
			obj.Spec.Protocols = []meridio2v1alpha1.TransportProtocol{meridio2v1alpha1.TCP}
			obj.Spec.Priority = 1
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("overlapping CIDR"))
		})

		It("Should reject identical IPv6 /128 CIDRs", func() {
			obj.Spec.DestinationCIDRs = []string{"2001:db8::1/128", "2001:db8::1/128"}
			obj.Spec.Protocols = []meridio2v1alpha1.TransportProtocol{meridio2v1alpha1.TCP}
			obj.Spec.Priority = 1
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("overlapping CIDR"))
		})
	})

	Context("When validating source ports", func() {
		It("Should accept non-overlapping ports", func() {
			obj.Spec.SourcePorts = []string{"80", "443", "8080-8090"}
			obj.Spec.DestinationCIDRs = []string{"192.168.1.1/32"}
			obj.Spec.Protocols = []meridio2v1alpha1.TransportProtocol{meridio2v1alpha1.TCP}
			obj.Spec.Priority = 1
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).NotTo(HaveOccurred())
		})

		It("Should accept 'any' port", func() {
			obj.Spec.SourcePorts = []string{"any"}
			obj.Spec.DestinationCIDRs = []string{"192.168.1.1/32"}
			obj.Spec.Protocols = []meridio2v1alpha1.TransportProtocol{meridio2v1alpha1.TCP}
			obj.Spec.Priority = 1
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).NotTo(HaveOccurred())
		})

		It("Should reject overlapping port ranges", func() {
			obj.Spec.SourcePorts = []string{"8080-8090", "8085-8095"}
			obj.Spec.DestinationCIDRs = []string{"192.168.1.1/32"}
			obj.Spec.Protocols = []meridio2v1alpha1.TransportProtocol{meridio2v1alpha1.TCP}
			obj.Spec.Priority = 1
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("overlapping ports"))
		})

		It("Should reject port overlapping with range", func() {
			obj.Spec.SourcePorts = []string{"8080-8090", "8085"}
			obj.Spec.DestinationCIDRs = []string{"192.168.1.1/32"}
			obj.Spec.Protocols = []meridio2v1alpha1.TransportProtocol{meridio2v1alpha1.TCP}
			obj.Spec.Priority = 1
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("overlapping ports"))
		})

		It("Should reject duplicate ports", func() {
			obj.Spec.SourcePorts = []string{"80", "80"}
			obj.Spec.DestinationCIDRs = []string{"192.168.1.1/32"}
			obj.Spec.Protocols = []meridio2v1alpha1.TransportProtocol{meridio2v1alpha1.TCP}
			obj.Spec.Priority = 1
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("overlapping ports"))
		})
	})

	Context("When validating destination ports", func() {
		It("Should accept non-overlapping ports", func() {
			obj.Spec.DestinationPorts = []string{"80", "443", "8080-8090"}
			obj.Spec.DestinationCIDRs = []string{"192.168.1.1/32"}
			obj.Spec.Protocols = []meridio2v1alpha1.TransportProtocol{meridio2v1alpha1.TCP}
			obj.Spec.Priority = 1
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).NotTo(HaveOccurred())
		})

		It("Should reject overlapping port ranges", func() {
			obj.Spec.DestinationPorts = []string{"8080-8090", "8085-8095"}
			obj.Spec.DestinationCIDRs = []string{"192.168.1.1/32"}
			obj.Spec.Protocols = []meridio2v1alpha1.TransportProtocol{meridio2v1alpha1.TCP}
			obj.Spec.Priority = 1
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("overlapping ports"))
		})
	})

	Context("When validating complete L34Route", func() {
		It("Should accept valid L34Route", func() {
			obj.Spec.DestinationCIDRs = []string{"192.168.1.1/32"}
			obj.Spec.SourceCIDRs = []string{"10.0.0.0/24"}
			obj.Spec.SourcePorts = []string{"any"}
			obj.Spec.DestinationPorts = []string{"80", "443"}
			obj.Spec.Protocols = []meridio2v1alpha1.TransportProtocol{
				meridio2v1alpha1.TCP,
				meridio2v1alpha1.UDP,
			}
			obj.Spec.Priority = 100
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).NotTo(HaveOccurred())
		})

		It("Should accept minimal valid L34Route", func() {
			obj.Spec.DestinationCIDRs = []string{"192.168.1.1/32"}
			obj.Spec.Protocols = []meridio2v1alpha1.TransportProtocol{meridio2v1alpha1.TCP}
			obj.Spec.Priority = 1
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Context("When validating updates", func() {
		It("Should accept valid updates", func() {
			oldObj := &meridio2v1alpha1.L34Route{
				Spec: meridio2v1alpha1.L34RouteSpec{
					DestinationCIDRs: []string{"192.168.1.1/32"},
					Protocols:        []meridio2v1alpha1.TransportProtocol{meridio2v1alpha1.TCP},
					Priority:         1,
				},
			}
			obj.Spec.DestinationCIDRs = []string{"192.168.1.2/32"}
			obj.Spec.Protocols = []meridio2v1alpha1.TransportProtocol{meridio2v1alpha1.UDP}
			obj.Spec.Priority = 2
			_, err := validator.ValidateUpdate(ctx, oldObj, obj)
			Expect(err).NotTo(HaveOccurred())
		})
	})
})
