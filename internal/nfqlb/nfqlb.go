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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/go-logr/logr"
	ctrl "sigs.k8s.io/controller-runtime"
)

// NFQueueLoadBalancer represents an nfqlb process with its related configuration and instances.
type NFQueueLoadBalancer struct {
	*nfqlbConfig
	instances map[string]*Instance // key: name
	mu        sync.Mutex
	logger    logr.Logger
	running   atomic.Bool
}

// New creates a new NFQueueLoadBalancer.
// Nftables rules for VIP matching and nfqueue are managed externally
// (via internal/nftables.Manager). This package only manages the nfqlb
// process, shared memory instances, flows, targets, and policy routing.
func New(options ...Option) (*NFQueueLoadBalancer, error) {
	config := newNFQLBConfig()
	for _, opt := range options {
		opt(config)
	}

	// Validate queue format to prevent command injection
	if _, _, err := getQueue(config.queue); err != nil {
		return nil, fmt.Errorf("invalid queue %q: %w", config.queue, err)
	}

	return &NFQueueLoadBalancer{
		nfqlbConfig: config,
		instances:   map[string]*Instance{},
		logger:      ctrl.Log.WithName("nfqlb"),
	}, nil
}

// Start nfqlb process in 'flowlb' mode supporting multiple shared mem lbs at once
// https://github.com/Nordix/nfqueue-loadbalancer/blob/1.1.4/src/nfqlb/cmdFlowLb.c#L238
// (Returned context gets cancelled when nfqlb process stops for whatever reason)
//
// Note:
// nfqlb process is supposed to run while the load-balancer container
// is alive and vice versa, thus there's no need for a Stop() function.
func (nfqlb *NFQueueLoadBalancer) Start(ctx context.Context) error {
	// Clean up stale policy rules/routes from a previous instance (container restart)
	if err := CleanupStaleRules(nfqlb.startingOffset); err != nil {
		nfqlb.logger.Error(err, "failed to cleanup stale rules at startup")
	}

	nfqlb.running.Store(true)
	defer nfqlb.running.Store(false)

	//nolint:gosec
	cmd := exec.CommandContext(
		ctx,
		nfqlb.nfqlbPath,
		"flowlb",
		"--promiscuous_ping",                   // accept ICMP Echo (ping) by default
		fmt.Sprintf("--queue=%s", nfqlb.queue), // gosec: queue is secured with the getQueue function.
		fmt.Sprintf("--qlength=%d", nfqlb.qlength), // gosec: qlength is secured since it is an int.
	)

	stdoutStderr, err := cmd.CombinedOutput()
	if err != nil && !errors.Is(err, context.Cause(ctx)) {
		return fmt.Errorf("failed starting nfqlb with flowlb ; %w; %s", err, stdoutStderr)
	}

	return nil
}

// updateNfQueueDestinationCIDRs is a no-op when nftables VIP management is external.
// The LB controller manages VIP sets via internal/nftables.Manager.
func (nfqlb *NFQueueLoadBalancer) updateNfQueueDestinationCIDRs(_ context.Context) error {
	return nil
}

// flowList runs the nfqlb flow-list commands and returns the output.
func (nfqlb *NFQueueLoadBalancer) flowList(ctx context.Context) ([]*nfqlbFlow, error) {
	args := []string{
		"flow-list",
	}

	//nolint:gosec
	cmd := exec.CommandContext(
		ctx,
		nfqlb.nfqlbPath,
		args...,
	)

	var stdout bytes.Buffer

	var stderr bytes.Buffer

	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		return nil, fmt.Errorf("failed listing nfqlb flows ; %w; %s", err, stderr.String())
	}

	return parseFlows(stdout.String())
}

// nfqlbFlow represents the nfqlb format returned with
// nfqlb flow-list.
//
//nolint:tagliatelle
type nfqlbFlow struct {
	Name                  string   `json:"Name"`
	ServerName            string   `json:"user_ref"`
	MatchesCount          int      `json:"matches_count"`
	SourceCIDRs           []string `json:"srcs"`
	DestinationCIDRs      []string `json:"dests"`
	SourcePortRange       []string `json:"sports"`
	DestinationPortRanges []string `json:"dports"`
	Protocols             []string `json:"protocols"`
	Priority              int32    `json:"priority"`
	ByteMatches           []string `json:"match"`
}

func (nfqlbf *nfqlbFlow) GetName() string {
	return nfqlbf.Name
}

func (nfqlbf *nfqlbFlow) GetSourceCIDRs() []string {
	return nfqlbf.SourceCIDRs
}

