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

package prerequisites

import (
	"fmt"

	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// CheckGatewayAPI verifies that Gateway API CRDs are installed in the cluster.
// The caller supplies the REST config so this one-shot discovery call uses the
// same client settings as the manager.
func CheckGatewayAPI(cfg *rest.Config) error {
	discoveryClient, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return fmt.Errorf("failed to create discovery client: %w", err)
	}

	// Check for Gateway API group (direct API server call)
	apiGroupList, err := discoveryClient.ServerGroups()
	if err != nil {
		return fmt.Errorf("failed to list API groups: %w", err)
	}

	for _, group := range apiGroupList.Groups {
		if group.Name == gatewayapiv1.GroupName {
			return nil // Found Gateway API
		}
	}

	return fmt.Errorf("%s API group not found", gatewayapiv1.GroupName)
}
