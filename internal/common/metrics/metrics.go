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

// Package metrics provides generic, component-agnostic helpers shared by all
// Meridio-2 binaries for wiring custom Prometheus metrics: metrics-prefix
// validation and a metrics-enabled check.
//
// This package intentionally has no knowledge of any specific metric, CRD, or
// data source. Metric definitions and prometheus.Collector implementations
// live in per-binary packages (e.g. internal/metrics for controller-manager),
// next to (or reading via read-only accessors from) the domain/data-source
// packages they instrument.
package metrics

import (
	"fmt"
	"regexp"
)

// DefaultPrefix is the default metrics name prefix used when --metrics-prefix
// is not overridden.
const DefaultPrefix = "meridio_2"

// maxPrefixLength is the maximum allowed length for a metrics prefix.
const maxPrefixLength = 10

// validPrefix matches lowercase letters, digits, and underscores, and must
// not start with an underscore or a digit (Prometheus metric name convention,
// restricted further here to keep the prefix itself unambiguous).
var validPrefix = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// ValidatePrefix validates a metrics name prefix. Valid prefixes contain only
// lowercase letters, digits, and underscores, must start with a lowercase
// letter, and must not exceed maxPrefixLength characters.
//
// The prefix is combined with a metric's base name using an underscore
// separator (e.g. prefix "meridio_2" + base name "gateway_count" produces
// "meridio_2_gateway_count"), so the separator itself must not be included by
// callers when constructing metric names.
func ValidatePrefix(prefix string) error {
	if prefix == "" {
		return fmt.Errorf("metrics prefix must not be empty")
	}
	if len(prefix) > maxPrefixLength {
		return fmt.Errorf("metrics prefix %q exceeds maximum length of %d characters", prefix, maxPrefixLength)
	}
	if !validPrefix.MatchString(prefix) {
		return fmt.Errorf(
			"metrics prefix %q is invalid: must start with a lowercase letter and contain only "+
				"lowercase letters, digits, and underscores", prefix)
	}
	return nil
}

// Enabled reports whether the metrics subsystem is enabled based on the
// configured --metrics-bind-address value. "0" is the sentinel used across
// all Meridio-2 binaries to mean "metrics endpoint disabled".
//
// Call sites that register prometheus.Collectors or increment push-style
// counters should check Enabled before doing so, keeping "metrics off" a true
// no-op: nothing is registered and no instrumentation call sites execute.
func Enabled(metricsAddr string) bool {
	return metricsAddr != "0"
}
