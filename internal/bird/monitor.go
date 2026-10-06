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
	"strconv"
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

// ProtocolStatus represents the status of a BIRD protocol.
//
// RoutesImported, RoutesExported, and EstablishedUnix are populated only for
// BGP protocols (Proto == "BGP"), parsed from the detail block of `show
// protocols all` output. They are left nil for all other protocol types
// (e.g. Static) — nil means "not applicable", not "zero". See
// docs/design/router-metrics-source-of-truth.md for why these are BGP-only
// and must not be defaulted to a real zero value for non-BGP protocols.
type ProtocolStatus struct {
	Name  string
	Proto string // Protocol type (BGP, Static)
	State ProtocolState
	Info  string

	// RoutesImported is the protocol's imported route count, BGP only.
	RoutesImported *int
	// RoutesExported is the protocol's exported route count, BGP only.
	RoutesExported *int
	// EstablishedUnix is the protocol's `Since` timestamp as Unix epoch
	// seconds, BGP only. Requires `timeformat protocol "%s";` in bird.conf —
	// see docs/design/router-metrics-source-of-truth.md.
	EstablishedUnix *int64
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

	protocols, err := GetProtocolStatuses(ctx, b.SocketPath)
	if err != nil {
		return status
	}
	status.Protocols = protocols

	bfdSessions, err := GetBfdSessions(ctx, b.SocketPath)
	if err != nil {
		return status
	}
	status.BfdSessions = bfdSessions

	return status
}

// GetProtocolStatuses runs `birdc show protocols all "NBR-*"` against the BIRD
// instance listening on socketPath and parses the result into ProtocolStatus
// values. Exported, independent of any Bird instance's lifecycle (mutex/running
// state), so it can be reused by callers that have no relationship to the
// monitor's control loop (e.g. an independent metrics collector) and must only
// honor ctx's deadline.
func GetProtocolStatuses(ctx context.Context, socketPath string) ([]ProtocolStatus, error) {
	cmd := exec.CommandContext(ctx, "birdc", "-s", socketPath, "show", "protocols", "all", `"NBR-*"`)
	out, err := cmd.CombinedOutput()
	if err != nil {
		log.V(1).Info("birdc command failed", "cmd", cmd.String(), "error", err, "output", strings.TrimSpace(string(out)))
		return nil, fmt.Errorf("%s: %w: %s", cmd.String(), err, strings.TrimSpace(string(out)))
	}
	return parseShowProtocolsOutput(string(out)), nil
}

// parseShowProtocolsOutput parses birdc protocol output
func parseShowProtocolsOutput(output string) []ProtocolStatus {
	var protocols []ProtocolStatus

	lines := strings.Split(output, "\n")
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" || strings.HasPrefix(line, "BIRD") ||
			strings.HasPrefix(line, "Name") || strings.HasPrefix(line, "name") {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 5 || !strings.HasPrefix(fields[0], "NBR-") {
			continue
		}

		protocol := ProtocolStatus{
			Name:  fields[0],
			Proto: fields[1],
			State: ProtocolState(fields[3]),
		}
		if len(fields) > 5 {
			protocol.Info = strings.Join(fields[5:], " ")
		}

		if protocol.Proto == "BGP" {
			// Since (field 4) is a Unix epoch only when bird.conf set
			// `timeformat protocol "%s";`; otherwise it's BIRD's default
			// human-readable form and is left unparsed (EstablishedUnix nil).
			if epoch, err := strconv.ParseInt(fields[4], 10, 64); err == nil {
				protocol.EstablishedUnix = &epoch
			}
			// Consume this protocol's detail block in the same pass; i advances
			// to the last consumed line so the outer loop resumes at the block end.
			i = parseBGPDetail(lines, i, &protocol)
		}

		protocols = append(protocols, protocol)
	}

	return protocols
}

// parseBGPDetail scans the detail block following the BGP summary line at
// lines[summaryIdx], summing the route counts of every Channel sub-block into
// protocol. It returns the index of the last line belonging to this block, so
// the caller resumes immediately after it.
//
// Route counts are summed across all channels rather than selecting the
// "active" one. The counts are post-filter, so a disabled channel (import
// none/export none) always reports "0 imported, 0 exported" and contributes
// nothing — making the sum equal the active channel's counts without any
// brittle filter/REJECT detection, and independent of channel order. In this
// architecture a BGP protocol is single-family (the template declares only its
// own channel; older configs additionally carried a disabled second channel),
// so at most one channel is ever non-zero.
//
// The detail block is the run of indented lines after the summary line. It
// ends at the first non-detail line: a blank line (BIRD's separator between
// entries) or a non-indented line (the next top-level protocol entry) or at
// EOF.
func parseBGPDetail(lines []string, summaryIdx int, protocol *ProtocolStatus) int {
	var imported, exported int
	var found bool

	last := summaryIdx
	for j := summaryIdx + 1; j < len(lines); j++ {
		if strings.TrimSpace(lines[j]) == "" {
			break // blank line: BIRD's separator between top-level entries
		}
		// A detail line is indented; a line starting at column 0 is the next
		// top-level protocol entry, so it ends this block. BIRD indents detail
		// lines with spaces (tab tolerated defensively). The blank check above
		// guarantees lines[j] is non-empty here.
		if lines[j][0] != ' ' && lines[j][0] != '\t' {
			break
		}
		last = j

		if imp, exp, ok := parseRoutesLine(strings.TrimSpace(lines[j])); ok {
			imported += imp
			exported += exp
			found = true
		}
	}

	if found {
		protocol.RoutesImported = &imported
		protocol.RoutesExported = &exported
	}
	return last
}

// parseRoutesLine parses a BIRD "Routes: N imported, N exported, N preferred"
// detail line. Returns ok=false if it isn't a Routes: line or the counts
// aren't parseable.
func parseRoutesLine(trimmed string) (imported, exported int, ok bool) {
	rest, found := strings.CutPrefix(trimmed, "Routes:")
	if !found {
		return 0, 0, false
	}
	for part := range strings.SplitSeq(rest, ",") {
		f := strings.Fields(part)
		if len(f) != 2 {
			continue
		}
		n, err := strconv.Atoi(f[0])
		if err != nil {
			continue
		}
		switch f[1] {
		case "imported":
			imported, ok = n, true
		case "exported":
			exported, ok = n, true
		}
	}
	return imported, exported, ok
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

// GetBfdSessions runs `birdc show bfd sessions` against the BIRD instance listening
// on socketPath and parses the result into BfdSession values. Same independence
// rationale as GetProtocolStatuses.
func GetBfdSessions(ctx context.Context, socketPath string) ([]BfdSession, error) {
	cmd := exec.CommandContext(ctx, "birdc", "-s", socketPath, "show", "bfd", "sessions")
	out, err := cmd.CombinedOutput()
	if err != nil {
		log.V(1).Info("birdc command failed", "cmd", cmd.String(), "error", err, "output", strings.TrimSpace(string(out)))
		return nil, fmt.Errorf("%s: %w: %s", cmd.String(), err, strings.TrimSpace(string(out)))
	}
	return parseShowBfdSessionsOutput(string(out)), nil
}

// parseShowBfdSessionsOutput parses the output of `birdc show bfd sessions`.
func parseShowBfdSessionsOutput(output string) []BfdSession {
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
