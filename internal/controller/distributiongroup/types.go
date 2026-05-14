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

import (
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// podWithNetworkIP pairs a Pod with its scraped secondary IP for a specific network context
type podWithNetworkIP struct {
	pod corev1.Pod
	ip  string
}

// gatewayNetworkContext groups network contexts for a single Gateway.
// Used to scope Maglev ID allocation per Gateway.
type gatewayNetworkContext struct {
	// gateway is the namespaced name of the Gateway object.
	gateway client.ObjectKey
	// networks maps normalized CIDR → attachment type for this Gateway.
	networks map[string]string
}

// maglevCapacityInfo tracks Maglev capacity issues per network
type maglevCapacityInfo struct {
	networkIssues map[string]struct {
		excluded int32 // Pods that couldn't get IDs
		total    int32 // Total Pods that tried to join
	}
}
