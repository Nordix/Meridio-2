/*
Copyright (c) 2024-2026 OpenInfra Foundation Europe

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

package nfqlb

// Option applies a configuration option value to nfqlb.
type Option func(*nfqlbConfig)

// WithQueue specifies the queue(s) nfqlb will use.
func WithQueue(queue string) Option {
	return func(c *nfqlbConfig) {
		c.queue = queue
	}
}

// WithQLength sets the queue length.
func WithQLength(qlength uint) Option {
	return func(c *nfqlbConfig) {
		c.qlength = qlength
	}
}

// WithStartingOffset sets the starting offset for the fowarding mark
// to avoid collisions with existing routing tables.
func WithStartingOffset(startingOffset int) Option {
	return func(c *nfqlbConfig) {
		c.startingOffset = startingOffset
	}
}

// WithNFQLBPath sets the path to the nfqlb binary.
func WithNFQLBPath(nfqlbPath string) Option {
	return func(c *nfqlbConfig) {
		c.nfqlbPath = nfqlbPath
	}
}

// InstanceOption applies a configuration option value to a nfqlb instance.
type InstanceOption func(*nfqlbInstanceConfig)

// WithMaxTargets sets the maximum number of targets for the instance.
func WithMaxTargets(maxTargets int) InstanceOption {
	return func(c *nfqlbInstanceConfig) {
		c.maxTargets = maxTargets
	}
}
