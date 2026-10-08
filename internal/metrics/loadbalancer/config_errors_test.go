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
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func TestConfigErrors_PreInitializedToZero(t *testing.T) {
	c := NewConfigErrors(testGateway, testPrefix)

	// Both reason series exist at 0 from the first scrape (rate()/increase() baseline).
	expected := `
# HELP meridio_2_lb_route_config_errors_total Number of failed data-plane config operations in the LB reconcile path, by reason (route_config: policy rule/route or target activation failure; vip_config: nftables VIP set update failure).
# TYPE meridio_2_lb_route_config_errors_total counter
meridio_2_lb_route_config_errors_total{gateway="gw-a",reason="route_config"} 0
meridio_2_lb_route_config_errors_total{gateway="gw-a",reason="vip_config"} 0
`
	require.NoError(t, testutil.CollectAndCompare(c.Collector(), strings.NewReader(expected),
		"meridio_2_lb_route_config_errors_total"))
}

func TestConfigErrors_Inc(t *testing.T) {
	c := NewConfigErrors(testGateway, testPrefix)
	c.Inc(ReasonRouteConfig)
	c.Inc(ReasonRouteConfig)
	c.Inc(ReasonVIPConfig)

	expected := `
# HELP meridio_2_lb_route_config_errors_total Number of failed data-plane config operations in the LB reconcile path, by reason (route_config: policy rule/route or target activation failure; vip_config: nftables VIP set update failure).
# TYPE meridio_2_lb_route_config_errors_total counter
meridio_2_lb_route_config_errors_total{gateway="gw-a",reason="route_config"} 2
meridio_2_lb_route_config_errors_total{gateway="gw-a",reason="vip_config"} 1
`
	require.NoError(t, testutil.CollectAndCompare(c.Collector(), strings.NewReader(expected),
		"meridio_2_lb_route_config_errors_total"))
}

// A nil *ConfigErrors (metrics disabled) is a valid no-op.
func TestConfigErrors_NilReceiverIsNoOp(t *testing.T) {
	var c *ConfigErrors
	require.NotPanics(t, func() { c.Inc(ReasonRouteConfig) })
}
