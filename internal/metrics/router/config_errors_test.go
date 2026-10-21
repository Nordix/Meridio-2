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

package router

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

const configErrorsHelp = `
# HELP meridio_2_router_route_config_errors_total ` +
	`Number of failed netlink operations in the router's VIP routing path, by reason.
# TYPE meridio_2_router_route_config_errors_total counter
`

func TestConfigErrors_PreInitializedToZero(t *testing.T) {
	c := NewConfigErrors(testPrefix, testGateway)

	// Both reason series must be present at 0 from the start, so rate()/increase() have a
	// baseline and "no errors" is distinguishable from "series absent".
	expected := configErrorsHelp + `
meridio_2_router_route_config_errors_total{gateway="gw-a",reason="route"} 0
meridio_2_router_route_config_errors_total{gateway="gw-a",reason="rule"} 0
`
	require.NoError(t, testutil.CollectAndCompare(c.Collector(), strings.NewReader(expected)))
}

func TestConfigErrors_Inc(t *testing.T) {
	c := NewConfigErrors(testPrefix, testGateway)

	c.Inc(ReasonRule)
	c.Inc(ReasonRule)
	c.Inc(ReasonRoute)

	expected := configErrorsHelp + `
meridio_2_router_route_config_errors_total{gateway="gw-a",reason="route"} 1
meridio_2_router_route_config_errors_total{gateway="gw-a",reason="rule"} 2
`
	require.NoError(t, testutil.CollectAndCompare(c.Collector(), strings.NewReader(expected)))
}

func TestConfigErrors_NilReceiverIncIsNoOp(t *testing.T) {
	var c *ConfigErrors // nil, as when metrics are disabled
	require.NotPanics(t, func() { c.Inc(ReasonRule) })
}

// The reasons are defined in internal/bird, where the increments happen, and re-exported here.
// This guards the aliasing: a divergence would silently relabel the series.
func TestConfigErrors_ReasonsMatchBird(t *testing.T) {
	require.Equal(t, "rule", ReasonRule)
	require.Equal(t, "route", ReasonRoute)
}
