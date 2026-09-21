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

package networksidecar

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	meridio2v1alpha1 "github.com/nordix/meridio-2/api/v1alpha1"
)

const (
	testPodName = "app-pod-0"
	testPodNS   = "default"
	testPrefix  = "meridio_2"
)

// alwaysSyncedWaiter is a metricsutil.CacheSyncWaiter test double that always reports synced
type alwaysSyncedWaiter struct{}

func (alwaysSyncedWaiter) WaitForCacheSync(_ context.Context) bool { return true }

func scScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	_ = meridio2v1alpha1.AddToScheme(scheme)
	return scheme
}

func newCollector(objs ...*meridio2v1alpha1.EndpointNetworkConfiguration) *SidecarCollector {
	builder := fake.NewClientBuilder().WithScheme(scScheme())
	for _, o := range objs {
		builder = builder.WithObjects(o)
	}
	return NewSidecarCollector(builder.Build(), alwaysSyncedWaiter{}, time.Second, testPodName, testPodNS, testPrefix)
}

// enc returns an ENC named after the test Pod, in the test namespace.
func enc(gateways ...meridio2v1alpha1.GatewayConnection) *meridio2v1alpha1.EndpointNetworkConfiguration {
	return &meridio2v1alpha1.EndpointNetworkConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: testPodName, Namespace: testPodNS},
		Spec:       meridio2v1alpha1.EndpointNetworkConfigurationSpec{Gateways: gateways},
	}
}

func TestSidecarCollector_VIPsAndNexthops_PerGatewayAndFamily(t *testing.T) {
	collector := newCollector(enc(
		meridio2v1alpha1.GatewayConnection{
			Name: "gw-a",
			Domains: []meridio2v1alpha1.NetworkDomain{
				{
					Name:     "gw-a-v4",
					IPFamily: "IPv4",
					VIPs:     []string{"20.0.0.1", "20.0.0.2"},
					NextHops: []string{"169.254.0.1"},
				},
				{
					Name:     "gw-a-v6",
					IPFamily: "IPv6",
					VIPs:     []string{"2000::1"},
					NextHops: []string{"2001:db8::1", "2001:db8::2"},
				},
			},
		},
	))

	// gw-a: VIPs = 2 (v4) + 1 (v6) = 3, summed across domains (gateway label only).
	// nexthops: v4 = 1, v6 = 2 (split by ip_family).
	expected := `
# HELP meridio_2_sidecar_nexthops Number of next-hops configured on this Pod for the given Gateway and IP family, from the ENC spec.
# TYPE meridio_2_sidecar_nexthops gauge
meridio_2_sidecar_nexthops{gateway="gw-a",ip_family="IPv4"} 1
meridio_2_sidecar_nexthops{gateway="gw-a",ip_family="IPv6"} 2
# HELP meridio_2_sidecar_vips_configured Number of VIP addresses configured on this Pod for the given Gateway, from the ENC spec.
# TYPE meridio_2_sidecar_vips_configured gauge
meridio_2_sidecar_vips_configured{gateway="gw-a"} 3
`
	require.NoError(t, testutil.CollectAndCompare(collector, strings.NewReader(expected),
		"meridio_2_sidecar_vips_configured", "meridio_2_sidecar_nexthops"))
}

func TestSidecarCollector_MultipleGateways(t *testing.T) {
	collector := newCollector(enc(
		meridio2v1alpha1.GatewayConnection{
			Name: "gw-a",
			Domains: []meridio2v1alpha1.NetworkDomain{
				{Name: "gw-a-v4", IPFamily: "IPv4", VIPs: []string{"20.0.0.1"}, NextHops: []string{"169.254.0.1"}},
			},
		},
		meridio2v1alpha1.GatewayConnection{
			Name: "gw-b",
			Domains: []meridio2v1alpha1.NetworkDomain{
				{Name: "gw-b-v4", IPFamily: "IPv4", VIPs: []string{"30.0.0.1", "30.0.0.2"}, NextHops: []string{"169.254.1.1"}},
			},
		},
	))

	expected := `
# HELP meridio_2_sidecar_vips_configured Number of VIP addresses configured on this Pod for the given Gateway, from the ENC spec.
# TYPE meridio_2_sidecar_vips_configured gauge
meridio_2_sidecar_vips_configured{gateway="gw-a"} 1
meridio_2_sidecar_vips_configured{gateway="gw-b"} 2
`
	require.NoError(t, testutil.CollectAndCompare(collector, strings.NewReader(expected),
		"meridio_2_sidecar_vips_configured"))
}

