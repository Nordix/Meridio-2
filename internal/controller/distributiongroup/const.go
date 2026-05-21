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

package distributiongroup

const (
	// EndpointSlice label keys
	labelManagedBy         = "endpointslice.kubernetes.io/managed-by"
	labelDistributionGroup = "meridio-2.nordix.org/distribution-group"
	labelNetworkSubnet     = "meridio-2.nordix.org/network-subnet"

	// managedByValue identifies EndpointSlices managed by this controller
	managedByValue = "distributiongroup-controller.meridio-2.nordix.org"

	// maxEndpointsPerSlice is the maximum number of endpoints per EndpointSlice
	// Matches Kubernetes default (100 endpoints per slice)
	maxEndpointsPerSlice = 100

	// maglevIDPrefix is the prefix for Maglev IDs stored in EndpointSlice zone field
	// Example: "maglev:5" means Maglev ID 5
	maglevIDPrefix = "maglev:"

	// Kubernetes resource kinds
	kindPod                  = "Pod"
	kindGateway              = "Gateway"
	kindGatewayConfiguration = "GatewayConfiguration"
	kindService              = "Service"
	kindDistributionGroup    = "DistributionGroup"

	// Status condition types
	conditionTypeReady            = "Ready"
	conditionTypeCapacityExceeded = "CapacityExceeded"

	// Status condition reasons
	reasonEndpointsAvailable     = "EndpointsAvailable"
	reasonNoEndpoints            = "NoEndpoints"
	reasonMultipleGateways       = "MultipleGateways"
	reasonMaglevCapacityExceeded = "MaglevCapacityExceeded"

	// Status condition messages
	messageEndpointsAvailable   = "EndpointSlices reconciled successfully"
	messageNoEndpointsAvailable = "No endpoints available"
	messageNoMatchingPods       = "No Pods match selector"
	messageNoReferencedGateways = "No Gateways reference this DistributionGroup (check parentRefs or L34Route backendRefs)"
	messageNoAcceptedGateways   = "No accepted Gateways found (Gateways may not exist or lack Accepted=True status condition)"
	messageNoNetworkContext     = "No network context available (check GatewayConfiguration internalSubnets)"
	messageMultipleGateways     = "DistributionGroup is referenced by multiple Gateways; only a single Gateway is supported"
)
