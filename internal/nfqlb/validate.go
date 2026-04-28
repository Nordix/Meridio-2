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

import (
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
)

// validNameRe matches alphanumeric, dash, underscore, and dot.
var validNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

// validateName validates an instance or flow name for safe use in CLI arguments.
func validateName(name string) error {
	if name == "" {
		return fmt.Errorf("name must not be empty")
	}
	if strings.HasPrefix(name, "-") {
		return fmt.Errorf("name must not start with '-': %q", name)
	}
	if !validNameRe.MatchString(name) {
		return fmt.Errorf("name contains invalid character: %q (allowed: alphanumeric, dash, underscore, dot)", name)
	}
	return nil
}

// validateCIDRs validates a slice of CIDR strings.
func validateCIDRs(cidrs []string) error {
	for _, cidr := range cidrs {
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			return fmt.Errorf("invalid CIDR %q: %w", cidr, err)
		}
	}
	return nil
}

// validatePortRanges validates port range strings (e.g., "80", "1024-65535").
func validatePortRanges(ports []string) error {
	for _, p := range ports {
		parts := strings.SplitN(p, "-", 2)
		start, err := strconv.Atoi(parts[0])
		if err != nil {
			return fmt.Errorf("invalid port range %q: %w", p, err)
		}
		if start < 0 || start > 65535 {
			return fmt.Errorf("port out of range in %q: %d", p, start)
		}
		if len(parts) == 2 {
			end, err := strconv.Atoi(parts[1])
			if err != nil {
				return fmt.Errorf("invalid port range %q: %w", p, err)
			}
			if end < 0 || end > 65535 {
				return fmt.Errorf("port out of range in %q: %d", p, end)
			}
			if start > end {
				return fmt.Errorf("invalid port range %q: start must be <= end", p)
			}
		}
	}
	return nil
}

// validateProtocols validates protocol strings.
func validateProtocols(protocols []string) error {
	for _, p := range protocols {
		switch strings.ToLower(p) {
		case "tcp", "udp", "sctp":
			// valid
		default:
			return fmt.Errorf("unsupported protocol %q (allowed: tcp, udp, sctp)", p)
		}
	}
	return nil
}

// validateFlow validates all fields of a Flow before passing to nfqlb CLI.
func validateFlow(flow Flow) error {
	if err := validateName(flow.GetName()); err != nil {
		return fmt.Errorf("flow name: %w", err)
	}
	if err := validateCIDRs(flow.GetDestinationCIDRs()); err != nil {
		return fmt.Errorf("destination CIDRs: %w", err)
	}
	if err := validateCIDRs(flow.GetSourceCIDRs()); err != nil {
		return fmt.Errorf("source CIDRs: %w", err)
	}
	if err := validatePortRanges(flow.GetDestinationPortRanges()); err != nil {
		return fmt.Errorf("destination ports: %w", err)
	}
	if err := validatePortRanges(flow.GetSourcePortRanges()); err != nil {
		return fmt.Errorf("source ports: %w", err)
	}
	if err := validateProtocols(flow.GetProtocols()); err != nil {
		return err
	}
	return nil
}
