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

	meridio2v1alpha1 "github.com/nordix/meridio-2/api/v1alpha1"
	"github.com/nordix/meridio-2/internal/nfqlb"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// reconcileNFQLBInstance creates or retrieves the NFQLB service for a DistributionGroup.
func (c *Controller) reconcileNFQLBInstance(ctx context.Context, distGroup *meridio2v1alpha1.DistributionGroup) error {
	logr := log.FromContext(ctx)

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.instances == nil {
		c.instances = make(map[string]nfqlbInstance)
	}
	if c.targets == nil {
		c.targets = make(map[string]map[int][]string)
	}

	// Check if service already exists
	if _, exists := c.instances[distGroup.Name]; exists {
		return nil
	}

	// Get maxEndpoints from spec
	n := meridio2v1alpha1.DefaultMaglevMaxEndpoints
	if distGroup.Spec.Maglev != nil && distGroup.Spec.Maglev.MaxEndpoints > 0 {
		n = distGroup.Spec.Maglev.MaxEndpoints
	}

	// Create NFQLB service (offset is managed internally by NFQueueLoadBalancer)
	service, err := c.NFQLB.AddInstance(ctx, distGroup.Name,
		nfqlb.WithMaxTargets(int(n)),
	)
	if err != nil {
		return err
	}

	c.instances[distGroup.Name] = service
	c.targets[distGroup.Name] = make(map[int][]string)

	logr.Info("Created NFQLB service", "distGroup", distGroup.Name, "maxTargets", n)

	return nil
}
