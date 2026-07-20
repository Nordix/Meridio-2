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

func TestLoadBalancerController(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "LoadBalancer Controller Suite")
}

// newFakeClient creates a fake client with the field indexer for LoadBalancerEndpointSlice
// registered. All tests that call reconcileTargets need this.
func newFakeClient(scheme *runtime.Scheme, objects ...client.Object) client.Client {
	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithIndex(&meridio2v1alpha1.LoadBalancerEndpointSlice{}, "spec.distributionGroupName", func(obj client.Object) []string {
			return []string{obj.(*meridio2v1alpha1.LoadBalancerEndpointSlice).Spec.DistributionGroupName}
		}).
		WithIndex(&meridio2v1alpha1.LoadBalancerEndpointSlice{}, "spec.gatewayRef.name", func(obj client.Object) []string {
			return []string{obj.(*meridio2v1alpha1.LoadBalancerEndpointSlice).Spec.GatewayRef.Name}
		}).
		Build()
}

// newTestLBEPS creates a LoadBalancerEndpointSlice with sensible defaults for testing.
// Defaults: namespace "default", gatewayRef pointing to "test-gateway"/"default",
// ownerRef to the given DG, and distributionGroupName matching the DG.
// Functional options override specific fields to isolate what each test checks.
func newTestLBEPS(dg *meridio2v1alpha1.DistributionGroup, endpoints []meridio2v1alpha1.LoadBalancerEndpoint, opts ...func(*meridio2v1alpha1.LoadBalancerEndpointSlice)) *meridio2v1alpha1.LoadBalancerEndpointSlice {
	lbeps := &meridio2v1alpha1.LoadBalancerEndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-eps",
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "meridio-2.nordix.org/v1alpha1",
				Kind:       "DistributionGroup",
				Name:       dg.Name,
				UID:        dg.UID,
				Controller: ptr.To(true),
			}},
		},
		Spec: meridio2v1alpha1.LoadBalancerEndpointSliceSpec{
			DistributionGroupName: dg.Name,
			GatewayRef: meridio2v1alpha1.SliceGatewayRef{
				Name:      "test-gateway",
				Namespace: "default",
			},
			Endpoints: endpoints,
		},
	}
	for _, opt := range opts {
		opt(lbeps)
	}
	return lbeps
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

