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
	"errors"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

// fakeRoutes is a RouteReader test double.
type fakeRoutes struct {
	counts map[string]int
	err    error
}

func (f *fakeRoutes) PolicyRouteCounts() (map[string]int, error) { return f.counts, f.err }

func newTestRouteCollector(routes RouteReader, errs errorRecorder) *RouteCollector {
	return NewRouteCollector(routes, testGateway, testPrefix, errs)
}

func TestRouteCollector_PolicyRoutes_PerFamily(t *testing.T) {
	c := newTestRouteCollector(&fakeRoutes{counts: map[string]int{"IPv4": 3, "IPv6": 2}}, newFakeCollectorErrors())

	expected := `
# HELP meridio_2_lb_policy_routes Number of LB-owned policy routing rules currently installed, per IP family (one source-based routing rule per active target).
# TYPE meridio_2_lb_policy_routes gauge
meridio_2_lb_policy_routes{gateway="gw-a",ip_family="IPv4"} 3
meridio_2_lb_policy_routes{gateway="gw-a",ip_family="IPv6"} 2
`
	require.NoError(t, testutil.CollectAndCompare(c, strings.NewReader(expected),
		"meridio_2_lb_policy_routes"))
}

// A configured-but-empty family is emitted at 0 (not omitted).
func TestRouteCollector_PolicyRoutes_EmptyFamilyIsZero(t *testing.T) {
	c := newTestRouteCollector(&fakeRoutes{counts: map[string]int{"IPv4": 0, "IPv6": 0}}, newFakeCollectorErrors())

	expected := `
# HELP meridio_2_lb_policy_routes Number of LB-owned policy routing rules currently installed, per IP family (one source-based routing rule per active target).
# TYPE meridio_2_lb_policy_routes gauge
meridio_2_lb_policy_routes{gateway="gw-a",ip_family="IPv4"} 0
meridio_2_lb_policy_routes{gateway="gw-a",ip_family="IPv6"} 0
`
	require.NoError(t, testutil.CollectAndCompare(c, strings.NewReader(expected),
		"meridio_2_lb_policy_routes"))
}

// On a read error the collector SKIPS the series and counts the failure (no invalid metric).
func TestRouteCollector_PolicyRoutes_Error(t *testing.T) {
	errs := newFakeCollectorErrors()
	c := newTestRouteCollector(&fakeRoutes{err: errors.New("rule list failed")}, errs)

	require.NoError(t, testutil.CollectAndCompare(c, strings.NewReader(""),
		"meridio_2_lb_policy_routes"))
	require.Equal(t, 1, errs.count(CollectorRoute, CollectorReasonPolicyRoutes))
}
