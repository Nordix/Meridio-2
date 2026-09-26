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
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func TestConfigErrors_PreInitializedToZero(t *testing.T) {
	c := NewConfigErrors(testPrefix)

	// All three reason series must be present at 0 from the start, so rate()/increase() have a
	// baseline and "no errors" is distinguishable from "series absent".
	expected := `
# HELP meridio_2_sidecar_config_errors_total Number of failed netlink operations in the sidecar reconcile path, by reason.
# TYPE meridio_2_sidecar_config_errors_total counter
meridio_2_sidecar_config_errors_total{reason="address"} 0
meridio_2_sidecar_config_errors_total{reason="link"} 0
meridio_2_sidecar_config_errors_total{reason="route"} 0
`
	require.NoError(t, testutil.CollectAndCompare(c.Collector(), strings.NewReader(expected)))
}

func TestConfigErrors_Inc(t *testing.T) {
	c := NewConfigErrors(testPrefix)

	c.Inc(ReasonAddress)
	c.Inc(ReasonAddress)
	c.Inc(ReasonRoute)

	expected := `
# HELP meridio_2_sidecar_config_errors_total Number of failed netlink operations in the sidecar reconcile path, by reason.
# TYPE meridio_2_sidecar_config_errors_total counter
meridio_2_sidecar_config_errors_total{reason="address"} 2
meridio_2_sidecar_config_errors_total{reason="link"} 0
meridio_2_sidecar_config_errors_total{reason="route"} 1
`
	require.NoError(t, testutil.CollectAndCompare(c.Collector(), strings.NewReader(expected)))
}

func TestConfigErrors_NilReceiverIncIsNoOp(t *testing.T) {
	var c *ConfigErrors // nil, as when metrics are disabled
	require.NotPanics(t, func() { c.Inc(ReasonLink) })
}
