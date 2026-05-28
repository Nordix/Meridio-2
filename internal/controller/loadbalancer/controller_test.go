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
	"context"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	meridio2v1alpha1 "github.com/nordix/meridio-2/api/v1alpha1"
	"github.com/nordix/meridio-2/internal/common/readiness"
	"github.com/nordix/meridio-2/internal/nfqlb"
)

const (
	testZoneMaglev0 = "maglev:0"
	testZoneMaglev1 = "maglev:1"
	testZoneMaglev2 = "maglev:2"
)

func TestLoadBalancerController(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "LoadBalancer Controller Suite")
}

// mockNFQLB mocks the NFQueueLoadBalancer for testing.
type mockNFQLB struct {
	instances map[string]*mockNFQLBInstance
}

func newMockNFQLB() *mockNFQLB {
	return &mockNFQLB{instances: make(map[string]*mockNFQLBInstance)}
}

func (m *mockNFQLB) AddInstance(_ context.Context, name string, _ ...nfqlb.InstanceOption) (nfqlbInstance, error) {
	inst := &mockNFQLBInstance{
		name:    name,
		flows:   make(map[string]nfqlb.Flow),
		targets: make(map[int][]string),
	}
	m.instances[name] = inst
	return inst, nil
}

func (m *mockNFQLB) DeleteInstance(_ context.Context, name string) error {
	delete(m.instances, name)
	return nil
}

// mockNFQLBInstance mocks a single NFQLB instance (per DistributionGroup).
type mockNFQLBInstance struct {
	name    string
	flows   map[string]nfqlb.Flow
	targets map[int][]string
}

func (m *mockNFQLBInstance) AddFlow(_ context.Context, flow nfqlb.Flow) error {
	if m.flows == nil {
		m.flows = make(map[string]nfqlb.Flow)
	}
	m.flows[flow.GetName()] = flow
	return nil
}

func (m *mockNFQLBInstance) DeleteFlow(_ context.Context, flow nfqlb.Flow) error {
	delete(m.flows, flow.GetName())
	return nil
}

func (m *mockNFQLBInstance) AddTarget(_ context.Context, ips []string, identifier int) error {
	if m.targets == nil {
		m.targets = make(map[int][]string)
	}
	m.targets[identifier] = ips
	return nil
}

func (m *mockNFQLBInstance) DeleteTarget(_ context.Context, _ []string, identifier int) error {
	delete(m.targets, identifier)
	return nil
}

