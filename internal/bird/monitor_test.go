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

package bird

import (
	"context"
	"testing"
	"time"

	"k8s.io/utils/ptr"
)

func TestParseProtocolOutput(t *testing.T) {
	tests := []struct {
		name     string
		output   string
		expected []ProtocolStatus
	}{
		{
			name: "single protocol established",
			output: `BIRD 3.2.0 ready.
Name       Proto      Table      State  Since         Info
NBR-gw1    BGP        ---        up     2026-03-02    Established`,
			expected: []ProtocolStatus{
				{
					Name:  "NBR-gw1",
					State: ProtocolStateUp,
					Info:  "Established",
				},
			},
		},
		{
			name: "dual-stack sample",
			output: `BIRD 3.1.5 ready.
Name       Proto      Table      State  Since         Info
VIP4       Static     master4    up     10:04:11.787
VIP6       Static     master6    up     10:04:11.787
NBR-gatewayrouter-sample BGP        ---        up     10:04:13.527  Established
  Created:            10:04:11.787
  BGP state:          Established
NBR-gatewayrouter-sample-v6 BGP        ---        up     10:04:12.499  Established
  Created:            10:04:11.787
  BGP state:          Established
device1    Device     ---        up     10:04:11.552
kernel1    Kernel     master4    up     10:04:11.552
kernel2    Kernel     master6    up     10:04:11.552
bfd1       BFD        ---        up     10:04:11.552`,
			expected: []ProtocolStatus{
				{
					Name:  "NBR-gatewayrouter-sample",
					State: ProtocolStateUp,
					Info:  "Established",
				},
				{
					Name:  "NBR-gatewayrouter-sample-v6",
					State: ProtocolStateUp,
					Info:  "Established",
				},
			},
		},
		{
			name: "multiple protocols mixed state",
			output: `BIRD 3.2.0 ready.
Name       Proto      Table      State  Since         Info
NBR-gw1    BGP        ---        up     2026-03-02    Established
NBR-gw2    BGP        ---        start  2026-03-02    Connect       Socket: Connection refused`,
			expected: []ProtocolStatus{
				{
					Name:  "NBR-gw1",
					State: ProtocolStateUp,
					Info:  "Established",
				},
				{
					Name:  "NBR-gw2",
					State: ProtocolStateStart,
					Info:  "Connect Socket: Connection refused",
				},
			},
		},
		{
			name: "all protocols up",
			output: `BIRD 3.2.0 ready.
Name       Proto      Table      State  Since         Info
NBR-192-168-1-1 BGP   ---        up     14:23:45      Established
NBR-192-168-1-2 BGP   ---        up     14:23:46      Established`,
			expected: []ProtocolStatus{
				{
					Name:  "NBR-192-168-1-1",
					State: ProtocolStateUp,
					Info:  "Established",
				},
				{
					Name:  "NBR-192-168-1-2",
					State: ProtocolStateUp,
					Info:  "Established",
				},
			},
		},
		{
			name:     "no protocols configured",
			output:   `BIRD 3.2.0 ready.`,
			expected: []ProtocolStatus{},
		},
		{
			name: "non-NBR protocols ignored",
			output: `BIRD 3.2.0 ready.
Name       Proto      Table      State  Since         Info
kernel1    Kernel     master     up     12:36:03
device1    Device     master     up     12:36:03
direct1    Direct     master     up     12:36:03
NBR-peer1  BGP        ---        up     12:36:08      Established`,
			expected: []ProtocolStatus{
				{
					Name:  "NBR-peer1",
					State: ProtocolStateUp,
					Info:  "Established",
				},
			},
		},
		{
			name: "protocol down state",
			output: `BIRD 3.2.0 ready.
Name       Proto      Table      State  Since         Info
NBR-peer1  BGP        ---        down   12:43:38      Connection closed`,
			expected: []ProtocolStatus{
				{
					Name:  "NBR-peer1",
					State: ProtocolStateDown,
					Info:  "Connection closed",
				},
			},
		},
		{
			name: "dual stack gateways",
			output: `BIRD 3.2.0 ready.
Name       Proto      Table      State  Since         Info
NBR-gw-ipv4 BGP       ---        up     2026-03-02    Established
NBR-gw-ipv6 BGP       ---        up     2026-03-02    Established`,
			expected: []ProtocolStatus{
				{
					Name:  "NBR-gw-ipv4",
					State: ProtocolStateUp,
					Info:  "Established",
				},
				{
					Name:  "NBR-gw-ipv6",
					State: ProtocolStateUp,
					Info:  "Established",
				},
			},
		},
		{
			// Captured live from a router pod running the from-source BIRD 3.3.2
			// build (ipv4-simple suite). The summary-line column layout is
			// unchanged from 3.1.x/3.2.x, so the field-position parser still holds.
			name: "3.3.2 established session",
			output: `BIRD 3.3.2 ready.
Name       Proto      Table      State  Since         Info
NBR-gw-m1-router-v4 BGP        ---        up     14:14:42.576  Established`,
			expected: []ProtocolStatus{
				{
					Name:  "NBR-gw-m1-router-v4",
					State: ProtocolStateUp,
					Info:  "Established",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseShowProtocolsOutput(tt.output)
			if len(result) != len(tt.expected) {
				t.Fatalf("expected %d protocols, got %d", len(tt.expected), len(result))
			}
			for i, exp := range tt.expected {
				if result[i].Name != exp.Name {
					t.Errorf("protocol[%d].Name = %s, want %s", i, result[i].Name, exp.Name)
				}
				if result[i].State != exp.State {
					t.Errorf("protocol[%d].State = %s, want %s", i, result[i].State, exp.State)
				}
				if result[i].Info != exp.Info {
					t.Errorf("protocol[%d].Info = %s, want %s", i, result[i].Info, exp.Info)
				}
				if result[i].IsEstablished() != exp.IsEstablished() {
					t.Errorf("protocol[%d].IsEstablished() = %v, want %v", i, result[i].IsEstablished(), exp.IsEstablished())
				}
			}
		})
	}
}

// TestParseProtocolDetail covers the BGP detail-block parsing: route-count
// summing across channels, the Since epoch parse, and that non-BGP/non-numeric
// cases leave the optional fields nil.
func TestParseProtocolDetail(t *testing.T) {
	tests := []struct {
		name            string
		output          string
		wantImported    *int
		wantExported    *int
		wantEstablished *int64
	}{
		{
			// Live BIRD 3.2.3 capture: one active ipv4 channel plus a disabled
			// ipv6 channel (Input/Output filter REJECT, Routes: 0,0). Summing
			// yields the active channel's counts. Since is the default
			// human-readable format, so EstablishedUnix stays nil.
			name: "dual channel, active ipv4 + disabled ipv6",
			output: `BIRD 3.2.3 ready.
Name       Proto      Table      State  Since         Info
NBR-gw-a1-router-v4 BGP        ---        up     08:25:11.859  Established
  BGP state:          Established
  Channel ipv4
    Input filter:   gateway_routes
    Output filter:  announced_routes
    Routes:         1 imported, 1 exported, 1 preferred
  Channel ipv6
    Input filter:   REJECT
    Output filter:  REJECT
    Routes:         0 imported, 0 exported, 0 preferred`,
			wantImported:    ptr.To(1),
			wantExported:    ptr.To(1),
			wantEstablished: nil,
		},
		{
			// With `timeformat protocol "%s";` the Since column is a Unix epoch.
			name: "numeric Since parsed as epoch",
			output: `BIRD 3.2.3 ready.
Name       Proto      Table      State  Since         Info
NBR-gw1    BGP        ---        up     1759518307  Established
  Channel ipv4
    Input filter:   gateway_routes
    Output filter:  announced_routes
    Routes:         3 imported, 2 exported, 3 preferred`,
			wantImported:    ptr.To(3),
			wantExported:    ptr.To(2),
			wantEstablished: ptr.To(int64(1759518307)),
		},
		{
			// A BGP summary line with no detail block (e.g. session not up):
			// no Routes: line, so route counts stay nil.
			name: "no detail block leaves route counts nil",
			output: `BIRD 3.2.3 ready.
Name       Proto      Table      State  Since         Info
NBR-gw1    BGP        ---        start  1759518307  Connect`,
			wantImported:    nil,
			wantExported:    nil,
			wantEstablished: ptr.To(int64(1759518307)),
		},
		{
			// Synthetic: a Static protocol block carrying a Routes: line (real
			// Static blocks for our routers don't). Detail parsing is gated on
			// Proto=="BGP", so the route fields and epoch must stay nil here
			// even though a Routes: line and a numeric Since are present.
			name: "static protocol detail is not parsed",
			output: `BIRD 3.2.3 ready.
Name       Proto      Table      State  Since         Info
NBR-static1 Static     ---        up     1759518307
  Channel ipv4
    Routes:         7 imported, 7 exported, 7 preferred`,
			wantImported:    nil,
			wantExported:    nil,
			wantEstablished: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseShowProtocolsOutput(tt.output)
			if len(result) != 1 {
				t.Fatalf("expected 1 protocol, got %d", len(result))
			}
			p := result[0]
			assertIntPtr(t, "RoutesImported", p.RoutesImported, tt.wantImported)
			assertIntPtr(t, "RoutesExported", p.RoutesExported, tt.wantExported)
			assertInt64Ptr(t, "EstablishedUnix", p.EstablishedUnix, tt.wantEstablished)
		})
	}
}

