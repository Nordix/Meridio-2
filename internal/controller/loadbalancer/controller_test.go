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

	nspAPI "github.com/nordix/meridio/api/nsp/v1"
	"github.com/nordix/meridio/pkg/loadbalancer/types"
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

// Mock NFQLB instance
type mockNFQLBInstance struct {
	name               string
	activatedTargets   map[int]int // map[fwmark]index for verification
	deactivatedIndexes map[int]bool
	flows              map[string]*nspAPI.Flow // map[flow-name]flow
	deletedFlows       map[string]bool
}

func (m *mockNFQLBInstance) Activate(index int, fwmark int) error {
	if m.activatedTargets == nil {
		m.activatedTargets = make(map[int]int)
	}
	m.activatedTargets[fwmark] = index
	return nil
}

func (m *mockNFQLBInstance) Deactivate(index int) error {
	if m.deactivatedIndexes == nil {
		m.deactivatedIndexes = make(map[int]bool)
	}
	m.deactivatedIndexes[index] = true
	return nil
}

func (m *mockNFQLBInstance) Start() error  { return nil }
func (m *mockNFQLBInstance) Delete() error { return nil }

func (m *mockNFQLBInstance) SetFlow(flow *nspAPI.Flow) error {
	if m.flows == nil {
		m.flows = make(map[string]*nspAPI.Flow)
	}
	m.flows[flow.GetName()] = flow
	return nil
}

func (m *mockNFQLBInstance) DeleteFlow(flow *nspAPI.Flow) error {
	if m.deletedFlows == nil {
		m.deletedFlows = make(map[string]bool)
	}
	m.deletedFlows[flow.GetName()] = true
	return nil
}

func (m *mockNFQLBInstance) GetName() string { return m.name }

// Mock NFQLB factory
type mockNFQLBFactory struct {
	instances map[string]*mockNFQLBInstance
}

func (m *mockNFQLBFactory) Start(ctx context.Context) error {
	return nil
}

func (m *mockNFQLBFactory) New(name string, mParam int, nParam int) (types.NFQueueLoadBalancer, error) {
	if m.instances == nil {
		m.instances = make(map[string]*mockNFQLBInstance)
	}
	instance := &mockNFQLBInstance{name: name}
	m.instances[name] = instance
	return instance, nil
}

