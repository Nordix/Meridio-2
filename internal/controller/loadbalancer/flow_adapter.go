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
	meridio2v1alpha1 "github.com/nordix/meridio-2/api/v1alpha1"
	"github.com/nordix/meridio-2/internal/nfqlb"
)

// l34RouteFlow adapts an L34Route to the nfqlb.Flow interface.
type l34RouteFlow struct {
	name  string
	route *meridio2v1alpha1.L34Route
}

var _ nfqlb.Flow = (*l34RouteFlow)(nil)

func newL34RouteFlow(name string, route *meridio2v1alpha1.L34Route) *l34RouteFlow {
	return &l34RouteFlow{name: name, route: route}
}

func (f *l34RouteFlow) GetName() string                    { return f.name }
func (f *l34RouteFlow) GetSourceCIDRs() []string           { return f.route.Spec.SourceCIDRs }
func (f *l34RouteFlow) GetDestinationCIDRs() []string      { return f.route.Spec.DestinationCIDRs }
func (f *l34RouteFlow) GetSourcePortRanges() []string      { return f.route.Spec.SourcePorts }
func (f *l34RouteFlow) GetDestinationPortRanges() []string { return f.route.Spec.DestinationPorts }
func (f *l34RouteFlow) GetByteMatches() []string           { return f.route.Spec.ByteMatches }
func (f *l34RouteFlow) GetPriority() int32                 { return f.route.Spec.Priority }

func (f *l34RouteFlow) GetProtocols() []string {
	protocols := make([]string, len(f.route.Spec.Protocols))
	for i, p := range f.route.Spec.Protocols {
		protocols[i] = string(p)
	}
	return protocols
}

// nameOnlyFlow implements nfqlb.Flow with only a name (for deletion by name).
type nameOnlyFlow struct{ name string }

var _ nfqlb.Flow = (*nameOnlyFlow)(nil)

func (f *nameOnlyFlow) GetName() string                    { return f.name }
func (f *nameOnlyFlow) GetSourceCIDRs() []string           { return nil }
func (f *nameOnlyFlow) GetDestinationCIDRs() []string      { return nil }
func (f *nameOnlyFlow) GetSourcePortRanges() []string      { return nil }
func (f *nameOnlyFlow) GetDestinationPortRanges() []string { return nil }
func (f *nameOnlyFlow) GetProtocols() []string             { return nil }
func (f *nameOnlyFlow) GetPriority() int32                 { return 0 }
func (f *nameOnlyFlow) GetByteMatches() []string           { return nil }