// TestSidecarCollector_NoENC_NoSeries verifies that a missing ENC (Pod has no network config
// yet) is treated as "nothing configured" — no series emitted, no error.
func TestSidecarCollector_NoENC_NoSeries(t *testing.T) {
	collector := newCollector() // no ENC in the fake client

	require.NoError(t, testutil.CollectAndCompare(collector, strings.NewReader(""),
		"meridio_2_sidecar_vips_configured", "meridio_2_sidecar_nexthops"))
}

// TestSidecarCollector_VIPsEmpty_WithNextHops verifies the collector handles a domain that has
// next-hops but no VIPs. This is a reachable ENC state: the ENC controller's buildGatewayConnection
// skips a domain only when BOTH vips and next-hops are empty, so a family with resolved SLLBR
// next-hops but no assigned VIPs is emitted into the ENC. The gateway's vips_configured is then 0
// (summed across its domains), while the nexthops series reflects the configured next-hops.
func TestSidecarCollector_VIPsEmpty_WithNextHops(t *testing.T) {
	collector := newCollector(enc(
		meridio2v1alpha1.GatewayConnection{
			Name: "gw-a",
			Domains: []meridio2v1alpha1.NetworkDomain{
				{Name: "gw-a-v6", IPFamily: "IPv6", NextHops: []string{"2001:db8::1"}},
			},
		},
	))

	expected := `
# HELP meridio_2_sidecar_nexthops Number of next-hops configured on this Pod for the given Gateway and IP family, from the ENC spec.
# TYPE meridio_2_sidecar_nexthops gauge
meridio_2_sidecar_nexthops{gateway="gw-a",ip_family="IPv6"} 1
# HELP meridio_2_sidecar_vips_configured Number of VIP addresses configured on this Pod for the given Gateway, from the ENC spec.
# TYPE meridio_2_sidecar_vips_configured gauge
meridio_2_sidecar_vips_configured{gateway="gw-a"} 0
`
	require.NoError(t, testutil.CollectAndCompare(collector, strings.NewReader(expected),
		"meridio_2_sidecar_vips_configured", "meridio_2_sidecar_nexthops"))
}

// next-hops still emits a nexthops series at 0 for that family (a configured-but-empty family is
// distinguishable from an absent one.
func TestSidecarCollector_GatewayWithNoNexthops(t *testing.T) {
	collector := newCollector(enc(
		meridio2v1alpha1.GatewayConnection{
			Name: "gw-a",
			Domains: []meridio2v1alpha1.NetworkDomain{
				{Name: "gw-a-v4", IPFamily: "IPv4", VIPs: []string{"20.0.0.1"}},
			},
		},
	))

	expected := `
# HELP meridio_2_sidecar_nexthops Number of next-hops configured on this Pod for the given Gateway and IP family, from the ENC spec.
# TYPE meridio_2_sidecar_nexthops gauge
meridio_2_sidecar_nexthops{gateway="gw-a",ip_family="IPv4"} 0
# HELP meridio_2_sidecar_vips_configured Number of VIP addresses configured on this Pod for the given Gateway, from the ENC spec.
# TYPE meridio_2_sidecar_vips_configured gauge
meridio_2_sidecar_vips_configured{gateway="gw-a"} 1
`
	require.NoError(t, testutil.CollectAndCompare(collector, strings.NewReader(expected),
		"meridio_2_sidecar_vips_configured", "meridio_2_sidecar_nexthops"))
}

// TestSidecarCollector_DomainWithNoVIPsOrNextHops locks the collector's behavior for a domain
// carrying neither VIPs nor next-hops. The ENC controller currently skips such a domain upstream
// (see the package doc), but if that changes, the collector emits vips_configured=0 for the
// gateway and nexthops{family}=0 for the domain's family.
func TestSidecarCollector_DomainWithNoVIPsOrNextHops(t *testing.T) {
	collector := newCollector(enc(
		meridio2v1alpha1.GatewayConnection{
			Name: "gw-a",
			Domains: []meridio2v1alpha1.NetworkDomain{
				{Name: "gw-a-v4", IPFamily: "IPv4"},
			},
		},
	))

	expected := `
# HELP meridio_2_sidecar_nexthops Number of next-hops configured on this Pod for the given Gateway and IP family, from the ENC spec.
# TYPE meridio_2_sidecar_nexthops gauge
meridio_2_sidecar_nexthops{gateway="gw-a",ip_family="IPv4"} 0
# HELP meridio_2_sidecar_vips_configured Number of VIP addresses configured on this Pod for the given Gateway, from the ENC spec.
# TYPE meridio_2_sidecar_vips_configured gauge
meridio_2_sidecar_vips_configured{gateway="gw-a"} 0
`
	require.NoError(t, testutil.CollectAndCompare(collector, strings.NewReader(expected),
		"meridio_2_sidecar_vips_configured", "meridio_2_sidecar_nexthops"))
}