func (nfqlbf *nfqlbFlow) GetDestinationCIDRs() []string {
	return nfqlbf.DestinationCIDRs
}

func (nfqlbf *nfqlbFlow) GetSourcePortRanges() []string {
	return nfqlbf.SourcePortRange
}

func (nfqlbf *nfqlbFlow) GetDestinationPortRanges() []string {
	return nfqlbf.DestinationPortRanges
}

func (nfqlbf *nfqlbFlow) GetProtocols() []string {
	return nfqlbf.Protocols
}

func (nfqlbf *nfqlbFlow) GetPriority() int32 {
	return nfqlbf.Priority
}

func (nfqlbf *nfqlbFlow) GetByteMatches() []string {
	return nfqlbf.ByteMatches
}

func parseFlows(flowList string) ([]*nfqlbFlow, error) {
	nfqlbFlows := []*nfqlbFlow{}

	err := json.Unmarshal([]byte(flowList), &nfqlbFlows)
	if err != nil {
		return nil, fmt.Errorf("failed json.Unmarshal to flow-list ; %w", err)
	}

	return nfqlbFlows, nil
}

// Flow is the interface that wraps the basic Flow method.
type Flow interface {
	// Name of the flow
	GetName() string
	// Source CIDRs allowed in the flow
	// e.g.: ["124.0.0.0/24", "2001::/32"
	GetSourceCIDRs() []string
	// Destination CIDRs allowed in the flow
	// e.g.: ["124.0.0.0/24", "2001::/32"
	GetDestinationCIDRs() []string
	// Source port ranges allowed in the flow
	// e.g.: ["35000-35500", "40000"]
	GetSourcePortRanges() []string
	// Destination port ranges allowed in the flow
	// e.g.: ["35000-35500", "40000"]
	GetDestinationPortRanges() []string
	// Protocols allowed
	// e.g.: ["tcp", "udp"]
	GetProtocols() []string
	// Priority of the flow
	GetPriority() int32
	// Bytes in L4 header
	GetByteMatches() []string
}

// Instance represents a nfqlb instance instantiated with nfqlb init.
type Instance struct {
	*nfqlbInstanceConfig
	name                              string
	targets                           map[int][]string // Key: identifier ; Value: IPs
	broken                            map[int]struct{} // identifiers in inconsistent state (partial route or activate failure)
	offset                            int
	mu                                sync.Mutex
	updateNfQueueDestinationCIDRsFunc func(ctx context.Context) error
	nfqlbPath                         string
	// routeCreate and routeDelete are injectable for testing.
	// When nil, the package-level createPolicyRoute/deletePolicyRoute are used.
	routeCreate func(fwmark int, ip string) error
	routeDelete func(fwmark int, ip string) error
	// execCmd is injectable for testing. When nil, exec.CommandContext is used.
	execCmd func(ctx context.Context, args ...string) ([]byte, error)
}

// AddInstance adds a nfqlb instance.
func (nfqlb *NFQueueLoadBalancer) AddInstance(ctx context.Context,
	name string,
	options ...InstanceOption,
) (*Instance, error) {
	if !nfqlb.running.Load() {
		return nil, fmt.Errorf("NFQLB process not running")
	}

	nfqlb.mu.Lock()
	defer nfqlb.mu.Unlock()

	nfqlbInstance, exists := nfqlb.instances[name]
	if exists {
		return nfqlbInstance, nil
	}

	if err := validateName(name); err != nil {
		return nil, fmt.Errorf("invalid instance name: %w", err)
	}

	ctrl.LoggerFrom(ctx).Info("nfqlb: add instance", "instance", name)

	config := newNFQLBInstanceConfig()
	for _, opt := range options {
		opt(config)
	}

	offset, err := getOffset(nfqlb.startingOffset, nfqlb.instances, config.maxTargets)
	if err != nil {
		return nil, err
	}

	nfqlbInstance = &Instance{
		name:                              name,
		nfqlbInstanceConfig:               config,
		targets:                           map[int][]string{},
		broken:                            map[int]struct{}{},
		updateNfQueueDestinationCIDRsFunc: nfqlb.updateNfQueueDestinationCIDRs,
		offset:                            offset,
		nfqlbPath:                         nfqlb.nfqlbPath,
	}

	//nolint:gosec
	cmd := exec.CommandContext(
		ctx,
		nfqlb.nfqlbPath,
		"init",
		fmt.Sprintf("--ownfw=%d", ownfw),
		fmt.Sprintf("--shm=%s", nfqlbInstance.name),
		fmt.Sprintf("--M=%d", nfqlbInstance.getM()),
		fmt.Sprintf("--N=%d", nfqlbInstance.maxTargets),
	)

	stdoutStderr, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("failed init nfqlb ; %w; %s", err, stdoutStderr)
	}

	nfqlb.instances[name] = nfqlbInstance

	ctrl.LoggerFrom(ctx).Info("nfqlb: instance added", "instance", name)

	return nfqlbInstance, nil
}