// TestParseProtocolDetail_BlockBoundaries verifies that a BGP protocol's detail
// block is consumed up to the correct boundary and parsing resumes at the right
// line — route counts are attributed to the right protocol and no protocol
// after a detail block is swallowed or re-parsed. Covers the three block
// terminators: a following top-level entry, a blank-line separator, and EOF.
func TestParseProtocolDetail_BlockBoundaries(t *testing.T) {
	t.Run("single NBR among other protocols", func(t *testing.T) {
		// BIRD 3.2.3 `show protocols all` (no name filter): the NBR-gw BGP block
		// (two channels) is surrounded by VIP4/device/kernel/bfd blocks, each
		// with their own Channel/Routes lines. Only the NBR- BGP protocol must
		// be returned, with the active channel's counts — proving foreign blocks
		// are skipped and parsing resumes correctly after the BGP detail block.
		output := `BIRD 3.2.3 ready.
Name       Proto      Table      State  Since         Info
VIP4       Static     master4    up     06:34:36.268
  Channel ipv4
    Routes:         1 imported, 0 exported, 1 preferred

NBR-gw-a1-router-v4 BGP        ---        up     08:25:11.859  Established
  BGP state:          Established
  Channel ipv4
    Input filter:   gateway_routes
    Output filter:  announced_routes
    Routes:         1 imported, 1 exported, 1 preferred
  Channel ipv6
    Input filter:   REJECT
    Output filter:  REJECT
    Routes:         0 imported, 0 exported, 0 preferred

device1    Device     ---        up     06:34:36.059

kernel1    Kernel     master4    up     06:34:36.059
  Channel ipv4
    Routes:         0 imported, 1 exported, 0 preferred

bfd1       BFD        ---        up     06:34:36.059`

		result := parseShowProtocolsOutput(output)
		if len(result) != 1 {
			t.Fatalf("expected 1 NBR protocol, got %d: %+v", len(result), result)
		}
		if result[0].Name != "NBR-gw-a1-router-v4" {
			t.Fatalf("Name = %s, want NBR-gw-a1-router-v4", result[0].Name)
		}
		assertIntPtr(t, "RoutesImported", result[0].RoutesImported, ptr.To(1))
		assertIntPtr(t, "RoutesExported", result[0].RoutesExported, ptr.To(1))
	})

	t.Run("two adjacent BGP blocks, counts not bleeding across", func(t *testing.T) {
		// Blank-line separated BGP blocks: each protocol's counts must be its
		// own, proving the first block ends before the second's summary line.
		output := `BIRD 3.2.3 ready.
Name       Proto      Table      State  Since         Info
NBR-gw1    BGP        ---        up     1759518307  Established
  Channel ipv4
    Routes:         2 imported, 3 exported, 2 preferred

NBR-gw2    BGP        ---        up     1759518400  Established
  Channel ipv4
    Routes:         5 imported, 7 exported, 5 preferred`

		result := parseShowProtocolsOutput(output)
		if len(result) != 2 {
			t.Fatalf("expected 2 protocols, got %d", len(result))
		}
		assertIntPtr(t, "gw1 RoutesImported", result[0].RoutesImported, ptr.To(2))
		assertIntPtr(t, "gw1 RoutesExported", result[0].RoutesExported, ptr.To(3))
		assertIntPtr(t, "gw2 RoutesImported", result[1].RoutesImported, ptr.To(5))
		assertIntPtr(t, "gw2 RoutesExported", result[1].RoutesExported, ptr.To(7))
	})

	t.Run("adjacent summary lines with no detail blocks", func(t *testing.T) {
		// Two summary lines with no detail blocks and no blank line between
		// them: the first protocol's block must end at the second summary line,
		// so both protocols are returned.
		output := `BIRD 3.2.3 ready.
Name       Proto      Table      State  Since         Info
NBR-gw1    BGP        ---        start  1759518307  Connect
NBR-gw2    BGP        ---        up     1759518400  Established`

		result := parseShowProtocolsOutput(output)
		if len(result) != 2 {
			t.Fatalf("expected 2 protocols, got %d", len(result))
		}
		if result[0].Name != "NBR-gw1" || result[1].Name != "NBR-gw2" {
			t.Fatalf("names = [%s, %s], want [NBR-gw1, NBR-gw2]", result[0].Name, result[1].Name)
		}
	})

	t.Run("BGP detail block at EOF", func(t *testing.T) {
		// The detail block runs to EOF with no trailing blank line: counts must
		// still be parsed.
		output := `BIRD 3.2.3 ready.
Name       Proto      Table      State  Since         Info
NBR-gw1    BGP        ---        up     1759518307  Established
  Channel ipv4
    Routes:         9 imported, 4 exported, 9 preferred`

		result := parseShowProtocolsOutput(output)
		if len(result) != 1 {
			t.Fatalf("expected 1 protocol, got %d", len(result))
		}
		assertIntPtr(t, "RoutesImported", result[0].RoutesImported, ptr.To(9))
		assertIntPtr(t, "RoutesExported", result[0].RoutesExported, ptr.To(4))
	})
}