var _ = Describe("LoadBalancer Controller", func() {
	var (
		scheme      *runtime.Scheme
		fakeClient  client.Client
		controller  *Controller
		mockFactory *mockNFQLBFactory
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

		mockFactory = &mockNFQLBFactory{}

		controller = &Controller{
			Scheme:           scheme,
			GatewayName:      gatewayName,
			GatewayNamespace: namespace,
			LBFactory:        mockFactory,
			routingManager:   NewMockRoutingManager(),
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
			Expect(mockFactory.instances).To(HaveKey(distGroup.Name))
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

			// Verify sequential IDs
			Expect(controller.dgIDs["dg-1"]).To(Equal(0))
			Expect(controller.dgIDs["dg-2"]).To(Equal(1))
			Expect(controller.dgIDs["dg-3"]).To(Equal(2))
			Expect(controller.nextID).To(Equal(3))
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

			// Create three instances (IDs: 0, 1, 2)
			Expect(controller.reconcileNFQLBInstance(ctx, dg1)).To(Succeed())
			Expect(controller.reconcileNFQLBInstance(ctx, dg2)).To(Succeed())
			Expect(controller.reconcileNFQLBInstance(ctx, dg3)).To(Succeed())

			// Delete dg-2 (frees ID 1)
			_, err := controller.cleanupDistributionGroup(ctx, "dg-2")
			Expect(err).ToNot(HaveOccurred())
			Expect(controller.freedIDs).To(ContainElement(1))

			// Create new DG - should reuse ID 1
			dg4 := &meridio2v1alpha1.DistributionGroup{
				ObjectMeta: metav1.ObjectMeta{Name: "dg-4", Namespace: namespace},
			}
			Expect(controller.reconcileNFQLBInstance(ctx, dg4)).To(Succeed())
			Expect(controller.dgIDs["dg-4"]).To(Equal(1))
			Expect(controller.freedIDs).To(BeEmpty())
			Expect(controller.nextID).To(Equal(3)) // Should not increment
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
			expectedOffset := controller.getFwmarkOffset(distGroup.Name)

			// Verify targets were activated with correct fwmark
			// identifier=0 -> index=1, fwmark=offset+0
			// identifier=1 -> index=2, fwmark=offset+1
			mockInstance := mockFactory.instances[distGroup.Name]
			Expect(mockInstance.activatedTargets).To(HaveKey(expectedOffset))     // fwmark = 0 + offset
			Expect(mockInstance.activatedTargets[expectedOffset]).To(Equal(1))    // index = 0 + 1
			Expect(mockInstance.activatedTargets).To(HaveKey(expectedOffset + 1)) // fwmark = 1 + offset
			Expect(mockInstance.activatedTargets[expectedOffset+1]).To(Equal(2))  // index = 1 + 1
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
			mockInstance := mockFactory.instances[distGroup.Name]
			Expect(mockInstance.activatedTargets).To(BeEmpty())
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
			mockInstance := mockFactory.instances[distGroup.Name]
			Expect(mockInstance.activatedTargets).To(BeEmpty())
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
			mockInstance := mockFactory.instances[distGroup.Name]
			Expect(mockInstance.deactivatedIndexes).To(HaveKey(1))
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
			expectedOffset := controller.getFwmarkOffset(distGroup.Name)

			// Verify targets were activated with correct identifiers
			mockInstance := mockFactory.instances[distGroup.Name]
			Expect(mockInstance.activatedTargets).To(HaveKey(expectedOffset))     // fwmark = 0 + offset
			Expect(mockInstance.activatedTargets[expectedOffset]).To(Equal(1))    // index = 0 + 1
			Expect(mockInstance.activatedTargets).To(HaveKey(expectedOffset + 1)) // fwmark = 1 + offset
			Expect(mockInstance.activatedTargets[expectedOffset+1]).To(Equal(2))  // index = 1 + 1
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
			expectedOffset := controller.getFwmarkOffset(distGroup.Name)

			// Only valid endpoint should be activated
			mockInstance := mockFactory.instances[distGroup.Name]
			Expect(mockInstance.activatedTargets).To(HaveLen(1))
			Expect(mockInstance.activatedTargets).To(HaveKey(expectedOffset)) // Only maglev:0
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
			mockInstance := mockFactory.instances[distGroup.Name]
			Expect(mockInstance.flows).To(HaveLen(1))

			// Check flow details
			var flow *nspAPI.Flow
			for _, f := range mockInstance.flows {
				flow = f
				break
			}
			Expect(flow).ToNot(BeNil())
			Expect(flow.GetName()).To(Equal("test-route"))
			Expect(flow.GetPriority()).To(Equal(int32(100)))
			Expect(flow.GetProtocols()).To(ConsistOf("TCP"))
			Expect(flow.GetVips()).To(HaveLen(1))
			Expect(flow.GetVips()[0].GetAddress()).To(Equal("20.0.0.1/32"))
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
			mockInstance := mockFactory.instances[distGroup.Name]
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
			mockInstance := mockFactory.instances[distGroup.Name]
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
			mockInstance := mockFactory.instances[distGroup.Name]
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
			mockInstance := mockFactory.instances[distGroup.Name]
			Expect(mockInstance.flows).To(BeEmpty())
			Expect(mockInstance.deletedFlows).To(HaveKey("test-route"))
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
			mockInstance := mockFactory.instances[distGroup.Name]
			Expect(mockInstance.deletedFlows).To(HaveKey("old-route"))
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
				flows: make(map[string]*nspAPI.Flow),
			}

			// Initialize mockFactory instances map
			if mockFactory.instances == nil {
				mockFactory.instances = make(map[string]*mockNFQLBInstance)
			}
			mockFactory.instances[distGroup.Name] = mockInstance

			// Create mock nftables manager
			mockNftMgr := newMockNftablesManager()

			// Initialize controller maps
			if controller.instances == nil {
				controller.instances = make(map[string]types.NFQueueLoadBalancer)
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
