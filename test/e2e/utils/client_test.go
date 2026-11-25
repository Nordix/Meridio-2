//go:build e2e
// +build e2e

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

package utils

import (
	"slices"
	"strings"
	"testing"
)

func TestParseIPv6OutputQueuedPackets(t *testing.T) {
	tests := []struct {
		name    string
		out     string
		want    uint64
		wantErr bool
	}{
		{
			name: "rule restricted by ICMPv6 type",
			out: `table inet meridio-lb {
	chain output {
		type filter hook output priority filter; policy accept;
		meta l4proto icmp ip daddr @ipv4-vips counter packets 7 bytes 588 queue to 0-3
		ip6 daddr @ipv6-vips icmpv6 type { destination-unreachable, packet-too-big, time-exceeded, parameter-problem, echo-request, echo-reply } counter packets 0 bytes 0 queue to 0-3
	}
}`,
			want: 0,
		},
		{
			name: "unfiltered rule",
			out: `table inet meridio-lb {
	chain output {
		type filter hook output priority filter; policy accept;
		meta l4proto icmp ip daddr @ipv4-vips counter packets 0 bytes 0 queue to 0-3
		meta l4proto ipv6-icmp ip6 daddr @ipv6-vips counter packets 3 bytes 216 queue to 0-3
	}
}`,
			want: 3,
		},
		{
			name:    "rule missing",
			out:     "table inet meridio-lb {\n\tchain output {\n\t}\n}",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseIPv6OutputQueuedPackets(tt.out)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("got %d, want %d", got, tt.want)
			}
		})
	}
}

func TestParseAppliedIPv6NextHops(t *testing.T) {
	const applied = `{
  "metadata": {"name": "target-c6-0-abc", "generation": 2},
  "spec": {"gateways": [{"name": "gw-c6", "domains": [
    {"name": "d4", "ipFamily": "IPv4", "network": {"subnet": "169.111.0.0/24"}, "nextHops": ["169.111.0.1"]},
    {"name": "d6", "ipFamily": "IPv6", "network": {"subnet": "fd00:cafe:190::/64"}, "nextHops": ["fd00:cafe:190::1", "fd00:cafe:190::2"]}
  ]}]},
  "status": {"conditions": [{"type": "Ready", "status": "True", "observedGeneration": 2, "reason": "Configured", "message": "", "lastTransitionTime": "2026-01-01T00:00:00Z"}]}
}`
	tests := []struct {
		name    string
		in      string
		want    []string
		wantErr bool
	}{
		{name: "applied", in: applied, want: []string{"fd00:cafe:190::1", "fd00:cafe:190::2"}},
		{name: "stale generation", in: strings.Replace(applied, `"generation": 2`, `"generation": 3`, 1), wantErr: true},
		{name: "not ready", in: strings.Replace(applied, `"status": "True"`, `"status": "False"`, 1), wantErr: true},
		{name: "no conditions", in: `{"metadata": {"name": "p", "generation": 1}}`, wantErr: true},
		{name: "no IPv6 next hops", in: strings.Replace(applied, `"nextHops": ["fd00:cafe:190::1", "fd00:cafe:190::2"]`, `"nextHops": []`, 1), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseAppliedIPv6NextHops([]byte(tt.in))
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}
