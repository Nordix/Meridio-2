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

package v1alpha1

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	meridio2v1alpha1 "github.com/nordix/meridio-2/api/v1alpha1"
)

func boolPtr(b bool) *bool { return &b }

func uint16Ptr(v uint16) *uint16 { return &v }

var _ = Describe("GatewayRouter Webhook", func() {
	var (
		obj       *meridio2v1alpha1.GatewayRouter
		validator GatewayRouterCustomValidator
	)

	BeforeEach(func() {
		obj = &meridio2v1alpha1.GatewayRouter{}
		obj.Name = "test-router"
		obj.Spec.Address = "169.254.100.1"
		obj.Spec.Interface = "ext0"
		validator = GatewayRouterCustomValidator{}
	})

	Context("When validating BGP holdTime", func() {
		It("Should accept a valid holdTime", func() {
			obj.Spec.Protocol = meridio2v1alpha1.RoutingProtocolBGP
			obj.Spec.BGP = meridio2v1alpha1.BgpSpec{
				RemoteASN: 64512,
				LocalASN:  64513,
				HoldTime:  "90s",
			}
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).NotTo(HaveOccurred())
		})

		It("Should accept empty holdTime", func() {
			obj.Spec.Protocol = meridio2v1alpha1.RoutingProtocolBGP
			obj.Spec.BGP = meridio2v1alpha1.BgpSpec{
				RemoteASN: 64512,
				LocalASN:  64513,
			}
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).NotTo(HaveOccurred())
		})

		It("Should reject invalid holdTime", func() {
			obj.Spec.Protocol = meridio2v1alpha1.RoutingProtocolBGP
			obj.Spec.BGP = meridio2v1alpha1.BgpSpec{
				RemoteASN: 64512,
				LocalASN:  64513,
				HoldTime:  "notaduration",
			}
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("holdTime"))
		})
	})

	Context("When validating BGP BFD timers", func() {
		It("Should accept valid BFD timers", func() {
			obj.Spec.Protocol = meridio2v1alpha1.RoutingProtocolBGP
			obj.Spec.BGP = meridio2v1alpha1.BgpSpec{
				RemoteASN: 64512,
				LocalASN:  64513,
				BFD: &meridio2v1alpha1.BfdSpec{
					Switch: boolPtr(true),
					MinTx:  "300ms",
					MinRx:  "300ms",
				},
			}
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).NotTo(HaveOccurred())
		})

		It("Should reject invalid BFD minTx", func() {
			obj.Spec.Protocol = meridio2v1alpha1.RoutingProtocolBGP
			obj.Spec.BGP = meridio2v1alpha1.BgpSpec{
				RemoteASN: 64512,
				LocalASN:  64513,
				BFD: &meridio2v1alpha1.BfdSpec{
					Switch: boolPtr(true),
					MinTx:  "bad",
				},
			}
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("minTx"))
		})

		It("Should reject invalid BFD minRx", func() {
			obj.Spec.Protocol = meridio2v1alpha1.RoutingProtocolBGP
			obj.Spec.BGP = meridio2v1alpha1.BgpSpec{
				RemoteASN: 64512,
				LocalASN:  64513,
				BFD: &meridio2v1alpha1.BfdSpec{
					Switch: boolPtr(true),
					MinRx:  "xyz",
				},
			}
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("minRx"))
		})
	})

	Context("When validating Static BFD timers", func() {
		It("Should accept valid static BFD timers", func() {
			obj.Spec.Protocol = meridio2v1alpha1.RoutingProtocolStatic
			obj.Spec.Static = &meridio2v1alpha1.StaticSpec{
				BFD: &meridio2v1alpha1.BfdSpec{
					Switch:     boolPtr(true),
					MinTx:      "200ms",
					MinRx:      "200ms",
					Multiplier: uint16Ptr(3),
				},
			}
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).NotTo(HaveOccurred())
		})

		It("Should reject invalid static BFD minTx", func() {
			obj.Spec.Protocol = meridio2v1alpha1.RoutingProtocolStatic
			obj.Spec.Static = &meridio2v1alpha1.StaticSpec{
				BFD: &meridio2v1alpha1.BfdSpec{
					Switch: boolPtr(true),
					MinTx:  "invalid",
				},
			}
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("minTx"))
		})

		It("Should reject invalid static BFD minRx", func() {
			obj.Spec.Protocol = meridio2v1alpha1.RoutingProtocolStatic
			obj.Spec.Static = &meridio2v1alpha1.StaticSpec{
				BFD: &meridio2v1alpha1.BfdSpec{
					Switch: boolPtr(true),
					MinRx:  "invalid",
				},
			}
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("minRx"))
		})

		It("Should accept static without BFD", func() {
			obj.Spec.Protocol = meridio2v1alpha1.RoutingProtocolStatic
			obj.Spec.Static = &meridio2v1alpha1.StaticSpec{}
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Context("When validating updates", func() {
		It("Should validate the new object on update", func() {
			oldObj := &meridio2v1alpha1.GatewayRouter{}
			oldObj.Spec.Protocol = meridio2v1alpha1.RoutingProtocolBGP
			oldObj.Spec.BGP = meridio2v1alpha1.BgpSpec{
				RemoteASN: 64512,
				LocalASN:  64513,
				HoldTime:  "90s",
			}

			obj.Spec.Protocol = meridio2v1alpha1.RoutingProtocolBGP
			obj.Spec.BGP = meridio2v1alpha1.BgpSpec{
				RemoteASN: 64512,
				LocalASN:  64513,
				HoldTime:  "garbage",
			}
			_, err := validator.ValidateUpdate(ctx, oldObj, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("holdTime"))
		})
	})

	Context("When validating deletion", func() {
		It("Should always allow deletion", func() {
			_, err := validator.ValidateDelete(ctx, obj)
			Expect(err).NotTo(HaveOccurred())
		})
	})
})
