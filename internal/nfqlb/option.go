/*
Copyright (c) 2024 OpenInfra Foundation Europe

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

// WithFanout sets the queue fanout option.
func WithFanout(fanout bool) Option {
	return func(c *nfqlbConfig) {
		c.fanout = fanout
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

// ServiceOption applies a configuration option value to a nfqlb service.
type ServiceOption func(*nfqlbServiceConfig)

// WithQLength sets the queue length.
func WithMaxTargets(maxTargets int) ServiceOption {
	return func(c *nfqlbServiceConfig) {
		c.maxTargets = maxTargets
	}
}