func assertIntPtr(t *testing.T, field string, got, want *int) {
	t.Helper()
	switch {
	case want == nil && got != nil:
		t.Errorf("%s = %d, want nil", field, *got)
	case want != nil && got == nil:
		t.Errorf("%s = nil, want %d", field, *want)
	case want != nil && got != nil && *got != *want:
		t.Errorf("%s = %d, want %d", field, *got, *want)
	}
}

func assertInt64Ptr(t *testing.T, field string, got, want *int64) {
	t.Helper()
	switch {
	case want == nil && got != nil:
		t.Errorf("%s = %d, want nil", field, *got)
	case want != nil && got == nil:
		t.Errorf("%s = nil, want %d", field, *want)
	case want != nil && got != nil && *got != *want:
		t.Errorf("%s = %d, want %d", field, *got, *want)
	}
}

func TestParseAndFormatStatus(t *testing.T) {
	tests := []struct {
		name          string
		birdcOutput   string
		expectedUp    int
		expectedTotal int
	}{
		{
			name: "dual-stack both up",
			birdcOutput: `BIRD 3.1.5 ready.
Name       Proto      Table      State  Since         Info
NBR-gatewayrouter-sample BGP        ---        up     10:04:13.527  Established
NBR-gatewayrouter-sample-v6 BGP        ---        up     10:04:12.499  Established`,
			expectedUp: 2, expectedTotal: 2,
		},
		{
			name: "dual-stack one up",
			birdcOutput: `BIRD 3.1.5 ready.
Name       Proto      Table      State  Since         Info
NBR-gatewayrouter-sample BGP        ---        start  10:04:13.527  Connect
NBR-gatewayrouter-sample-v6 BGP        ---        up     10:04:12.499  Established`,
			expectedUp: 1, expectedTotal: 2,
		},
		{
			name: "no protocols",
			birdcOutput: `BIRD 3.1.5 ready.
Name       Proto      Table      State  Since         Info`,
			expectedUp: 0, expectedTotal: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			protocols := parseShowProtocolsOutput(tt.birdcOutput)
			if len(protocols) != tt.expectedTotal {
				t.Errorf("protocol count = %d, want %d", len(protocols), tt.expectedTotal)
			}
			upCount := 0
			for _, p := range protocols {
				if p.IsEstablished() {
					upCount++
				}
			}
			if upCount != tt.expectedUp {
				t.Errorf("established count = %d, want %d", upCount, tt.expectedUp)
			}
		})
	}
}