// DeleteInstance deletes a nfqlb instance and all related configuration (targets and flows).
func (nfqlb *NFQueueLoadBalancer) DeleteInstance(ctx context.Context, name string) error {
	nfqlb.mu.Lock()

	nfqlbInstance, exists := nfqlb.instances[name]
	if !exists {
		return nil
	}

	ctrl.LoggerFrom(ctx).Info("nfqlb: delete instance", "instance", name)

	delete(nfqlb.instances, name)

	// Safe to release nfqlb.mu before locking instance.mu: the instance was
	// already removed from the map above, so heal() (which iterates
	// nfqlb.instances under nfqlb.mu) will no longer see it.
	nfqlb.mu.Unlock()

	nfqlbInstance.mu.Lock()
	defer nfqlbInstance.mu.Unlock()

	// unlink the shared mem file
	//nolint:gosec
	cmd := exec.CommandContext(
		ctx,
		nfqlb.nfqlbPath,
		"delete",
		fmt.Sprintf("--shm=%s", name),
	)

	stdoutStderr, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed deleting nfqlb instance ; %w; %s", err, stdoutStderr)
	}

	var errs []error

	for targetIdentifier, targetIPs := range nfqlbInstance.targets {
		if err := nfqlbInstance.deleteTargetNoLock(ctx, targetIPs, targetIdentifier); err != nil {
			errs = append(errs, fmt.Errorf("delete target %d: %w", targetIdentifier, err))
		}
	}

	flows, err := nfqlb.flowList(ctx)
	if err != nil {
		errs = append(errs, fmt.Errorf("list flows: %w", err))
		return errors.Join(errs...)
	}

	for _, flow := range flows {
		if flow.ServerName == name {
			if err := nfqlbInstance.DeleteFlow(ctx, flow); err != nil {
				errs = append(errs, fmt.Errorf("delete flow %s: %w", flow.GetName(), err))
			}
		}
	}

	ctrl.LoggerFrom(ctx).Info("nfqlb: instance deleted", "instance", name)

	return errors.Join(errs...)
}

// AddFlow adds/updates a Flow selecting the associated nfqlb instance.
func (s *Instance) AddFlow(ctx context.Context, flowToAdd Flow) error {
	if err := validateFlow(flowToAdd); err != nil {
		return fmt.Errorf("invalid flow: %w", err)
	}

	ctrl.LoggerFrom(ctx).Info("nfqlb: add flow", "instance", s.name, "flow", flowToAdd)

	args := []string{
		"flow-set",
		fmt.Sprintf("--name=%s", flowToAdd.GetName()),
		fmt.Sprintf("--target=%s", s.name),
		fmt.Sprintf("--prio=%d", flowToAdd.GetPriority()),
		fmt.Sprintf("--protocols=%s", strings.Join(flowToAdd.GetProtocols(), ",")),
	}

	if dsts := flowToAdd.GetDestinationCIDRs(); dsts != nil {
		args = append(args, fmt.Sprintf("--dsts=%s", strings.Join(dsts, ",")))
	}

	if srcs := flowToAdd.GetSourceCIDRs(); srcs != nil && !anyIPRange(srcs) {
		args = append(args, fmt.Sprintf("--srcs=%s", strings.Join(srcs, ",")))
	}

	if dports := flowToAdd.GetDestinationPortRanges(); dports != nil && !anyPortRange(dports) {
		args = append(args, fmt.Sprintf("--dports=%s", strings.Join(dports, ",")))
	}

	if sports := flowToAdd.GetSourcePortRanges(); sports != nil && !anyPortRange(sports) {
		args = append(args, fmt.Sprintf("--sports=%s", strings.Join(sports, ",")))
	}

	if byteMatches := flowToAdd.GetByteMatches(); byteMatches != nil {
		args = append(args, fmt.Sprintf("--match=%s", strings.Join(byteMatches, ",")))
	}

	//nolint:gosec
	cmd := exec.CommandContext(
		ctx,
		s.nfqlbPath,
		args...,
	)

	stdoutStderr, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed setting nfqlb flow ; %w; %s", err, stdoutStderr)
	}

	err = s.updateNfQueueDestinationCIDRsFunc(ctx)
	if err != nil {
		return fmt.Errorf("failed setting nfqlb flow ; %w; %s", err, stdoutStderr)
	}

	ctrl.LoggerFrom(ctx).Info("nfqlb: flow added", "instance", s.name, "flow", flowToAdd)

	return nil
}

