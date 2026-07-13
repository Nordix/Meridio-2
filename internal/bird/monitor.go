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

// BgpInfoEstablished is the Info value BIRD reports for an established BGP session.
const BgpInfoEstablished = "Established"

// BfdInfoDown is the Info value set on static protocols when BFD session is down or missing.
const BfdInfoDown = "BFD Down"

// IsUp returns true if the protocol is in an operational state
func (s ProtocolState) IsUp() bool {
	return s == ProtocolStateUp
}

// IsEstablished checks if the protocol has an established BGP session
// For BGP protocols, both State must be "up" AND Info must contain "Established"
func (p ProtocolStatus) IsEstablished() bool {
	return p.State.IsUp() && strings.Contains(p.Info, BgpInfoEstablished)
}

// ProtocolStatus represents the status of a BIRD protocol
type ProtocolStatus struct {
	Name  string
	Proto string // Protocol type (BGP, Static)
	State ProtocolState
	Info  string
}

// MonitorStatus represents the overall monitoring status
type MonitorStatus struct {
	Protocols   []ProtocolStatus
	BfdSessions []BfdSession
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
		Protocols:   []ProtocolStatus{},
		BfdSessions: []BfdSession{},
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

	bfdCmd := exec.CommandContext(ctx, "birdc", "-s", b.SocketPath, "show", "bfd", "sessions")
	bfdOut, err := bfdCmd.CombinedOutput()
	if err != nil {
		log.V(1).Info("birdc bfd query failed", "error", err)
	} else {
		status.BfdSessions = parseBfdOutput(string(bfdOut))
	}

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

// BfdSession represents the state of a BIRD BFD session.
type BfdSession struct {
	IP        string
	Interface string
	State     string // "Up", "Down", "Init", etc.
}

// IsUp returns true if the BFD session state is "Up".
func (b BfdSession) IsUp() bool {
	return b.State == "Up"
}

// parseBfdOutput parses the output of `birdc show bfd sessions`.
func parseBfdOutput(output string) []BfdSession {
	sessions := make([]BfdSession, 0, 8)

	for line := range strings.SplitSeq(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "BIRD") ||
			strings.HasPrefix(line, "IP address") {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}

		sessions = append(sessions, BfdSession{
			IP:        fields[0],
			Interface: fields[1],
			State:     fields[2],
		})
	}

	return sessions
}
