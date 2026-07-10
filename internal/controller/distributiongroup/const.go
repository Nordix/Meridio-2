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
	// labelManagedBy identifies LoadBalancerEndpointSlices managed by this controller.
	labelManagedBy = "app.kubernetes.io/managed-by"
	managedByValue = "distributiongroup-controller.meridio-2.nordix.org"

	// labelDistributionGroup is a convenience label for kubectl filtering.
	// It MUST NOT be used for controller logic (use ownerReferences instead).
	// Value is the DG name, truncated to 63 chars if necessary (Kubernetes label value limit).
	labelDistributionGroup = "meridio-2.nordix.org/distribution-group"

	// Kubernetes resource kinds
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