func (m *mockNFQLBInstance) DeleteTarget(_ context.Context, identifier int) error {
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

			fakeClient = newFakeClient(scheme, l34route)
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

			fakeClient = newFakeClient(scheme)
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

			fakeClient = newFakeClient(scheme, l34route)
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

			fakeClient = newFakeClient(scheme)
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

			fakeClient = newFakeClient(scheme)
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
			fakeClient = newFakeClient(scheme)
			controller.Client = fakeClient

			result, err := controller.belongsToGateway(ctx, distGroup)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(BeTrue())
		})
	})

	Describe("reconcileNFQLBInstance", func() {
		BeforeEach(func() {
			fakeClient = newFakeClient(scheme)
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
			lbeps := newTestLBEPS(distGroup, []meridio2v1alpha1.LoadBalancerEndpoint{
				{
					Target:     meridio2v1alpha1.EndpointTarget{Name: "pod-1", UID: "uid-1"},
					Addresses:  []meridio2v1alpha1.EndpointAddress{{IP: "10.0.0.1", Family: meridio2v1alpha1.IPv4}},
					Identifier: ptr.To(int32(0)),
					Ready:      true,
				},
				{
					Target:     meridio2v1alpha1.EndpointTarget{Name: "pod-2", UID: "uid-2"},
					Addresses:  []meridio2v1alpha1.EndpointAddress{{IP: "10.0.0.2", Family: meridio2v1alpha1.IPv4}},
					Identifier: ptr.To(int32(1)),
					Ready:      true,
				},
			})

			fakeClient = newFakeClient(scheme, lbeps)
			controller.Client = fakeClient

			err := controller.reconcileTargets(ctx, distGroup)
			Expect(err).ToNot(HaveOccurred())

			// Calculate expected fwmark offset for this DistributionGroup
			// Targets are now tracked by identifier (offset is internal to nfqlb)

			// Verify targets were activated with correct fwmark
			// identifier=0 -> fwmark=offset+0
			// identifier=1 -> fwmark=offset+1
			mockInstance := mockNfqlb.instances[distGroup.Name]
			Expect(mockInstance.targets).To(HaveKey(0)) // fwmark = 0 + offset (internal to nfqlb)
			Expect(mockInstance.targets[0]).To(Equal([]string{"10.0.0.1"}))
			Expect(mockInstance.targets).To(HaveKey(1)) // fwmark = 1 + offset (internal to nfqlb)
			Expect(mockInstance.targets[1]).To(Equal([]string{"10.0.0.2"}))
		})

		It("should skip endpoints without Identifier field", func() {
			lbeps := newTestLBEPS(distGroup, []meridio2v1alpha1.LoadBalancerEndpoint{
				{
					Target:     meridio2v1alpha1.EndpointTarget{Name: "pod-1", UID: "uid-1"},
					Addresses:  []meridio2v1alpha1.EndpointAddress{{IP: "10.0.0.1", Family: meridio2v1alpha1.IPv4}},
					Identifier: nil, // no identifier
					Ready:      true,
				},
			})

			fakeClient = newFakeClient(scheme, lbeps)
			controller.Client = fakeClient

			err := controller.reconcileTargets(ctx, distGroup)
			Expect(err).ToNot(HaveOccurred())

			// No targets should be activated
			mockInstance := mockNfqlb.instances[distGroup.Name]
			Expect(mockInstance.targets).To(BeEmpty())
		})

		It("should skip non-ready endpoints", func() {
			lbeps := newTestLBEPS(distGroup, []meridio2v1alpha1.LoadBalancerEndpoint{
				{
					Target:     meridio2v1alpha1.EndpointTarget{Name: "pod-1", UID: "uid-1"},
					Addresses:  []meridio2v1alpha1.EndpointAddress{{IP: "10.0.0.1", Family: meridio2v1alpha1.IPv4}},
					Identifier: ptr.To(int32(0)),
					Ready:      false, // not ready
				},
			})

			fakeClient = newFakeClient(scheme, lbeps)
			controller.Client = fakeClient

			err := controller.reconcileTargets(ctx, distGroup)
			Expect(err).ToNot(HaveOccurred())

			// No targets should be activated
			mockInstance := mockNfqlb.instances[distGroup.Name]
			Expect(mockInstance.targets).To(BeEmpty())
		})

		It("should deactivate removed targets with correct index", func() {
			// Setup: activate a target first (identifier=0)
			controller.targets = map[string]map[int]struct{}{
				distGroup.Name: {
					0: {},
				},
			}

			// No LoadBalancerEndpointSlices (target removed)
			fakeClient = newFakeClient(scheme)
			controller.Client = fakeClient

			err := controller.reconcileTargets(ctx, distGroup)
			Expect(err).ToNot(HaveOccurred())

			// Verify target was deactivated with index=1 (identifier 0 + 1)
			mockInstance := mockNfqlb.instances[distGroup.Name]
			Expect(mockInstance.targets).ToNot(HaveKey(0))
		})

		It("should activate dual-stack endpoint with both IPs", func() {
			lbeps := newTestLBEPS(distGroup, []meridio2v1alpha1.LoadBalancerEndpoint{
				{
					Target: meridio2v1alpha1.EndpointTarget{Name: "pod-1", UID: "uid-1"},
					Addresses: []meridio2v1alpha1.EndpointAddress{
						{IP: "192.168.100.10", Family: meridio2v1alpha1.IPv4},
						{IP: "2001:db8:100::10", Family: meridio2v1alpha1.IPv6},
					},
					Identifier: ptr.To(int32(0)),
					Ready:      true,
				},
			})

			fakeClient = newFakeClient(scheme, lbeps)
			controller.Client = fakeClient

			err := controller.reconcileTargets(ctx, distGroup)
			Expect(err).ToNot(HaveOccurred())

			// Both IPv4 and IPv6 addresses activated under the same identifier
			mockInstance := mockNfqlb.instances[distGroup.Name]
			Expect(mockInstance.targets).To(HaveKey(0))
			Expect(mockInstance.targets[0]).To(ConsistOf("192.168.100.10", "2001:db8:100::10"))
		})

		It("should ignore slices scoped to a different Gateway", func() {
			// Slice for this Gateway — should be processed
			lbepsOurs := newTestLBEPS(distGroup, []meridio2v1alpha1.LoadBalancerEndpoint{
				{
					Target:     meridio2v1alpha1.EndpointTarget{Name: "pod-1", UID: "uid-1"},
					Addresses:  []meridio2v1alpha1.EndpointAddress{{IP: "10.0.0.1", Family: meridio2v1alpha1.IPv4}},
					Identifier: ptr.To(int32(0)),
					Ready:      true,
				},
			})

			// Slice for a different Gateway — should be excluded by field index
			lbepsOther := newTestLBEPS(distGroup, []meridio2v1alpha1.LoadBalancerEndpoint{
				{
					Target:     meridio2v1alpha1.EndpointTarget{Name: "pod-2", UID: "uid-2"},
					Addresses:  []meridio2v1alpha1.EndpointAddress{{IP: "10.0.0.2", Family: meridio2v1alpha1.IPv4}},
					Identifier: ptr.To(int32(1)),
					Ready:      true,
				},
			}, func(s *meridio2v1alpha1.LoadBalancerEndpointSlice) {
				s.Name = "other-gw-eps"
				s.Spec.GatewayRef.Name = "other-gateway"
			})

			fakeClient = newFakeClient(scheme, lbepsOurs, lbepsOther)
			controller.Client = fakeClient

			err := controller.reconcileTargets(ctx, distGroup)
			Expect(err).ToNot(HaveOccurred())

			// Only the slice for our Gateway should produce a target
			mockInstance := mockNfqlb.instances[distGroup.Name]
			Expect(mockInstance.targets).To(HaveKey(0))
			Expect(mockInstance.targets[0]).To(Equal([]string{"10.0.0.1"}))
			Expect(mockInstance.targets).ToNot(HaveKey(1), "slice for other Gateway should be excluded")
		})

		It("should use first-occurrence-wins for duplicate identifiers across slices", func() {
			// Two slices with the same identifier=0 but different IPs.
			// Sorted by name: "slice-a" < "slice-b", so slice-a's endpoint wins.
			sliceA := newTestLBEPS(distGroup, []meridio2v1alpha1.LoadBalancerEndpoint{
				{
					Target:     meridio2v1alpha1.EndpointTarget{Name: "pod-1", UID: "uid-1"},
					Addresses:  []meridio2v1alpha1.EndpointAddress{{IP: "10.0.0.1", Family: meridio2v1alpha1.IPv4}},
					Identifier: ptr.To(int32(0)),
					Ready:      true,
				},
			}, func(s *meridio2v1alpha1.LoadBalancerEndpointSlice) {
				s.Name = "slice-a"
			})

			sliceB := newTestLBEPS(distGroup, []meridio2v1alpha1.LoadBalancerEndpoint{
				{
					Target:     meridio2v1alpha1.EndpointTarget{Name: "pod-2", UID: "uid-2"},
					Addresses:  []meridio2v1alpha1.EndpointAddress{{IP: "10.0.0.99", Family: meridio2v1alpha1.IPv4}},
					Identifier: ptr.To(int32(0)),
					Ready:      true,
				},
			}, func(s *meridio2v1alpha1.LoadBalancerEndpointSlice) {
				s.Name = "slice-b"
			})

			fakeClient = newFakeClient(scheme, sliceA, sliceB)
			controller.Client = fakeClient

			err := controller.reconcileTargets(ctx, distGroup)
			Expect(err).ToNot(HaveOccurred())

			// First occurrence (slice-a, sorted by name) wins
			mockInstance := mockNfqlb.instances[distGroup.Name]
			Expect(mockInstance.targets).To(HaveKey(0))
			Expect(mockInstance.targets[0]).To(Equal([]string{"10.0.0.1"}),
				"first-occurrence-wins: slice-a should take priority over slice-b for identifier=0")
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

			// Create LoadBalancerEndpointSlice with ready endpoints
			lbeps := newTestLBEPS(distGroup, []meridio2v1alpha1.LoadBalancerEndpoint{{
				Target:     meridio2v1alpha1.EndpointTarget{Name: "pod-1", UID: "uid-1"},
				Addresses:  []meridio2v1alpha1.EndpointAddress{{IP: "10.0.0.1", Family: meridio2v1alpha1.IPv4}},
				Identifier: ptr.To(int32(0)),
				Ready:      true,
			}})

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

			fakeClient = newFakeClient(scheme, distGroup, lbeps, l34route, gateway)
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

			// Create LoadBalancerEndpointSlice with ready endpoints
			lbeps := newTestLBEPS(distGroup, []meridio2v1alpha1.LoadBalancerEndpoint{{
				Target:     meridio2v1alpha1.EndpointTarget{Name: "pod-1", UID: "uid-1"},
				Addresses:  []meridio2v1alpha1.EndpointAddress{{IP: "10.0.0.1", Family: meridio2v1alpha1.IPv4}},
				Identifier: ptr.To(int32(0)),
				Ready:      true,
			}})

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

			fakeClient = newFakeClient(scheme, distGroup, lbeps, l34route1, l34route2, gateway)
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

			fakeClient = newFakeClient(scheme, l34route)
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

			fakeClient = newFakeClient(scheme, l34route)
			controller.Client = fakeClient

			err := controller.reconcileFlows(ctx, distGroup)
			Expect(err).ToNot(HaveOccurred())

			// No flows should be configured
			mockInstance := mockNfqlb.instances[distGroup.Name]
			Expect(mockInstance.flows).To(BeEmpty())
		})

		It("should preserve flows when DistributionGroup has no ready endpoints", func() {
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

			// No LoadBalancerEndpointSlice (no endpoints) but L34Route and Gateway exist
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

			fakeClient = newFakeClient(scheme, distGroup, l34route, gateway)
			controller.Client = fakeClient

			err := controller.reconcileFlows(ctx, distGroup)
			Expect(err).ToNot(HaveOccurred())

			// Flows should be preserved (L34Route still exists)
			mockInstance := mockNfqlb.instances[distGroup.Name]
			Expect(mockInstance.flows).To(HaveKey("test-route"))
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
			fakeClient = newFakeClient(scheme)
			controller.Client = fakeClient

			err := controller.reconcileFlows(ctx, distGroup)
			Expect(err).ToNot(HaveOccurred())

			// Verify flow was deleted
			mockInstance := mockNfqlb.instances[distGroup.Name]
			Expect(mockInstance.flows).ToNot(HaveKey("old-route"))
		})
	})

	Describe("endpointSliceEnqueue", func() {
		var distGroup *meridio2v1alpha1.DistributionGroup

		BeforeEach(func() {
			distGroup = &meridio2v1alpha1.DistributionGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-distgroup",
					Namespace: namespace,
				},
			}
		})

		It("should map LoadBalancerEndpointSlice to DistributionGroup", func() {
			lbeps := newTestLBEPS(distGroup, nil)

			requests := controller.endpointSliceEnqueue(ctx, lbeps)
			Expect(requests).To(HaveLen(1))
			Expect(requests[0].Name).To(Equal("test-distgroup"))
			Expect(requests[0].Namespace).To(Equal(namespace))
		})

		It("should return nil when ownerReference is missing", func() {
			lbeps := newTestLBEPS(distGroup, nil, func(s *meridio2v1alpha1.LoadBalancerEndpointSlice) {
				s.OwnerReferences = nil
			})

			requests := controller.endpointSliceEnqueue(ctx, lbeps)
			Expect(requests).To(BeNil())
		})

		It("should filter by namespace", func() {
			lbeps := newTestLBEPS(distGroup, nil, func(s *meridio2v1alpha1.LoadBalancerEndpointSlice) {
				s.Namespace = "other-namespace"
			})

			requests := controller.endpointSliceEnqueue(ctx, lbeps)
			Expect(requests).To(BeNil())
		})

		It("should skip slices scoped to a different Gateway", func() {
			lbeps := newTestLBEPS(distGroup, nil, func(s *meridio2v1alpha1.LoadBalancerEndpointSlice) {
				s.Spec.GatewayRef.Name = "other-gateway"
			})

			requests := controller.endpointSliceEnqueue(ctx, lbeps)
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
				controller.targets = make(map[string]map[int]struct{})
			}

			controller.instances[distGroup.Name] = mockInstance
			controller.nftManager = mockNftMgr // Use shared manager
			controller.flows[distGroup.Name] = make(map[string]*meridio2v1alpha1.L34Route)
			controller.targets[distGroup.Name] = make(map[int]struct{})

			// Simulate DistributionGroup deletion by returning NotFound
			fakeClient = newFakeClient(scheme)
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