func TestProtocolUpCountChanges(t *testing.T) {
	// Simulate the sequence: 0 protocols → 1/1 up → 1/2 up → 2/2 up
	// This verifies that up-count changes at each step
	outputs := []struct {
		birdcOutput   string
		expectedCount int
		expectedTotal int
	}{
		{
			birdcOutput: `BIRD 3.1.5 ready.
Name       Proto      Table      State  Since         Info`,
			expectedCount: 0, expectedTotal: 0,
		},
		{
			birdcOutput: `BIRD 3.1.5 ready.
Name       Proto      Table      State  Since         Info
NBR-gatewayrouter-sample-v6 BGP        ---        up     10:04:12.499  Established`,
			expectedCount: 1, expectedTotal: 1,
		},
		{
			birdcOutput: `BIRD 3.1.5 ready.
Name       Proto      Table      State  Since         Info
NBR-gatewayrouter-sample BGP        ---        start  10:04:13.527  Connect
NBR-gatewayrouter-sample-v6 BGP        ---        up     10:04:12.499  Established`,
			expectedCount: 1, expectedTotal: 2,
		},
		{
			birdcOutput: `BIRD 3.1.5 ready.
Name       Proto      Table      State  Since         Info
NBR-gatewayrouter-sample BGP        ---        up     10:04:13.527  Established
NBR-gatewayrouter-sample-v6 BGP        ---        up     10:04:12.499  Established`,
			expectedCount: 2, expectedTotal: 2,
		},
	}

	for i, step := range outputs {
		protocols := parseShowProtocolsOutput(step.birdcOutput)

		if len(protocols) != step.expectedTotal {
			t.Errorf("Step %d: protocol count = %d, want %d", i, len(protocols), step.expectedTotal)
		}

		upCount := 0
		for _, p := range protocols {
			if p.IsEstablished() {
				upCount++
			}
		}

		if upCount != step.expectedCount {
			t.Errorf("Step %d: upCount = %d, want %d", i, upCount, step.expectedCount)
		}
	}
}