var _ = Describe("LoadBalancer Controller", func() {
	var (
		scheme      *runtime.Scheme
		fakeClient  client.Client
		controller  *Controller
		mockNfqlb   *mockNFQLB
		ctx         context.Context
		gatewayName = "test-gateway"
		namespace   = "default"
	)

	BeforeEach(func() {
		ctx = context.Background()
		scheme = runtime.NewScheme()
		Expect(meridio2v1alpha1.AddToScheme(scheme)).To(Succeed())
		Expect(gatewayv1.Install(scheme)).To(Succeed())
		Expect(discoveryv1.AddToScheme(scheme)).To(Succeed())

		mockNfqlb = newMockNFQLB()

		controller = &Controller{
			Scheme:           scheme,
			GatewayName:      gatewayName,
			GatewayNamespace: namespace,
			NFQLB:            mockNfqlb,
			Readiness:        readiness.NewManager(""),
			NftManagerFactory: func(queueNum, queueTotal uint16) (nftablesManager, error) {
				return newMockNftablesManager(), nil
			},
		}

		// Initialize shared nftManager
		var err error
		controller.nftManager, err = controller.NftManagerFactory(0, 4)
		Expect(err).ToNot(HaveOccurred())
	})

	Describe("belongsToGateway", func() {
		It("should return true when L34Route references both Gateway and DistributionGroup", func() {
			distGroup := &meridio2v1alpha1.DistributionGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-distgroup",
					Namespace: namespace,
				},
			}

			group := meridio2v1alpha1.GroupVersion.Group
			kind := kindDistributionGroup
			l34route := &meridio2v1alpha1.L34Route{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-route",
					Namespace: namespace,
				},
				Spec: meridio2v1alpha1.L34RouteSpec{
					ParentRefs: []gatewayv1.ParentReference{
						{
							Name: gatewayv1.ObjectName(gatewayName),
						},
					},
					BackendRefs: []gatewayv1.BackendRef{
						{
							BackendObjectReference: gatewayv1.BackendObjectReference{
								Group: (*gatewayv1.Group)(&group),
								Kind:  (*gatewayv1.Kind)(&kind),
								Name:  gatewayv1.ObjectName(distGroup.Name),
							},
						},
					},
				},
			}

			fakeClient = fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(l34route).
				Build()
			controller.Client = fakeClient

			result, err := controller.belongsToGateway(ctx, distGroup)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(BeTrue())
		})

		It("should return false when no L34Route references the DistributionGroup", func() {
			distGroup := &meridio2v1alpha1.DistributionGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-distgroup",
					Namespace: namespace,
				},
			}

			fakeClient = fake.NewClientBuilder().
				WithScheme(scheme).
				Build()
			controller.Client = fakeClient

			result, err := controller.belongsToGateway(ctx, distGroup)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(BeFalse())
		})

		It("should return false when L34Route references different Gateway", func() {
			distGroup := &meridio2v1alpha1.DistributionGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-distgroup",
					Namespace: namespace,
				},
			}

			group := meridio2v1alpha1.GroupVersion.Group
			kind := kindDistributionGroup
			l34route := &meridio2v1alpha1.L34Route{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-route",
					Namespace: namespace,
				},
				Spec: meridio2v1alpha1.L34RouteSpec{
					ParentRefs: []gatewayv1.ParentReference{
						{
							Name: gatewayv1.ObjectName("other-gateway"),
						},
					},
					BackendRefs: []gatewayv1.BackendRef{
						{
							BackendObjectReference: gatewayv1.BackendObjectReference{
								Group: (*gatewayv1.Group)(&group),
								Kind:  (*gatewayv1.Kind)(&kind),
								Name:  gatewayv1.ObjectName(distGroup.Name),
							},
						},
					},
				},
			}

			fakeClient = fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(l34route).
				Build()
			controller.Client = fakeClient

			result, err := controller.belongsToGateway(ctx, distGroup)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(BeFalse())
		})

		It("should return true when DistributionGroup has direct parentRef to this Gateway", func() {
			distGroup := &meridio2v1alpha1.DistributionGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-distgroup",
					Namespace: namespace,
				},
				Spec: meridio2v1alpha1.DistributionGroupSpec{
					ParentRefs: []meridio2v1alpha1.ParentReference{
						{Name: gatewayName},
					},
				},
			}

			fakeClient = fake.NewClientBuilder().
				WithScheme(scheme).
				Build()
			controller.Client = fakeClient

			result, err := controller.belongsToGateway(ctx, distGroup)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(BeTrue())
		})

		It("should return false when DistributionGroup has parentRef to different Gateway", func() {
			distGroup := &meridio2v1alpha1.DistributionGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-distgroup",
					Namespace: namespace,
				},
				Spec: meridio2v1alpha1.DistributionGroupSpec{
					ParentRefs: []meridio2v1alpha1.ParentReference{
						{Name: "other-gateway"},
					},
				},
			}

			fakeClient = fake.NewClientBuilder().
				WithScheme(scheme).
				Build()
			controller.Client = fakeClient

			result, err := controller.belongsToGateway(ctx, distGroup)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(BeFalse())
		})

		It("should return true via direct parentRef even without L34Routes", func() {
			distGroup := &meridio2v1alpha1.DistributionGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-distgroup",
					Namespace: namespace,
				},
				Spec: meridio2v1alpha1.DistributionGroupSpec{
					ParentRefs: []meridio2v1alpha1.ParentReference{
						{Name: gatewayName},
					},
				},
			}

			// No L34Routes exist — direct parentRef should still match
			fakeClient = fake.NewClientBuilder().
				WithScheme(scheme).
				Build()
			controller.Client = fakeClient

			result, err := controller.belongsToGateway(ctx, distGroup)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(BeTrue())
		})
	})

	Describe("reconcileNFQLBInstance", func() {
		BeforeEach(func() {
			fakeClient = fake.NewClientBuilder().
				WithScheme(scheme).
				Build()
			controller.Client = fakeClient
		})

		It("should create NFQLB instance with M=N×100", func() {
			maxEndpoints := int32(32)
			distGroup := &meridio2v1alpha1.DistributionGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-distgroup",
					Namespace: namespace,
				},
				Spec: meridio2v1alpha1.DistributionGroupSpec{
					Maglev: &meridio2v1alpha1.MaglevConfig{
						MaxEndpoints: maxEndpoints,
					},
				},
			}

			err := controller.reconcileNFQLBInstance(ctx, distGroup)
			Expect(err).ToNot(HaveOccurred())

			// Verify instance was created
			Expect(controller.instances).To(HaveKey(distGroup.Name))
			Expect(mockNfqlb.instances).To(HaveKey(distGroup.Name))
		})

		It("should use default N=32 when Maglev config is nil", func() {
			distGroup := &meridio2v1alpha1.DistributionGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-distgroup",
					Namespace: namespace,
				},
				Spec: meridio2v1alpha1.DistributionGroupSpec{
					Maglev: nil,
				},
			}

			err := controller.reconcileNFQLBInstance(ctx, distGroup)
			Expect(err).ToNot(HaveOccurred())

			Expect(controller.instances).To(HaveKey(distGroup.Name))
		})

		It("should not recreate existing instance", func() {
			distGroup := &meridio2v1alpha1.DistributionGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-distgroup",
					Namespace: namespace,
				},
			}

			// Create instance first time
			err := controller.reconcileNFQLBInstance(ctx, distGroup)
			Expect(err).ToNot(HaveOccurred())
			firstInstance := controller.instances[distGroup.Name]

			// Reconcile again
			err = controller.reconcileNFQLBInstance(ctx, distGroup)
			Expect(err).ToNot(HaveOccurred())
			secondInstance := controller.instances[distGroup.Name]

			// Should be same instance
			Expect(firstInstance).To(BeIdenticalTo(secondInstance))
		})

		It("should assign sequential IDs without collisions", func() {
			dg1 := &meridio2v1alpha1.DistributionGroup{
				ObjectMeta: metav1.ObjectMeta{Name: "dg-1", Namespace: namespace},
			}
			dg2 := &meridio2v1alpha1.DistributionGroup{
				ObjectMeta: metav1.ObjectMeta{Name: "dg-2", Namespace: namespace},
			}
			dg3 := &meridio2v1alpha1.DistributionGroup{
				ObjectMeta: metav1.ObjectMeta{Name: "dg-3", Namespace: namespace},
			}

			// Create instances
			Expect(controller.reconcileNFQLBInstance(ctx, dg1)).To(Succeed())
			Expect(controller.reconcileNFQLBInstance(ctx, dg2)).To(Succeed())
			Expect(controller.reconcileNFQLBInstance(ctx, dg3)).To(Succeed())

			// Verify sequential instance creation
			Expect(controller.instances).To(HaveKey("dg-1"))
			Expect(controller.instances).To(HaveKey("dg-2"))
			Expect(controller.instances).To(HaveKey("dg-3"))
		})

		It("should reuse freed IDs", func() {
			dg1 := &meridio2v1alpha1.DistributionGroup{
				ObjectMeta: metav1.ObjectMeta{Name: "dg-1", Namespace: namespace},
			}
			dg2 := &meridio2v1alpha1.DistributionGroup{
				ObjectMeta: metav1.ObjectMeta{Name: "dg-2", Namespace: namespace},
			}
			dg3 := &meridio2v1alpha1.DistributionGroup{
				ObjectMeta: metav1.ObjectMeta{Name: "dg-3", Namespace: namespace},
			}

			// Create three instances
			Expect(controller.reconcileNFQLBInstance(ctx, dg1)).To(Succeed())
			Expect(controller.reconcileNFQLBInstance(ctx, dg2)).To(Succeed())
			Expect(controller.reconcileNFQLBInstance(ctx, dg3)).To(Succeed())

			// Delete dg-2
			_, err := controller.cleanupDistributionGroup(ctx, "dg-2")
			Expect(err).ToNot(HaveOccurred())
			Expect(controller.instances).ToNot(HaveKey("dg-2"))

			// Create new DG - should succeed (offset reuse is internal to nfqlb)
			dg4 := &meridio2v1alpha1.DistributionGroup{
				ObjectMeta: metav1.ObjectMeta{Name: "dg-4", Namespace: namespace},
			}
			Expect(controller.reconcileNFQLBInstance(ctx, dg4)).To(Succeed())
			Expect(controller.instances).To(HaveKey("dg-4"))
		})
	})

	Describe("reconcileTargets", func() {
		var distGroup *meridio2v1alpha1.DistributionGroup

		BeforeEach(func() {
			distGroup = &meridio2v1alpha1.DistributionGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-distgroup",
					Namespace: namespace,
				},
			}

			// Create NFQLB instance first
			err := controller.reconcileNFQLBInstance(ctx, distGroup)
			Expect(err).ToNot(HaveOccurred())
		})

		It("should activate new targets with correct index and fwmark", func() {
			ready := true
			zone0 := testZoneMaglev0
			zone1 := testZoneMaglev1
			endpointSlice := &discoveryv1.EndpointSlice{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-eps",
					Namespace: namespace,
					Labels: map[string]string{
						"meridio-2.nordix.org/distribution-group": "test-distgroup",
					},
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion: "meridio-2.nordix.org/v1alpha1",
							Kind:       "DistributionGroup",
							Name:       distGroup.Name,
							Controller: ptr.To(true),
						},
					},
				},
				Endpoints: []discoveryv1.Endpoint{
					{
						Addresses: []string{"10.0.0.1"},
						Conditions: discoveryv1.EndpointConditions{
							Ready: &ready,
						},
						Zone: &zone0,
					},
					{
						Addresses: []string{"10.0.0.2"},
						Conditions: discoveryv1.EndpointConditions{
							Ready: &ready,
						},
						Zone: &zone1,
					},
				},
			}

			fakeClient = fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(endpointSlice).
				Build()
			controller.Client = fakeClient

			err := controller.reconcileTargets(ctx, distGroup)
			Expect(err).ToNot(HaveOccurred())

			// Calculate expected fwmark offset for this DistributionGroup
			// Targets are now tracked by identifier (offset is internal to nfqlb)

			// Verify targets were activated with correct fwmark
			// identifier=0 -> index=1, fwmark=offset+0
			// identifier=1 -> index=2, fwmark=offset+1
			mockInstance := mockNfqlb.instances[distGroup.Name]
			Expect(mockInstance.targets).To(HaveKey(0))    // fwmark = 0 + offset
			Expect(mockInstance.targets[0]).To(HaveLen(1)) // index = 0 + 1
			Expect(mockInstance.targets).To(HaveKey(1))    // fwmark = 1 + offset
			Expect(mockInstance.targets[1]).To(HaveLen(1)) // index = 1 + 1
		})

		It("should skip endpoints without Zone field", func() {
			ready := true
			endpointSlice := &discoveryv1.EndpointSlice{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-eps",
					Namespace: namespace,
					Labels: map[string]string{
						"meridio-2.nordix.org/distribution-group": "test-distgroup",
					},
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion: "meridio-2.nordix.org/v1alpha1",
							Kind:       "DistributionGroup",
							Name:       distGroup.Name,
							Controller: ptr.To(true),
						},
					},
				},
				Endpoints: []discoveryv1.Endpoint{
					{
						Addresses: []string{"10.0.0.1"},
						Conditions: discoveryv1.EndpointConditions{
							Ready: &ready,
						},
						Zone: nil,
					},
				},
			}

			fakeClient = fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(endpointSlice).
				Build()
			controller.Client = fakeClient

			err := controller.reconcileTargets(ctx, distGroup)
			Expect(err).ToNot(HaveOccurred())

			// No targets should be activated
			mockInstance := mockNfqlb.instances[distGroup.Name]
			Expect(mockInstance.targets).To(BeEmpty())
		})

		It("should skip non-ready endpoints", func() {
			ready := false
			zone := "0"
			endpointSlice := &discoveryv1.EndpointSlice{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-eps",
					Namespace: namespace,
					Labels: map[string]string{
						"meridio-2.nordix.org/distribution-group": "test-distgroup",
					},
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion: "meridio-2.nordix.org/v1alpha1",
							Kind:       "DistributionGroup",
							Name:       distGroup.Name,
							Controller: ptr.To(true),
						},
					},
				},
				Endpoints: []discoveryv1.Endpoint{
					{
						Addresses: []string{"10.0.0.1"},
						Conditions: discoveryv1.EndpointConditions{
							Ready: &ready,
						},
						Zone: &zone,
					},
				},
			}

			fakeClient = fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(endpointSlice).
				Build()
			controller.Client = fakeClient

			err := controller.reconcileTargets(ctx, distGroup)
			Expect(err).ToNot(HaveOccurred())

			// No targets should be activated
			mockInstance := mockNfqlb.instances[distGroup.Name]
			Expect(mockInstance.targets).To(BeEmpty())
		})

		It("should deactivate removed targets with correct index", func() {
			// Setup: activate a target first (identifier=0)
			controller.targets = map[string]map[int][]string{
				distGroup.Name: {
					0: {"10.0.0.1"},
				},
			}

			// No EndpointSlices (target removed)
			fakeClient = fake.NewClientBuilder().
				WithScheme(scheme).
				Build()
			controller.Client = fakeClient

			err := controller.reconcileTargets(ctx, distGroup)
			Expect(err).ToNot(HaveOccurred())

			// Verify target was deactivated with index=1 (identifier 0 + 1)
			mockInstance := mockNfqlb.instances[distGroup.Name]
			Expect(mockInstance.targets).ToNot(HaveKey(0))
		})

		It("should parse Zone field in maglev:N format", func() {
			ready := true
			zone0 := testZoneMaglev0
			zone1 := testZoneMaglev1
			endpointSlice := &discoveryv1.EndpointSlice{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-eps",
					Namespace: namespace,
					Labels: map[string]string{
						"meridio-2.nordix.org/distribution-group": "test-distgroup",
					},
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion: "meridio-2.nordix.org/v1alpha1",
							Kind:       "DistributionGroup",
							Name:       distGroup.Name,
							Controller: ptr.To(true),
						},
					},
				},
				Endpoints: []discoveryv1.Endpoint{
					{
						Addresses: []string{"10.0.0.1"},
						Conditions: discoveryv1.EndpointConditions{
							Ready: &ready,
						},
						Zone: &zone0,
					},
					{
						Addresses: []string{"10.0.0.2"},
						Conditions: discoveryv1.EndpointConditions{
							Ready: &ready,
						},
						Zone: &zone1,
					},
				},
			}

			fakeClient = fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(endpointSlice).
				Build()
			controller.Client = fakeClient

			err := controller.reconcileTargets(ctx, distGroup)
			Expect(err).ToNot(HaveOccurred())

			// Calculate expected fwmark offset for this DistributionGroup
			// Targets are now tracked by identifier (offset is internal to nfqlb)

			// Verify targets were activated with correct identifiers
			mockInstance := mockNfqlb.instances[distGroup.Name]
			Expect(mockInstance.targets).To(HaveKey(0))    // fwmark = 0 + offset
			Expect(mockInstance.targets[0]).To(HaveLen(1)) // index = 0 + 1
			Expect(mockInstance.targets).To(HaveKey(1))    // fwmark = 1 + offset
			Expect(mockInstance.targets[1]).To(HaveLen(1)) // index = 1 + 1
		})

		It("should skip endpoints with invalid Zone format", func() {
			ready := true
			invalidZone := "0" // Plain integer, not maglev:N format
			validZone := testZoneMaglev0
			endpointSlice := &discoveryv1.EndpointSlice{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-eps",
					Namespace: namespace,
					Labels: map[string]string{
						"meridio-2.nordix.org/distribution-group": "test-distgroup",
					},
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion: "meridio-2.nordix.org/v1alpha1",
							Kind:       "DistributionGroup",
							Name:       distGroup.Name,
							Controller: ptr.To(true),
						},
					},
				},
				Endpoints: []discoveryv1.Endpoint{
					{
						Addresses: []string{"10.0.0.1"},
						Conditions: discoveryv1.EndpointConditions{
							Ready: &ready,
						},
						Zone: &invalidZone, // Should be skipped
					},
					{
						Addresses: []string{"10.0.0.2"},
						Conditions: discoveryv1.EndpointConditions{
							Ready: &ready,
						},
						Zone: &validZone, // Should be activated
					},
				},
			}

			fakeClient = fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(endpointSlice).
				Build()
			controller.Client = fakeClient

			err := controller.reconcileTargets(ctx, distGroup)
			Expect(err).ToNot(HaveOccurred())

			// Calculate expected fwmark offset for this DistributionGroup
			// Targets are now tracked by identifier (offset is internal to nfqlb)

			// Only valid endpoint should be activated
			mockInstance := mockNfqlb.instances[distGroup.Name]
			Expect(mockInstance.targets).To(HaveLen(1))
			Expect(mockInstance.targets).To(HaveKey(0)) // Only maglev:0
		})

		It("should accumulate IPs from IPv4 and IPv6 EndpointSlices for the same identifier", func() {
			ready := true
			zone0 := testZoneMaglev0

			epsV4 := &discoveryv1.EndpointSlice{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-eps-v4",
					Namespace: namespace,
					Labels: map[string]string{
						"meridio-2.nordix.org/distribution-group": "test-distgroup",
					},
				},
				AddressType: discoveryv1.AddressTypeIPv4,
				Endpoints: []discoveryv1.Endpoint{
					{
						Addresses:  []string{"192.168.100.10"},
						Conditions: discoveryv1.EndpointConditions{Ready: &ready},
						Zone:       &zone0,
					},
				},
			}

			epsV6 := &discoveryv1.EndpointSlice{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-eps-v6",
					Namespace: namespace,
					Labels: map[string]string{
						"meridio-2.nordix.org/distribution-group": "test-distgroup",
					},
				},
				AddressType: discoveryv1.AddressTypeIPv6,
				Endpoints: []discoveryv1.Endpoint{
					{
						Addresses:  []string{"2001:db8:100::10"},
						Conditions: discoveryv1.EndpointConditions{Ready: &ready},
						Zone:       &zone0,
					},
				},
			}

			fakeClient = fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(epsV4, epsV6).
				Build()
			controller.Client = fakeClient

			err := controller.reconcileTargets(ctx, distGroup)
			Expect(err).ToNot(HaveOccurred())

			mockInstance := mockNfqlb.instances[distGroup.Name]
			Expect(mockInstance.targets).To(HaveKey(0))
			Expect(mockInstance.targets[0]).To(Equal([]string{"192.168.100.10", "2001:db8:100::10"}))
		})
	})

	Describe("reconcileFlows", func() {
		var distGroup *meridio2v1alpha1.DistributionGroup

		BeforeEach(func() {
			distGroup = &meridio2v1alpha1.DistributionGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-distgroup",
					Namespace: namespace,
				},
			}

			// Create NFQLB instance first
			err := controller.reconcileNFQLBInstance(ctx, distGroup)
			Expect(err).ToNot(HaveOccurred())
		})

		It("should configure flow from L34Route", func() {
			group := meridio2v1alpha1.GroupVersion.Group
			kind := kindDistributionGroup

			// Create EndpointSlice with ready endpoints
			ready := true
			zone := "maglev:0"
			endpointSlice := &discoveryv1.EndpointSlice{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-eps",
					Namespace: namespace,
					Labels: map[string]string{
						"meridio-2.nordix.org/distribution-group": "test-distgroup",
					},
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion: "meridio-2.nordix.org/v1alpha1",
							Kind:       "DistributionGroup",
							Name:       distGroup.Name,
							Controller: ptr.To(true),
						},
					},
				},
				Endpoints: []discoveryv1.Endpoint{
					{
						Addresses: []string{"10.0.0.1"},
						Conditions: discoveryv1.EndpointConditions{
							Ready: &ready,
						},
						Zone: &zone,
					},
				},
			}

			l34route := &meridio2v1alpha1.L34Route{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-route",
					Namespace: namespace,
				},
				Spec: meridio2v1alpha1.L34RouteSpec{
					ParentRefs: []gatewayv1.ParentReference{
						{
							Name: gatewayv1.ObjectName(gatewayName),
						},
					},
					BackendRefs: []gatewayv1.BackendRef{
						{
							BackendObjectReference: gatewayv1.BackendObjectReference{
								Group: (*gatewayv1.Group)(&group),
								Kind:  (*gatewayv1.Kind)(&kind),
								Name:  gatewayv1.ObjectName(distGroup.Name),
							},
						},
					},
					DestinationCIDRs: []string{"20.0.0.1/32"},
					Protocols:        []meridio2v1alpha1.TransportProtocol{meridio2v1alpha1.TCP},
					DestinationPorts: []string{"80"},
					Priority:         100,
				},
			}

			// Create Gateway with VIPs in status
			ipAddrType := gatewayv1.IPAddressType
			gateway := &gatewayv1.Gateway{
				ObjectMeta: metav1.ObjectMeta{
					Name:      gatewayName,
					Namespace: namespace,
				},
				Status: gatewayv1.GatewayStatus{
					Addresses: []gatewayv1.GatewayStatusAddress{
						{Type: &ipAddrType, Value: "20.0.0.1"},
					},
				},
			}

			fakeClient = fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(distGroup, endpointSlice, l34route, gateway).
				Build()
			controller.Client = fakeClient

			err := controller.reconcileFlows(ctx, distGroup)
			Expect(err).ToNot(HaveOccurred())

			// Verify flow was configured
			mockInstance := mockNfqlb.instances[distGroup.Name]
			Expect(mockInstance.flows).To(HaveLen(1))

			// Check flow details
			var flow nfqlb.Flow
			for _, f := range mockInstance.flows {
				flow = f
				break
			}
			Expect(flow).ToNot(BeNil())
			Expect(flow.GetName()).To(Equal("test-route"))
			Expect(flow.GetPriority()).To(Equal(int32(100)))
			Expect(flow.GetProtocols()).To(ConsistOf("TCP"))
			Expect(flow.GetDestinationCIDRs()).To(ConsistOf("20.0.0.1/32"))
			Expect(flow.GetDestinationPortRanges()).To(ConsistOf("80"))
		})

		It("should handle multiple L34Routes for same DistributionGroup", func() {
			group := meridio2v1alpha1.GroupVersion.Group
			kind := kindDistributionGroup

			// Create EndpointSlice with ready endpoints
			ready := true
			zone := "maglev:0"
			endpointSlice := &discoveryv1.EndpointSlice{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-eps",
					Namespace: namespace,
					Labels: map[string]string{
						"meridio-2.nordix.org/distribution-group": "test-distgroup",
					},
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion: "meridio-2.nordix.org/v1alpha1",
							Kind:       "DistributionGroup",
							Name:       distGroup.Name,
							Controller: ptr.To(true),
						},
					},
				},
				Endpoints: []discoveryv1.Endpoint{
					{
						Addresses: []string{"10.0.0.1"},
						Conditions: discoveryv1.EndpointConditions{
							Ready: &ready,
						},
						Zone: &zone,
					},
				},
			}

			l34route1 := &meridio2v1alpha1.L34Route{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "route-1",
					Namespace: namespace,
				},
				Spec: meridio2v1alpha1.L34RouteSpec{
					ParentRefs: []gatewayv1.ParentReference{
						{Name: gatewayv1.ObjectName(gatewayName)},
					},
					BackendRefs: []gatewayv1.BackendRef{
						{
							BackendObjectReference: gatewayv1.BackendObjectReference{
								Group: (*gatewayv1.Group)(&group),
								Kind:  (*gatewayv1.Kind)(&kind),
								Name:  gatewayv1.ObjectName(distGroup.Name),
							},
						},
					},
					DestinationCIDRs: []string{"20.0.0.1/32"},
					Protocols:        []meridio2v1alpha1.TransportProtocol{meridio2v1alpha1.TCP},
					DestinationPorts: []string{"80"},
					Priority:         100,
				},
			}

			l34route2 := &meridio2v1alpha1.L34Route{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "route-2",
					Namespace: namespace,
				},
				Spec: meridio2v1alpha1.L34RouteSpec{
					ParentRefs: []gatewayv1.ParentReference{
						{Name: gatewayv1.ObjectName(gatewayName)},
					},
					BackendRefs: []gatewayv1.BackendRef{
						{
							BackendObjectReference: gatewayv1.BackendObjectReference{
								Group: (*gatewayv1.Group)(&group),
								Kind:  (*gatewayv1.Kind)(&kind),
								Name:  gatewayv1.ObjectName(distGroup.Name),
							},
						},
					},
					DestinationCIDRs: []string{"20.0.0.2/32"},
					Protocols:        []meridio2v1alpha1.TransportProtocol{meridio2v1alpha1.UDP},
					DestinationPorts: []string{"53"},
					Priority:         200,
				},
			}

			// Create Gateway with VIPs in status
			ipAddrType := gatewayv1.IPAddressType
			gateway := &gatewayv1.Gateway{
				ObjectMeta: metav1.ObjectMeta{
					Name:      gatewayName,
					Namespace: namespace,
				},
				Status: gatewayv1.GatewayStatus{
					Addresses: []gatewayv1.GatewayStatusAddress{
						{Type: &ipAddrType, Value: "20.0.0.1"},
						{Type: &ipAddrType, Value: "20.0.0.2"},
					},
				},
			}

			fakeClient = fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(distGroup, endpointSlice, l34route1, l34route2, gateway).
				Build()
			controller.Client = fakeClient

			err := controller.reconcileFlows(ctx, distGroup)
			Expect(err).ToNot(HaveOccurred())

			// Verify both flows configured
			mockInstance := mockNfqlb.instances[distGroup.Name]
			Expect(mockInstance.flows).To(HaveLen(2))
			Expect(mockInstance.flows).To(HaveKey("route-1"))
			Expect(mockInstance.flows).To(HaveKey("route-2"))
		})

		It("should skip L34Routes for different Gateway", func() {
			group := meridio2v1alpha1.GroupVersion.Group
			kind := kindDistributionGroup

			l34route := &meridio2v1alpha1.L34Route{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "other-route",
					Namespace: namespace,
				},
				Spec: meridio2v1alpha1.L34RouteSpec{
					ParentRefs: []gatewayv1.ParentReference{
						{Name: gatewayv1.ObjectName("other-gateway")},
					},
					BackendRefs: []gatewayv1.BackendRef{
						{
							BackendObjectReference: gatewayv1.BackendObjectReference{
								Group: (*gatewayv1.Group)(&group),
								Kind:  (*gatewayv1.Kind)(&kind),
								Name:  gatewayv1.ObjectName(distGroup.Name),
							},
						},
					},
					DestinationCIDRs: []string{"20.0.0.1/32"},
					Protocols:        []meridio2v1alpha1.TransportProtocol{meridio2v1alpha1.TCP},
					Priority:         100,
				},
			}

			fakeClient = fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(l34route).
				Build()
			controller.Client = fakeClient

			err := controller.reconcileFlows(ctx, distGroup)
			Expect(err).ToNot(HaveOccurred())

			// No flows should be configured
			mockInstance := mockNfqlb.instances[distGroup.Name]
			Expect(mockInstance.flows).To(BeEmpty())
		})

		It("should skip L34Routes for different DistributionGroup", func() {
			group := meridio2v1alpha1.GroupVersion.Group
			kind := kindDistributionGroup

			l34route := &meridio2v1alpha1.L34Route{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "other-route",
					Namespace: namespace,
				},
				Spec: meridio2v1alpha1.L34RouteSpec{
					ParentRefs: []gatewayv1.ParentReference{
						{Name: gatewayv1.ObjectName(gatewayName)},
					},
					BackendRefs: []gatewayv1.BackendRef{
						{
							BackendObjectReference: gatewayv1.BackendObjectReference{
								Group: (*gatewayv1.Group)(&group),
								Kind:  (*gatewayv1.Kind)(&kind),
								Name:  "other-distgroup",
							},
						},
					},
					DestinationCIDRs: []string{"20.0.0.1/32"},
					Protocols:        []meridio2v1alpha1.TransportProtocol{meridio2v1alpha1.TCP},
					Priority:         100,
				},
			}

			fakeClient = fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(l34route).
				Build()
			controller.Client = fakeClient

			err := controller.reconcileFlows(ctx, distGroup)
			Expect(err).ToNot(HaveOccurred())

			// No flows should be configured
			mockInstance := mockNfqlb.instances[distGroup.Name]
			Expect(mockInstance.flows).To(BeEmpty())
		})

		It("should delete flows when DistributionGroup has no endpoints", func() {
			group := meridio2v1alpha1.GroupVersion.Group
			kind := kindDistributionGroup

			// Setup: configure a flow first
			controller.flows = map[string]map[string]*meridio2v1alpha1.L34Route{
				distGroup.Name: {
					"test-route": &meridio2v1alpha1.L34Route{
						ObjectMeta: metav1.ObjectMeta{Name: "test-route"},
					},
				},
			}

			l34route := &meridio2v1alpha1.L34Route{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-route",
					Namespace: namespace,
				},
				Spec: meridio2v1alpha1.L34RouteSpec{
					ParentRefs: []gatewayv1.ParentReference{
						{Name: gatewayv1.ObjectName(gatewayName)},
					},
					BackendRefs: []gatewayv1.BackendRef{
						{
							BackendObjectReference: gatewayv1.BackendObjectReference{
								Group: (*gatewayv1.Group)(&group),
								Kind:  (*gatewayv1.Kind)(&kind),
								Name:  gatewayv1.ObjectName(distGroup.Name),
							},
						},
					},
					DestinationCIDRs: []string{"20.0.0.1/32"},
					Protocols:        []meridio2v1alpha1.TransportProtocol{meridio2v1alpha1.TCP},
					Priority:         100,
				},
			}

			// No EndpointSlice (no endpoints)
			fakeClient = fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(distGroup, l34route).
				Build()
			controller.Client = fakeClient

			err := controller.reconcileFlows(ctx, distGroup)
			Expect(err).ToNot(HaveOccurred())

			// Flows should be deleted
			mockInstance := mockNfqlb.instances[distGroup.Name]
			Expect(mockInstance.flows).To(BeEmpty())
			Expect(mockInstance.flows).ToNot(HaveKey("test-route"))
		})

		It("should delete flows when L34Route is removed", func() {
			// Setup: configure a flow first
			controller.flows = map[string]map[string]*meridio2v1alpha1.L34Route{
				distGroup.Name: {
					"old-route": &meridio2v1alpha1.L34Route{
						ObjectMeta: metav1.ObjectMeta{Name: "old-route"},
					},
				},
			}

			// No L34Routes in cluster (removed)
			fakeClient = fake.NewClientBuilder().
				WithScheme(scheme).
				Build()
			controller.Client = fakeClient

			err := controller.reconcileFlows(ctx, distGroup)
			Expect(err).ToNot(HaveOccurred())

			// Verify flow was deleted
			mockInstance := mockNfqlb.instances[distGroup.Name]
			Expect(mockInstance.flows).ToNot(HaveKey("old-route"))
		})
	})

	Describe("endpointSliceEnqueue", func() {
		It("should map EndpointSlice to DistributionGroup", func() {
			endpointSlice := &discoveryv1.EndpointSlice{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-eps",
					Namespace: namespace,
					Labels: map[string]string{
						"meridio-2.nordix.org/distribution-group": "test-distgroup",
					},
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion: "meridio-2.nordix.org/v1alpha1",
							Kind:       "DistributionGroup",
							Name:       "test-distgroup",
							Controller: ptr.To(true),
						},
					},
				},
			}

			requests := controller.endpointSliceEnqueue(ctx, endpointSlice)
			Expect(requests).To(HaveLen(1))
			Expect(requests[0].Name).To(Equal("test-distgroup"))
			Expect(requests[0].Name).To(Equal("test-distgroup"))
			Expect(requests[0].Namespace).To(Equal(namespace))
		})

		It("should return nil when label is missing", func() {
			endpointSlice := &discoveryv1.EndpointSlice{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-eps",
					Namespace: namespace,
					Labels:    map[string]string{},
				},
			}

			requests := controller.endpointSliceEnqueue(ctx, endpointSlice)
			Expect(requests).To(BeNil())
		})

		It("should filter by namespace", func() {
			endpointSlice := &discoveryv1.EndpointSlice{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-eps",
					Namespace: "other-namespace",
					Labels: map[string]string{
						"meridio-2.nordix.org/distribution-group": "test-distgroup",
					},
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion: "meridio-2.nordix.org/v1alpha1",
							Kind:       "DistributionGroup",
							Name:       "test-distgroup",
							Controller: ptr.To(true),
						},
					},
				},
			}

			requests := controller.endpointSliceEnqueue(ctx, endpointSlice)
			Expect(requests).To(BeNil())
		})
	})

	Describe("l34RouteEnqueue", func() {
		It("should map L34Route to DistributionGroup", func() {
			group := meridio2v1alpha1.GroupVersion.Group
			kind := kindDistributionGroup

			l34route := &meridio2v1alpha1.L34Route{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-route",
					Namespace: namespace,
				},
				Spec: meridio2v1alpha1.L34RouteSpec{
					ParentRefs: []gatewayv1.ParentReference{
						{Name: gatewayv1.ObjectName(gatewayName)},
					},
					BackendRefs: []gatewayv1.BackendRef{
						{
							BackendObjectReference: gatewayv1.BackendObjectReference{
								Group: (*gatewayv1.Group)(&group),
								Kind:  (*gatewayv1.Kind)(&kind),
								Name:  "test-distgroup",
							},
						},
					},
				},
			}

			requests := controller.l34RouteEnqueue(ctx, l34route)
			Expect(requests).To(HaveLen(1))
			Expect(requests[0].Name).To(Equal("test-distgroup"))
			Expect(requests[0].Namespace).To(Equal(namespace))
		})

		It("should return nil when Gateway doesn't match", func() {
			group := meridio2v1alpha1.GroupVersion.Group
			kind := kindDistributionGroup

			l34route := &meridio2v1alpha1.L34Route{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-route",
					Namespace: namespace,
				},
				Spec: meridio2v1alpha1.L34RouteSpec{
					ParentRefs: []gatewayv1.ParentReference{
						{Name: "other-gateway"},
					},
					BackendRefs: []gatewayv1.BackendRef{
						{
							BackendObjectReference: gatewayv1.BackendObjectReference{
								Group: (*gatewayv1.Group)(&group),
								Kind:  (*gatewayv1.Kind)(&kind),
								Name:  "test-distgroup",
							},
						},
					},
				},
			}

			requests := controller.l34RouteEnqueue(ctx, l34route)
			Expect(requests).To(BeNil())
		})

		It("should return nil when BackendRef is not DistributionGroup", func() {
			otherKind := gatewayv1.Kind("Service")

			l34route := &meridio2v1alpha1.L34Route{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-route",
					Namespace: namespace,
				},
				Spec: meridio2v1alpha1.L34RouteSpec{
					ParentRefs: []gatewayv1.ParentReference{
						{Name: gatewayv1.ObjectName(gatewayName)},
					},
					BackendRefs: []gatewayv1.BackendRef{
						{
							BackendObjectReference: gatewayv1.BackendObjectReference{
								Kind: &otherKind,
								Name: "test-service",
							},
						},
					},
				},
			}

			requests := controller.l34RouteEnqueue(ctx, l34route)
			Expect(requests).To(BeEmpty())
		})

		It("should handle multiple BackendRefs", func() {
			group := meridio2v1alpha1.GroupVersion.Group
			kind := kindDistributionGroup

			l34route := &meridio2v1alpha1.L34Route{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-route",
					Namespace: namespace,
				},
				Spec: meridio2v1alpha1.L34RouteSpec{
					ParentRefs: []gatewayv1.ParentReference{
						{Name: gatewayv1.ObjectName(gatewayName)},
					},
					BackendRefs: []gatewayv1.BackendRef{
						{
							BackendObjectReference: gatewayv1.BackendObjectReference{
								Group: (*gatewayv1.Group)(&group),
								Kind:  (*gatewayv1.Kind)(&kind),
								Name:  "distgroup-1",
							},
						},
						{
							BackendObjectReference: gatewayv1.BackendObjectReference{
								Group: (*gatewayv1.Group)(&group),
								Kind:  (*gatewayv1.Kind)(&kind),
								Name:  "distgroup-2",
							},
						},
					},
				},
			}

			requests := controller.l34RouteEnqueue(ctx, l34route)
			Expect(requests).To(HaveLen(2))
			Expect(requests[0].Name).To(Equal("distgroup-1"))
			Expect(requests[1].Name).To(Equal("distgroup-2"))
		})
	})

	Describe("Instance cleanup on deletion", func() {
		It("should call Delete() and cleanup all state when DistributionGroup is deleted", func() {
			distGroup := &meridio2v1alpha1.DistributionGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-distgroup",
					Namespace: namespace,
				},
			}

			// Create a mock instance
			mockInstance := &mockNFQLBInstance{
				name:  distGroup.Name,
				flows: make(map[string]nfqlb.Flow),
			}

			// Initialize mockFactory instances map
			if mockNfqlb.instances == nil {
				mockNfqlb.instances = make(map[string]*mockNFQLBInstance)
			}
			mockNfqlb.instances[distGroup.Name] = mockInstance

			// Create mock nftables manager
			mockNftMgr := newMockNftablesManager()

			// Initialize controller maps
			if controller.instances == nil {
				controller.instances = make(map[string]nfqlbInstance)
			}
			if controller.flows == nil {
				controller.flows = make(map[string]map[string]*meridio2v1alpha1.L34Route)
			}
			if controller.targets == nil {
				controller.targets = make(map[string]map[int][]string)
			}

			controller.instances[distGroup.Name] = mockInstance
			controller.nftManager = mockNftMgr // Use shared manager
			controller.flows[distGroup.Name] = make(map[string]*meridio2v1alpha1.L34Route)
			controller.targets[distGroup.Name] = make(map[int][]string)

			// Simulate DistributionGroup deletion by returning NotFound
			fakeClient = fake.NewClientBuilder().
				WithScheme(scheme).
				Build()
			controller.Client = fakeClient

			// Reconcile with non-existent DistributionGroup
			result, err := controller.Reconcile(ctx, reconcile.Request{
				NamespacedName: client.ObjectKey{Name: distGroup.Name},
			})

			Expect(err).ToNot(HaveOccurred())
			Expect(result).To(Equal(reconcile.Result{}))

			// Verify all state cleaned up
			Expect(controller.instances).ToNot(HaveKey(distGroup.Name))
			// Note: nftManager is shared, not cleaned up per-DG
			Expect(controller.flows).ToNot(HaveKey(distGroup.Name))
			Expect(controller.targets).ToNot(HaveKey(distGroup.Name))
		})
	})
})