// DeleteFlow deletes a Flow from the associated nfqlb instance.
func (s *Instance) DeleteFlow(ctx context.Context, flowToDelete Flow) error {
	ctrl.LoggerFrom(ctx).Info("nfqlb: delete flow", "instance", s.name, "flow", flowToDelete)

	args := []string{
		"flow-delete",
		fmt.Sprintf("--name=%s", flowToDelete.GetName()),
	}

	//nolint:gosec
	cmd := exec.CommandContext(
		ctx,
		s.nfqlbPath,
		args...,
	)

	stdoutStderr, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed deleting nfqlb flow ; %w; %s", err, stdoutStderr)
	}

	err = s.updateNfQueueDestinationCIDRsFunc(ctx)
	if err != nil {
		return fmt.Errorf("failed setting nfqlb flow ; %w; %s", err, stdoutStderr)
	}

	ctrl.LoggerFrom(ctx).Info("nfqlb: flow deleted", "instance", s.name, "flow", flowToDelete)

	return nil
}

// AddTarget adds a target identifier to the nfqlb instance
// and configures the policy route associated.
// If the identifier already exists with the same IPs, this is a no-op.
// If the identifier exists with different IPs, policy routes are updated
// without re-activating in nfqlb (the fwmark is unchanged).
func (s *Instance) AddTarget(ctx context.Context, ips []string, identifier int) error {
	if len(ips) == 0 {
		return fmt.Errorf("target IPs must not be empty")
	}
	if identifier < 0 || identifier >= s.maxTargets {
		return fmt.Errorf("identifier %d out of range [0, %d)", identifier, s.maxTargets)
	}
	for _, ip := range ips {
		if net.ParseIP(ip) == nil {
			return fmt.Errorf("invalid target IP: %q", ip)
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	existingIPs, exists := s.targets[identifier]
	if exists {
		if slicesEqual(existingIPs, ips) {
			// IPs unchanged — re-apply routes and re-activate to recover from
			// potential kernel drift or a previously broken state.
			// RouteReplace, ensureRule, and activate are all idempotent.
			fwmark := identifier + s.offset
			var errFinal error
			for _, ip := range ips {
				if err := s.doCreatePolicyRoute(fwmark, ip); err != nil {
					errFinal = errors.Join(errFinal, err)
				}
			}
			if errFinal != nil {
				s.broken[identifier] = struct{}{}
				return errFinal
			}
			output, err := s.doExec(ctx, "activate",
				fmt.Sprintf("--index=%d", identifier),
				fmt.Sprintf("--shm=%s", s.name),
				strconv.Itoa(identifier+s.offset),
			)
			if err != nil {
				s.broken[identifier] = struct{}{}
				return fmt.Errorf("failed activating nfqlb target ; %w; %s", err, output)
			}
			delete(s.broken, identifier)
			return nil
		}
		// IPs changed — clean old neighbors, delete old routes, apply new ones
		ctrl.LoggerFrom(ctx).Info("nfqlb: target IPs changed, updating routes",
			"instance", s.name, "identifier", identifier, "oldIPs", existingIPs, "newIPs", ips)
		fwmark := identifier + s.offset
		for _, ip := range existingIPs {
			if err := cleanNeighbor(net.ParseIP(ip)); err != nil {
				ctrl.LoggerFrom(ctx).V(1).Info("failed to clean neighbor (may not exist)",
					"ip", ip, "error", err)
			}
			if err := s.doDeletePolicyRoute(fwmark, ip); err != nil {
				ctrl.LoggerFrom(ctx).V(1).Info("failed to delete old route (may not exist)",
					"ip", ip, "error", err)
			}
		}
		s.targets[identifier] = ips
		var errFinal error
		for _, ip := range ips {
			if err := s.doCreatePolicyRoute(fwmark, ip); err != nil {
				errFinal = errors.Join(errFinal, err)
			}
		}
		if errFinal != nil {
			s.broken[identifier] = struct{}{}
			return errFinal
		}
		output, err := s.doExec(ctx, "activate",
			fmt.Sprintf("--index=%d", identifier),
			fmt.Sprintf("--shm=%s", s.name),
			strconv.Itoa(identifier+s.offset),
		)
		if err != nil {
			s.broken[identifier] = struct{}{}
			return fmt.Errorf("failed activating nfqlb target ; %w; %s", err, output)
		}
		delete(s.broken, identifier)
		return nil
	}

	ctrl.LoggerFrom(ctx).Info("nfqlb: add target", "instance", s.name, "ips", ips, "identifier", identifier)

	fwmark := identifier + s.offset
	s.targets[identifier] = ips

	// Create policy routes first — if this fails, no nfqlb slot is activated.
	var errFinal error
	for _, ip := range ips {
		if err := s.doCreatePolicyRoute(fwmark, ip); err != nil {
			errFinal = errors.Join(errFinal, err)
		}
	}
	if errFinal != nil {
		s.broken[identifier] = struct{}{}
		return errFinal
	}

	output, err := s.doExec(ctx, "activate",
		fmt.Sprintf("--index=%d", identifier),
		fmt.Sprintf("--shm=%s", s.name),
		strconv.Itoa(identifier+s.offset),
	)
	if err != nil {
		s.broken[identifier] = struct{}{}
		return fmt.Errorf("failed activating nfqlb target ; %w; %s", err, output)
	}

	delete(s.broken, identifier)
	ctrl.LoggerFrom(ctx).Info("nfqlb: target added", "instance", s.name, "ips", ips, "identifier", identifier)

	return nil
}

// doCreatePolicyRoute uses the injected function or falls back to the package-level one.
func (s *Instance) doCreatePolicyRoute(fwmark int, ip string) error {
	if s.routeCreate != nil {
		return s.routeCreate(fwmark, ip)
	}
	return createPolicyRoute(fwmark, ip)
}

// doDeletePolicyRoute uses the injected function or falls back to the package-level one.
func (s *Instance) doDeletePolicyRoute(fwmark int, ip string) error {
	if s.routeDelete != nil {
		return s.routeDelete(fwmark, ip)
	}
	return deletePolicyRoute(fwmark, ip)
}

// doExec uses the injected function or falls back to exec.CommandContext.
func (s *Instance) doExec(ctx context.Context, args ...string) ([]byte, error) {
	if s.execCmd != nil {
		return s.execCmd(ctx, args...)
	}
	//nolint:gosec
	return exec.CommandContext(ctx, s.nfqlbPath, args...).CombinedOutput()
}

// slicesEqual reports whether two string slices have the same elements (order-sensitive).
func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// BrokenTargets returns identifiers that were activated but have incomplete routes.
// The caller should call DeleteTarget for any broken identifier no longer in the desired set.
func (s *Instance) BrokenTargets() map[int]struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make(map[int]struct{}, len(s.broken))
	for id := range s.broken {
		result[id] = struct{}{}
	}
	return result
}

// Targets returns a copy of the current targets map.
func (s *Instance) Targets() map[int][]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make(map[int][]string, len(s.targets))
	maps.Copy(result, s.targets)
	return result
}

