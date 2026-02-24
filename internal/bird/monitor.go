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
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// ProtocolState represents the state of a BIRD protocol
type ProtocolState string

const (
	ProtocolStateUp    ProtocolState = "up"
	ProtocolStateDown  ProtocolState = "down"
	ProtocolStateStart ProtocolState = "start"
	ProtocolStateIdle  ProtocolState = "idle"
)

// IsUp returns true if the protocol is in an operational state
func (s ProtocolState) IsUp() bool {
	return s == ProtocolStateUp
}

// IsEstablished checks if the protocol has an established BGP session
// For BGP protocols, both State must be "up" AND Info must contain "Established"
func (p ProtocolStatus) IsEstablished() bool {
	return p.State.IsUp() && strings.Contains(p.Info, "Established")
}

// ProtocolStatus represents the status of a BIRD protocol
type ProtocolStatus struct {
	Name  string
	Proto string // Protocol type (BGP, Static) - TODO: handle static protocols for feature parity with Meridio-1
	State ProtocolState
	Info  string
}

// MonitorStatus represents the overall monitoring status
type MonitorStatus struct {
	Protocols       []ProtocolStatus
	HasConnectivity bool
}

// Monitor periodically checks BGP protocol status by querying birdc.
// Returns a channel that emits MonitorStatus updates.
func (b *Bird) Monitor(ctx context.Context, interval time.Duration) (<-chan MonitorStatus, error) {
	statusCh := make(chan MonitorStatus, 1)

	go func() {
		defer close(statusCh)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				status := b.checkStatus(ctx)
				select {
				case statusCh <- status:
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	return statusCh, nil
}

// checkStatus queries birdc for protocol status
func (b *Bird) checkStatus(ctx context.Context) MonitorStatus {
	status := MonitorStatus{
		Protocols:       []ProtocolStatus{},
		HasConnectivity: false,
	}

	b.mu.Lock()
	running := b.running
	b.mu.Unlock()

	if !running {
		return status
	}

	cmd := exec.CommandContext(ctx, "birdc", "-s", b.SocketPath, "show", "protocols", "all", `"NBR-*"`)
	out, err := cmd.CombinedOutput()
	if err != nil {
		log.V(1).Info("birdc query failed", "error", err)
		return status
	}

	status.Protocols = parseProtocolOutput(string(out))
	status.HasConnectivity = hasConnectivity(status.Protocols)

	return status
}

// parseProtocolOutput parses birdc protocol output
func parseProtocolOutput(output string) []ProtocolStatus {
	var protocols []ProtocolStatus

	for line := range strings.SplitSeq(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "BIRD") ||
			strings.HasPrefix(line, "Name") || strings.HasPrefix(line, "name") {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}

		if strings.HasPrefix(fields[0], "NBR-") {
			protocol := ProtocolStatus{
				Name:  fields[0],
				Proto: fields[1],
				State: ProtocolState(fields[3]),
			}
			if len(fields) > 5 {
				protocol.Info = strings.Join(fields[5:], " ")
			}
			protocols = append(protocols, protocol)
		}
	}

	return protocols
}

// hasConnectivity determines if there's at least one established BGP session
func hasConnectivity(protocols []ProtocolStatus) bool {
	for _, p := range protocols {
		if p.IsEstablished() {
			return true
		}
	}
	return false
}

// StatusString returns a human-readable status summary
func (ms MonitorStatus) StatusString() string {
	if len(ms.Protocols) == 0 {
		return "No protocols configured"
	}

	upCount := 0
	for _, p := range ms.Protocols {
		if p.IsEstablished() {
			upCount++
		}
	}

	return fmt.Sprintf("%d/%d protocols up", upCount, len(ms.Protocols))
}