// TestGetProtocolStatuses_InvalidSocket verifies that a birdc failure (e.g. no
// BIRD instance listening on the socket) is surfaced as an error rather than
// silently swallowed, since GetProtocolStatuses is now called by callers (e.g.
// an independent metrics collector) that need to distinguish "fetch failed"
// from "no protocols configured".
func TestGetProtocolStatuses_InvalidSocket(t *testing.T) {
	_, err := GetProtocolStatuses(context.Background(), "/nonexistent/bird.ctl")
	if err == nil {
		t.Fatal("expected error for nonexistent socket, got nil")
	}
}

// TestGetBfdSessions_InvalidSocket mirrors TestGetProtocolStatuses_InvalidSocket
// for the BFD sessions query.
func TestGetBfdSessions_InvalidSocket(t *testing.T) {
	_, err := GetBfdSessions(context.Background(), "/nonexistent/bird.ctl")
	if err == nil {
		t.Fatal("expected error for nonexistent socket, got nil")
	}
}

// TestGetProtocolStatuses_ContextCanceled verifies that a canceled context is
// honored (the exec is not even attempted to run to completion), confirming
// callers can bound these calls with a timeout independent of any Bird
// instance's own lifecycle.
func TestGetProtocolStatuses_ContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := GetProtocolStatuses(ctx, "/nonexistent/bird.ctl")
	if err == nil {
		t.Fatal("expected error for canceled context, got nil")
	}
}