// DeleteTarget deactivates a target identifier in the nfqlb instance
// and deletes the associated policy routes.
func (s *Instance) DeleteTarget(ctx context.Context, ips []string, identifier int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.deleteTargetNoLock(ctx, ips, identifier)
}

func (s *Instance) deleteTargetNoLock(ctx context.Context, ips []string, identifier int) error {
	_, exists := s.targets[identifier]
	if !exists {
		return nil
	}

	ctrl.LoggerFrom(ctx).Info("nfqlb: delete target", "instance", s.name, "ips", ips, "identifier", identifier)

	output, err := s.doExec(ctx, "deactivate",
		fmt.Sprintf("--index=%d", identifier),
		fmt.Sprintf("--shm=%s", s.name),
	)
	if err != nil {
		s.broken[identifier] = struct{}{}
		return fmt.Errorf("failed deactivating nfqlb target ; %w; %s", err, output)
	}

	var errFinal error
	for _, ip := range ips {
		if err := s.doDeletePolicyRoute(identifier+s.offset, ip); err != nil {
			errFinal = errors.Join(errFinal, err)
		}
	}
	if errFinal != nil {
		s.broken[identifier] = struct{}{}
		return errFinal
	}

	// Remove from maps only after all operations succeed.
	delete(s.targets, identifier)
	delete(s.broken, identifier)

	ctrl.LoggerFrom(ctx).Info("nfqlb: target deleted", "instance", s.name, "ips", ips, "identifier", identifier)

	return nil
}
