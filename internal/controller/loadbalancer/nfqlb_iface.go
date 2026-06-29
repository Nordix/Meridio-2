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

	"github.com/nordix/meridio-2/internal/nfqlb"
)

// nfqlbManager abstracts the top-level NFQLB operations for testability.
type nfqlbManager interface {
	AddInstance(ctx context.Context, name string, options ...nfqlb.InstanceOption) (nfqlbInstance, error)
	DeleteInstance(ctx context.Context, name string) error
}

// nfqlbInstance abstracts per-DistributionGroup NFQLB operations for testability.
type nfqlbInstance interface {
	AddFlow(ctx context.Context, flow nfqlb.Flow) error
	DeleteFlow(ctx context.Context, flow nfqlb.Flow) error
	AddTarget(ctx context.Context, ips []string, identifier int) error
	DeleteTarget(ctx context.Context, identifier int) error
}

// NFQLBManagerAdapter wraps *nfqlb.NFQueueLoadBalancer to implement nfqlbManager.
type NFQLBManagerAdapter struct {
	NFQLB *nfqlb.NFQueueLoadBalancer
}

func (a *NFQLBManagerAdapter) AddInstance(ctx context.Context, name string, options ...nfqlb.InstanceOption) (nfqlbInstance, error) {
	return a.NFQLB.AddInstance(ctx, name, options...)
}

func (a *NFQLBManagerAdapter) DeleteInstance(ctx context.Context, name string) error {
	return a.NFQLB.DeleteInstance(ctx, name)
}
