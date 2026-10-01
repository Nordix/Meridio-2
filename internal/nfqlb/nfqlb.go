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
	"net"
	"os"
	"os/exec"
	"slices"
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

	// fwmarkBase must be >= 1: fwmark 0 is reserved (means "no mark").
	// Layout: fwmarkBase+0=nolb, fwmarkBase+1=notargets, fwmarkBase+2..=instance offsets.
	if config.fwmarkBase < 1 {
		return nil, fmt.Errorf("fwmarkBase must be >= 1 (got %d): fwmark 0 is reserved", config.fwmarkBase)
	}
	if config.fwmarkBase > MaxOffset-1 {
		return nil, fmt.Errorf("fwmarkBase must be <= %d (got %d): fwmarkBase+0 (nolb) and fwmarkBase+1 (notargets) must fit within the upper limit of %d", MaxOffset-1, config.fwmarkBase, MaxOffset)
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

// NoLBFwmark returns the fwmark value used when no flow matches.
func (nfqlb *NFQueueLoadBalancer) NoLBFwmark() int {
	return nfqlb.fwmarkBase
}

// NoTargetsFwmark returns the fwmark value used when no targets are active.
func (nfqlb *NFQueueLoadBalancer) NoTargetsFwmark() int {
	return nfqlb.fwmarkBase + 1
}

// startingOffset returns the offset where NFQLB instances begin allocating fwmarks.
func (nfqlb *NFQueueLoadBalancer) startingOffset() int {
	return nfqlb.fwmarkBase + 2
}

// Start nfqlb process in 'flowlb' mode supporting multiple shared mem lbs at once
// https://github.com/Nordix/nfqueue-loadbalancer/blob/1.1.4/src/nfqlb/cmdFlowLb.c#L238
//
// Blocks until the nfqlb process exits or ctx is cancelled. Returns nil only if
// ctx was cancelled (deliberate shutdown); any other termination — including a
// clean exit(0) — is reported as an error so the caller can treat a dead
// dataplane as fatal (see cmd/stateless-load-balancer/cmd/run.go, which cancels
// its own context and lets Kubernetes restart the container).
//
// Note:
// nfqlb process is supposed to run while the load-balancer container
// is alive and vice versa, thus there's no need for a Stop() function.
func (nfqlb *NFQueueLoadBalancer) Start(ctx context.Context) error {
	// Clean up stale policy rules/routes from a previous instance (container restart)
	if err := CleanupStaleRules(nfqlb.startingOffset()); err != nil {
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
		fmt.Sprintf("--nolb_fwmark=%d", nfqlb.NoLBFwmark()),
		fmt.Sprintf("--notargets_fwmark=%d", nfqlb.NoTargetsFwmark()),
	)

	stdoutStderr, err := cmd.CombinedOutput()

	// nfqlb is a long-running process for the lifetime of this container; it
	// is only ever expected to stop via ctx cancellation (container shutdown).
	// Any other termination — including a clean exit(0) — means the dataplane
	// is gone while the container is still alive, so it must be reported as
	// an error. The caller (cmd/stateless-load-balancer/cmd/run.go) cancels
	// the manager's context on error, crashing the container so Kubernetes
	// restarts it. Treating exit(0) as success here would leave `running`
	// permanently false with no process alive to flip it back: new NFQLB
	// instances fail forever while already-tracked instances keep accepting
	// target/flow changes against a dead daemon.
	//
	// ctx.Err() != nil is checked instead of comparing err against
	// context.Cause(ctx): exec.CommandContext kills the process with SIGKILL
	// on cancellation, so CombinedOutput's error is "signal: killed"
	// (an *exec.ExitError), never the context's cancellation error itself —
	// errors.Is against context.Cause(ctx) would never match.
	if ctx.Err() != nil {
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed starting nfqlb with flowlb ; %w; %s", err, stdoutStderr)
	}

	return fmt.Errorf("nfqlb flowlb terminated unexpectedly without a context cancellation ; %s", stdoutStderr)
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
	name      string
	targets   map[int][]string // Key: identifier ; Value: IPs
	broken    map[int]struct{} // identifiers in inconsistent state (partial route or activate failure)
	offset    int
	mu        sync.Mutex
	nfqlbPath string
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

	offset, err := getOffset(nfqlb.startingOffset(), nfqlb.instances, config.maxTargets)
	if err != nil {
		return nil, err
	}

	nfqlbInstance = &Instance{
		name:                name,
		nfqlbInstanceConfig: config,
		targets:             map[int][]string{},
		broken:              map[int]struct{}{},
		offset:              offset,
		nfqlbPath:           nfqlb.nfqlbPath,
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
// Cleanup order is: targets/routes, then flows, then the shm segment unlink last.
// The instance is removed from nfqlb.instances (releasing its fwmark offset for reuse)
// only after every step succeeds. If any step fails, the instance remains tracked (so
// its offset cannot be handed to a different DG while stale routes for it may still
// exist) and the caller is expected to retry DeleteInstance on the next reconcile.
func (nfqlb *NFQueueLoadBalancer) DeleteInstance(ctx context.Context, name string) error {
	nfqlb.mu.Lock()
	defer nfqlb.mu.Unlock()

	nfqlbInstance, exists := nfqlb.instances[name]
	if !exists {
		return nil
	}

	ctrl.LoggerFrom(ctx).Info("nfqlb: delete instance", "instance", name)

	nfqlbInstance.mu.Lock()
	defer nfqlbInstance.mu.Unlock()

	var errs []error

	for targetIdentifier := range nfqlbInstance.targets {
		if err := nfqlbInstance.deleteTargetNoLock(ctx, targetIdentifier); err != nil {
			errs = append(errs, fmt.Errorf("delete target %d: %w", targetIdentifier, err))
		}
	}

	flows, err := nfqlb.flowList(ctx)
	if err != nil {
		errs = append(errs, fmt.Errorf("list flows: %w", err))
	} else {
		for _, flow := range flows {
			if flow.ServerName == name {
				if err := nfqlbInstance.DeleteFlow(ctx, flow); err != nil {
					errs = append(errs, fmt.Errorf("delete flow %s: %w", flow.GetName(), err))
				}
			}
		}
	}

	if len(errs) > 0 {
		// Targets/flows are not fully cleaned up: keep the instance (and its
		// offset) reserved rather than unlinking shm and losing track of it.
		return fmt.Errorf("failed cleaning up nfqlb instance %q, shm not deleted: %w", name, errors.Join(errs...))
	}

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

	// Only release the offset for reuse once targets, flows, and the shm
	// segment are all confirmed gone.
	delete(nfqlb.instances, name)

	ctrl.LoggerFrom(ctx).Info("nfqlb: instance deleted", "instance", name)

	return nil
}

// shmDir is the directory nfqlb uses for its shared-memory segments.
// Overridable in tests.
var shmDir = "/dev/shm"

// GCStaleInstances removes NFQLB shared-memory segments left over from a
// previous process lifetime (container restart) whose owning instance no
// longer exists.
//
// Why this is needed: /dev/shm is a pod-sandbox-lifetime mount, but
// nfqlb.instances (and the LB controller's own tracking map) is rebuilt
// from scratch on every container start. If a DistributionGroup is deleted
// while the container is down (or otherwise never reaches a clean
// DeleteInstance call before a restart), its shm segment is never visited
// again by anything in this process — nothing short of a sweep like this one
// discovers it. Over repeated restarts and scale churn this accumulates
// unboundedly.
//
// keep is the set of instance names (DistributionGroup names) that are
// currently known-live, independent of nfqlb.instances — the caller is
// expected to derive it from the Kubernetes API (the authoritative source),
// not from any in-memory NFQLB state, since the whole point is to recover
// state that this process's memory does not have.
//
// Safety: only files directly under shmDir whose name passes validateName
// (the same validation applied to instance names before they are ever
// passed to `nfqlb init --shm=`) are considered for deletion. Entries that
// are already tracked in nfqlb.instances are never touched, even if absent
// from keep, to avoid racing a concurrent AddInstance/DeleteInstance call
// for the same name.
func (nfqlb *NFQueueLoadBalancer) GCStaleInstances(ctx context.Context, keep map[string]struct{}) error {
	entries, err := os.ReadDir(shmDir)
	if err != nil {
		return fmt.Errorf("failed to read %s for NFQLB shm garbage collection: %w", shmDir, err)
	}

	nfqlb.mu.Lock()
	defer nfqlb.mu.Unlock()

	var errs []error

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()

		if validateName(name) != nil {
			// Not a name this package would ever have passed to `nfqlb init --shm=`;
			// leave unrelated files alone.
			continue
		}
		if _, tracked := nfqlb.instances[name]; tracked {
			continue
		}
		if _, live := keep[name]; live {
			continue
		}

		ctrl.LoggerFrom(ctx).Info("nfqlb: garbage collecting stale shm instance", "instance", name)

		//nolint:gosec
		cmd := exec.CommandContext(ctx, nfqlb.nfqlbPath, "delete", fmt.Sprintf("--shm=%s", name))
		if stdoutStderr, err := cmd.CombinedOutput(); err != nil {
			errs = append(errs, fmt.Errorf("delete stale shm %q: %w; %s", name, err, stdoutStderr))
			continue
		}

		ctrl.LoggerFrom(ctx).Info("nfqlb: stale shm instance garbage collected", "instance", name)
	}

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

	ctrl.LoggerFrom(ctx).Info("nfqlb: flow deleted", "instance", s.name, "flow", flowToDelete)

	return nil
}

// AddTarget creates policy routes and activates the nfqlb slot for the given identifier.
// If the target already exists with the same IPs and is not broken, only routes are re-applied
// (drift recovery) without re-activating in nfqlb (the fwmark is unchanged).
func (s *Instance) AddTarget(ctx context.Context, ips []string, identifier int) (err error) {
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

	defer func() {
		if err != nil {
			s.broken[identifier] = struct{}{}
		} else {
			delete(s.broken, identifier)
		}
	}()

	existingIPs, exists := s.targets[identifier]
	_, broken := s.broken[identifier]
	skipActivate := exists && !broken

	// Handle existing target: clean up stale state for changed IPs
	if exists && !slicesEqual(existingIPs, ips) {
		ctrl.LoggerFrom(ctx).Info("nfqlb: target IPs changed, updating routes",
			"instance", s.name, "identifier", identifier, "oldIPs", existingIPs, "newIPs", ips)
		fwmark := identifier + s.offset
		for _, ip := range existingIPs {
			// IP change may imply Pod replacement (new MAC for all IPs)
			_ = cleanNeighbor(net.ParseIP(ip))
			if !slices.Contains(ips, ip) {
				_ = s.doDeletePolicyRoute(fwmark, ip)
			}
		}
	} else if !exists {
		ctrl.LoggerFrom(ctx).Info("nfqlb: add target", "instance", s.name, "ips", ips, "identifier", identifier)
	}

	// Update stored IPs and create routes
	s.targets[identifier] = ips
	fwmark := identifier + s.offset

	for _, ip := range ips {
		if e := s.doCreatePolicyRoute(fwmark, ip); e != nil {
			err = errors.Join(err, e)
		}
	}
	if err != nil {
		return err
	}

	// Activate only for new targets or recovery from broken state
	if skipActivate {
		return err
	}

	var output []byte
	output, err = s.doExec(ctx, "activate",
		fmt.Sprintf("--index=%d", identifier),
		fmt.Sprintf("--shm=%s", s.name),
		strconv.Itoa(identifier+s.offset),
	)
	if err != nil {
		err = fmt.Errorf("failed activating nfqlb target ; %w; %s", err, output)
	}

	return err
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

// DeleteTarget deactivates a target identifier in the nfqlb instance
// and deletes the associated policy routes.
func (s *Instance) DeleteTarget(ctx context.Context, identifier int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.deleteTargetNoLock(ctx, identifier)
}

func (s *Instance) deleteTargetNoLock(ctx context.Context, identifier int) (err error) {
	storedIPs, exists := s.targets[identifier]
	if !exists {
		return nil
	}

	ctrl.LoggerFrom(ctx).Info("nfqlb: delete target", "instance", s.name, "ips", storedIPs, "identifier", identifier)

	defer func() {
		if err != nil {
			s.broken[identifier] = struct{}{}
		} else {
			delete(s.targets, identifier)
			delete(s.broken, identifier)
		}
	}()

	var output []byte
	output, err = s.doExec(ctx, "deactivate",
		fmt.Sprintf("--index=%d", identifier),
		fmt.Sprintf("--shm=%s", s.name),
	)
	if err != nil {
		err = fmt.Errorf("failed deactivating nfqlb target ; %w; %s", err, output)
		return err
	}

	for _, ip := range storedIPs {
		if e := s.doDeletePolicyRoute(identifier+s.offset, ip); e != nil {
			err = errors.Join(err, e)
		}
	}

	return err
}
