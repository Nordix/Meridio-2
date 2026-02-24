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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseProtocolOutput(tt.output)
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

func TestHasConnectivity(t *testing.T) {
	tests := []struct {
		name      string
		protocols []ProtocolStatus
		expected  bool
	}{
		{
			name: "at least one established",
			protocols: []ProtocolStatus{
				{Name: "NBR-1", State: ProtocolStateUp, Info: "Established"},
				{Name: "NBR-2", State: ProtocolStateStart, Info: "Connect"},
			},
			expected: true,
		},
		{
			name: "all down",
			protocols: []ProtocolStatus{
				{Name: "NBR-1", State: ProtocolStateDown, Info: "Connection closed"},
				{Name: "NBR-2", State: ProtocolStateStart, Info: "Connect"},
			},
			expected: false,
		},
		{
			name:      "no protocols",
			protocols: []ProtocolStatus{},
			expected:  false,
		},
		{
			name: "all established",
			protocols: []ProtocolStatus{
				{Name: "NBR-1", State: ProtocolStateUp, Info: "Established"},
				{Name: "NBR-2", State: ProtocolStateUp, Info: "Established"},
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := hasConnectivity(tt.protocols)
			if result != tt.expected {
				t.Errorf("hasConnectivity() = %v, want %v", result, tt.expected)
			}
		})
	}
}
func TestParseAndFormatStatus(t *testing.T) {
	tests := []struct {
		name           string
		birdcOutput    string
		expectedStatus string
	}{
		{
			name: "dual-stack both up",
			birdcOutput: `BIRD 3.1.5 ready.
Name       Proto      Table      State  Since         Info
NBR-gatewayrouter-sample BGP        ---        up     10:04:13.527  Established
NBR-gatewayrouter-sample-v6 BGP        ---        up     10:04:12.499  Established`,
			expectedStatus: "2/2 protocols up",
		},
		{
			name: "dual-stack one up",
			birdcOutput: `BIRD 3.1.5 ready.
Name       Proto      Table      State  Since         Info
NBR-gatewayrouter-sample BGP        ---        start  10:04:13.527  Connect
NBR-gatewayrouter-sample-v6 BGP        ---        up     10:04:12.499  Established`,
			expectedStatus: "1/2 protocols up",
		},
		{
			name: "no protocols",
			birdcOutput: `BIRD 3.1.5 ready.
Name       Proto      Table      State  Since         Info`,
			expectedStatus: "No protocols configured",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			protocols := parseProtocolOutput(tt.birdcOutput)
			status := MonitorStatus{
				Protocols: protocols,
			}
			result := status.StatusString()
			if result != tt.expectedStatus {
				t.Errorf("StatusString() = %q, want %q", result, tt.expectedStatus)
			}
		})
	}
}

func TestProtocolUpCountChanges(t *testing.T) {
	// Simulate the sequence: 0 protocols → 1/1 up → 1/2 up → 2/2 up
	// This verifies that status string changes at each step
	outputs := []struct {
		birdcOutput    string
		expectedStatus string
		expectedCount  int
	}{
		{
			birdcOutput: `BIRD 3.1.5 ready.
Name       Proto      Table      State  Since         Info`,
			expectedStatus: "No protocols configured",
			expectedCount:  0,
		},
		{
			birdcOutput: `BIRD 3.1.5 ready.
Name       Proto      Table      State  Since         Info
NBR-gatewayrouter-sample-v6 BGP        ---        up     10:04:12.499  Established`,
			expectedStatus: "1/1 protocols up",
			expectedCount:  1,
		},
		{
			birdcOutput: `BIRD 3.1.5 ready.
Name       Proto      Table      State  Since         Info
NBR-gatewayrouter-sample BGP        ---        start  10:04:13.527  Connect
NBR-gatewayrouter-sample-v6 BGP        ---        up     10:04:12.499  Established`,
			expectedStatus: "1/2 protocols up",
			expectedCount:  1,
		},
		{
			birdcOutput: `BIRD 3.1.5 ready.
Name       Proto      Table      State  Since         Info
NBR-gatewayrouter-sample BGP        ---        up     10:04:13.527  Established
NBR-gatewayrouter-sample-v6 BGP        ---        up     10:04:12.499  Established`,
			expectedStatus: "2/2 protocols up",
			expectedCount:  2,
		},
	}

	for i, step := range outputs {
		protocols := parseProtocolOutput(step.birdcOutput)
		status := MonitorStatus{Protocols: protocols}

		upCount := 0
		for _, p := range protocols {
			if p.IsEstablished() {
				upCount++
			}
		}

		if upCount != step.expectedCount {
			t.Errorf("Step %d: upCount = %d, want %d", i, upCount, step.expectedCount)
		}

		statusStr := status.StatusString()
		if statusStr != step.expectedStatus {
			t.Errorf("Step %d: StatusString() = %q, want %q", i, statusStr, step.expectedStatus)
		}
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
