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

package nfqlb

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"maps"
	"strings"
)

// FlowMatch is the per-flow match counter read from nfqlb.
//
// Name is the nfqlb flow name, which in Meridio-2 equals the L34Route name (the LB
// controller names each flow after its L34Route — see internal/controller/loadbalancer).
// MatchesCount is nfqlb's monotonic counter of packets that matched the flow's selector.
// Note: the counter is bumped on flow match before a target is selected, and it resets to
// 0 if the flow is re-created — callers exposing it as a Prometheus counter should consume
// it via rate()/increase() (both reset-aware).
type FlowMatch struct {
	Name         string
	MatchesCount int
}

// FlowMatches returns the per-flow match counters from `nfqlb flow-list`.
//
// It reuses the already-parsed flow-list output (matches_count is part of nfqlbFlow), so no
// new subprocess beyond flow-list is run. Intended to be called lazily at metrics scrape time,
// not from the reconcile/packet path.
func (nfqlb *NFQueueLoadBalancer) FlowMatches(ctx context.Context) ([]FlowMatch, error) {
	flows, err := nfqlb.flowList(ctx)
	if err != nil {
		return nil, err
	}
	matches := make([]FlowMatch, 0, len(flows))
	for _, f := range flows {
		matches = append(matches, FlowMatch{Name: f.Name, MatchesCount: f.MatchesCount})
	}
	return matches, nil
}

// ActiveTargets returns, per nfqlb instance (keyed by instance name, which equals the
// DistributionGroup name), the number of active Maglev targets — i.e. the backends currently
// receiving traffic.
//
// The count is read from `nfqlb show --shm=<name>` output (the "Active:" line reflects the
// Maglev shared-memory activation state), which is the authoritative "currently serving"
// signal — unlike the in-memory target map, which also tracks not-yet-activated and broken
// targets (controller intent). Intended to be called lazily at metrics scrape time.
//
// The instance set is snapshotted under the NFQueueLoadBalancer lock; the per-instance
// `nfqlb show` subprocesses then run without holding it. Running `show` lock-free reads Maglev
// shared memory (not Go state), but can still race an instance being deleted (its shm unlinked),
// so a `show` may fail for an instance that is going away. Per-instance failures (show or parse)
// are best-effort and skipped — the instance is simply absent from the result for that scrape —
// rather than failing the whole call, so one failing/racing DG does not sink every DG's count.
// The error return is reserved for future use (currently always nil).
func (nfqlb *NFQueueLoadBalancer) ActiveTargets(ctx context.Context) (map[string]int, error) {
	// Snapshot the instances under the lock, then run subprocesses without holding it.
	nfqlb.mu.Lock()
	instances := make(map[string]*Instance, len(nfqlb.instances))
	maps.Copy(instances, nfqlb.instances)
	nfqlb.mu.Unlock()

	result := make(map[string]int, len(instances))
	for name, instance := range instances {
		out, err := instance.doExec(ctx, "show", fmt.Sprintf("--shm=%s", name))
		if err != nil {
			// Best-effort: skip this instance (e.g. a delete-race unlinked its shm) rather than
			// failing the whole scrape. Logged at V(1) for troubleshooting without steady noise.
			nfqlb.logger.V(1).Info("skipping instance: nfqlb show failed",
				"instance", name, "err", err, "output", string(out))
			continue
		}
		count, err := parseActiveTargets(out)
		if err != nil {
			nfqlb.logger.V(1).Info("skipping instance: failed to parse nfqlb show output",
				"instance", name, "output", string(out))
			continue
		}
		result[name] = count
	}
	return result, nil
}

// parseActiveTargets counts the active Maglev targets in `nfqlb show` output.
//
// The relevant line looks like:
//
//	Active: 5044(43) 5062(61) 5069(68) 5073(72)
//
// where each space-separated token is one active target (fwmark(identifier)). The count is the
// number of such tokens. An "Active:" line with no tokens means zero active targets.
func parseActiveTargets(output []byte) (int, error) {
	scanner := bufio.NewScanner(bytes.NewReader(output))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		rest, found := strings.CutPrefix(line, "Active:")
		if !found {
			continue
		}
		return len(strings.Fields(rest)), nil
	}
	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("error scanning nfqlb show output: %w", err)
	}
	return 0, fmt.Errorf("no \"Active:\" line found in nfqlb show output")
}