func TestMonitorChannelClosure(t *testing.T) {
	b := &Bird{
		running: false,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	statusCh, err := b.Monitor(ctx, 10*time.Millisecond)
	if err != nil {
		t.Fatalf("Monitor() error = %v", err)
	}

	cancel()

	select {
	case _, ok := <-statusCh:
		if ok {
			t.Error("expected channel to be closed")
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("channel did not close in time")
	}
}

func TestMonitorEmitsStatus(t *testing.T) {
	b := &Bird{
		running: false,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	statusCh, err := b.Monitor(ctx, 10*time.Millisecond)
	if err != nil {
		t.Fatalf("Monitor() error = %v", err)
	}

	select {
	case <-statusCh:
		// Successfully received status
	case <-time.After(50 * time.Millisecond):
		t.Error("did not receive status in time")
	}
}

func TestParseBfdOutput(t *testing.T) {
	t.Run("standard output with multiple sessions", func(t *testing.T) {
		output := `BIRD 3.2.0 ready.
IP address                Interface  State      Since         Interval  Timeout
169.254.110.1             vlan11     Up         14:23:45.100    0.300    0.900
169.254.111.2             vlan12     Down       14:24:01.200    1.000    0.000`

		sessions := parseShowBfdSessionsOutput(output)
		if len(sessions) != 2 {
			t.Fatalf("expected 2 sessions, got %d", len(sessions))
		}
		if sessions[0].IP != "169.254.110.1" || sessions[0].Interface != "vlan11" || sessions[0].State != "Up" {
			t.Errorf("session[0] = %+v", sessions[0])
		}
		if sessions[1].IP != "169.254.111.2" || sessions[1].Interface != "vlan12" || sessions[1].State != "Down" {
			t.Errorf("session[1] = %+v", sessions[1])
		}
	})

	t.Run("empty output", func(t *testing.T) {
		sessions := parseShowBfdSessionsOutput("")
		if len(sessions) != 0 {
			t.Errorf("expected 0 sessions, got %d", len(sessions))
		}
	})

	t.Run("headers only no sessions", func(t *testing.T) {
		output := `BIRD 3.2.0 ready.
IP address                Interface  State      Since         Interval  Timeout`

		sessions := parseShowBfdSessionsOutput(output)
		if len(sessions) != 0 {
			t.Errorf("expected 0 sessions, got %d", len(sessions))
		}
	})

	t.Run("IPv6 sessions", func(t *testing.T) {
		output := `BIRD 3.2.0 ready.
IP address                Interface  State      Since         Interval  Timeout
fd00:cafe:10::1           vlan11     Up         14:23:45.100    0.300    0.900
fd00:cafe:10::2           vlan11     Down       14:24:01.200    1.000    0.000`

		sessions := parseShowBfdSessionsOutput(output)
		if len(sessions) != 2 {
			t.Fatalf("expected 2 sessions, got %d", len(sessions))
		}
		if sessions[0].IP != "fd00:cafe:10::1" || sessions[0].State != "Up" {
			t.Errorf("session[0] = %+v", sessions[0])
		}
		if sessions[1].IP != "fd00:cafe:10::2" || sessions[1].State != "Down" {
			t.Errorf("session[1] = %+v", sessions[1])
		}
	})

	t.Run("malformed lines skipped", func(t *testing.T) {
		output := `BIRD 3.2.0 ready.
IP address                Interface  State      Since         Interval  Timeout
169.254.110.1             vlan11     Up         14:23:45.100    0.300    0.900
short
169.254.111.2             vlan12     Down       14:24:01.200    1.000    0.000`

		sessions := parseShowBfdSessionsOutput(output)
		if len(sessions) != 2 {
			t.Fatalf("expected 2 sessions (malformed skipped), got %d", len(sessions))
		}
	})

	// BIRD 3.3.2 prints a per-instance header line ("bfd1:") before the session
	// table. Captured live from a router pod running the from-source 3.3.2 build.
	// The single-field "bfd1:" line must be skipped (parser requires >= 3 fields).
	t.Run("3.3.2 instance header line skipped", func(t *testing.T) {
		output := `BIRD 3.3.2 ready.
bfd1:
IP address                Interface  State      Since         Interval  Timeout
169.254.40.150            vlan-700   Up         14:14:43.192    0.300    1.500`

		sessions := parseShowBfdSessionsOutput(output)
		if len(sessions) != 1 {
			t.Fatalf("expected 1 session (bfd1: header skipped), got %d", len(sessions))
		}
		if sessions[0].IP != "169.254.40.150" || sessions[0].Interface != "vlan-700" || sessions[0].State != "Up" {
			t.Errorf("session[0] = %+v", sessions[0])
		}
		if !sessions[0].IsUp() {
			t.Errorf("session[0].IsUp() = false, want true")
		}
	})
}
