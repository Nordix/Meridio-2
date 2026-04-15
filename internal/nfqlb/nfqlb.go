/*
Copyright (c) 2024 OpenInfra Foundation Europe

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
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	ctrl "sigs.k8s.io/controller-runtime"
)

// NFQueueLoadBalancer represents an nfqlb process with its related configuration and services.
type NFQueueLoadBalancer struct {
	*nfqlbConfig
	services map[string]*Service // key: name
	mu       sync.Mutex
	logger   logr.Logger
}

// New instantiates a NFQLB struct and configure netfiler for the nfqlb process.
// New creates a new NFQueueLoadBalancer.
// Nftables rules for VIP matching and nfqueue are managed externally
// (via internal/nftables.Manager). This package only manages the nfqlb
// process, shared memory services, flows, targets, and policy routing.
func New(options ...Option) (*NFQueueLoadBalancer, error) {
	config := newNFQLBConfig()
	for _, opt := range options {
		opt(config)
	}

	return &NFQueueLoadBalancer{
		nfqlbConfig: config,
		services:    map[string]*Service{},
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
	//nolint:gosec
	cmd := exec.CommandContext(
		ctx,
		nfqlb.nfqlbPath,
		"flowlb",
		"--promiscuous_ping",                   // accept ICMP Echo (ping) by default
		fmt.Sprintf("--queue=%s", nfqlb.queue), // gosec: queue is secured with the getQueue function.
		fmt.Sprintf("--qlength=%d", nfqlb.qlength), // gosec: qlength is secured since it is an int.
	)

	var wg sync.WaitGroup

	wg.Add(1)

	go func() {
		defer wg.Done()
		nfqlb.heal(ctx)
	}()

	var errFinal error

	stdoutStderr, err := cmd.CombinedOutput()
	if err != nil && !errors.Is(err, context.Cause(ctx)) {
		errFinal = fmt.Errorf("failed starting nfqlb with flowlb ; %w; %s", err, stdoutStderr)
	}

	wg.Wait()

	return errFinal
}

func (nfqlb *NFQueueLoadBalancer) heal(ctx context.Context) {
	for {
		select {
		case <-time.After(nfqlb.healInterval):
			nfqlb.mu.Lock()
			for _, service := range nfqlb.services {
				service.mu.Lock()
				for identifier, ips := range service.targets {
					fwmark := identifier + service.offset

					for _, ip := range ips {
						err := createPolicyRoute(fwmark, ip)
						if err != nil {
							nfqlb.logger.Error(err, "failed creating policy route, will retry in next heal",
								"service", service.name,
								"fwmark", fwmark,
								"ip", ip,
							)
						}
					}
				}
				service.mu.Unlock()
			}
			nfqlb.mu.Unlock()
		case <-ctx.Done():
			return
		}
	}
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

// nfqlbFlowStats represents the nfqlb format returned with
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

// Service represents a nfqlb service instantiated with nfqlb init.
type Service struct {
	*nfqlbServiceConfig
	name                              string
	targets                           map[int][]string // Key: identifier ; Value: IPs
	offset                            int
	mu                                sync.Mutex
	updateNfQueueDestinationCIDRsFunc func(ctx context.Context) error
	nfqlbPath                         string
}

// AddService adds a nfqlb service.
func (nfqlb *NFQueueLoadBalancer) AddService(ctx context.Context,
	name string,
	options ...ServiceOption,
) (*Service, error) {
	nfqlb.mu.Lock()
	defer nfqlb.mu.Unlock()

	nfqlbService, exists := nfqlb.services[name]
	if exists {
		return nfqlbService, nil
	}

	ctrl.LoggerFrom(ctx).Info("nfqlb: add service", "service", name)

	config := newNFQLBServiceConfig()
	for _, opt := range options {
		opt(config)
	}

	offset, err := getOffset(nfqlb.startingOffset, nfqlb.services, config.maxTargets)
	if err != nil {
		return nil, err
	}

	nfqlbService = &Service{
		name:                              name,
		nfqlbServiceConfig:                config,
		targets:                           map[int][]string{},
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
		fmt.Sprintf("--shm=%s", nfqlbService.name),
		fmt.Sprintf("--M=%d", nfqlbService.getM()),
		fmt.Sprintf("--N=%d", nfqlbService.maxTargets),
	)

	stdoutStderr, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("failed init nfqlb ; %w; %s", err, stdoutStderr)
	}

	nfqlb.services[name] = nfqlbService

	ctrl.LoggerFrom(ctx).Info("nfqlb: service added", "service", name)

	return nfqlbService, nil
}

// DeleteService deletes a nfqlb service and all related configuration (targets and flows).
func (nfqlb *NFQueueLoadBalancer) DeleteService(ctx context.Context, name string) error {
	nfqlb.mu.Lock()

	nfqlbService, exists := nfqlb.services[name]
	if !exists {
		return nil
	}

	ctrl.LoggerFrom(ctx).Info("nfqlb: delete service", "service", name)

	delete(nfqlb.services, name)

	nfqlb.mu.Unlock()

	nfqlbService.mu.Lock()
	defer nfqlbService.mu.Unlock()

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
		return fmt.Errorf("failed deleting nfqlb service ; %w; %s", err, stdoutStderr)
	}

	var errFinal error

	for targetIdentifier, targetIPs := range nfqlbService.targets {
		err := nfqlbService.deleteTargetNoLock(ctx, targetIPs, targetIdentifier)
		if err != nil {
			errFinal = fmt.Errorf("failed deleting nfqlb service target ; %w; %w", err, errFinal)
		}
	}

	flows, err := nfqlb.flowList(ctx)
	if err != nil {
		return fmt.Errorf("failed deleting nfqlb service flows ; %w; %w", err, errFinal)
	}

	for _, flow := range flows {
		if flow.ServerName == name {
			err = nfqlbService.DeleteFlow(ctx, flow)
			if err != nil {
				errFinal = fmt.Errorf("failed deleting nfqlb service flow ; %w; %w", err, errFinal)
			}
		}
	}

	ctrl.LoggerFrom(ctx).Info("nfqlb: service deleted", "service", name)

	return errFinal
}

// AddFlow adds/updates a Flow selecting the associated nfqlb service.
func (s *Service) AddFlow(ctx context.Context, flowToAdd Flow) error {
	ctrl.LoggerFrom(ctx).Info("nfqlb: add flow", "service", s.name, "flow", flowToAdd)

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

	ctrl.LoggerFrom(ctx).Info("nfqlb: flow added", "service", s.name, "flow", flowToAdd)

	return nil
}

// DeleteFlow adds a Flow selecting the associated nfqlb service.
func (s *Service) DeleteFlow(ctx context.Context, flowToDelete Flow) error {
	ctrl.LoggerFrom(ctx).Info("nfqlb: delete flow", "service", s.name, "flow", flowToDelete)

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

	ctrl.LoggerFrom(ctx).Info("nfqlb: flow deleted", "service", s.name, "flow", flowToDelete)

	return nil
}

// AddTarget adds a target identifier to the nfqlb service
// and configures the policy route associated.
func (s *Service) AddTarget(ctx context.Context, ips []string, identifier int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, exists := s.targets[identifier]
	if exists {
		return nil
	}

	ctrl.LoggerFrom(ctx).Info("nfqlb: add target", "service", s.name, "ips", ips, "identifier", identifier)

	//nolint:gosec
	stdoutStderr, err := exec.CommandContext(
		ctx,
		s.nfqlbPath,
		"activate",
		fmt.Sprintf("--index=%d", identifier),
		fmt.Sprintf("--shm=%s", s.name),
		strconv.Itoa(identifier+s.offset),
	).CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed activating nfqlb target ; %w; %s", err, stdoutStderr)
	}

	s.targets[identifier] = ips

	fwmark := identifier + s.offset

	for _, ip := range ips {
		err = createPolicyRoute(fwmark, ip)
		if err != nil {
			ctrl.LoggerFrom(ctx).Error(err, "failed creating policy route, will retry in next heal",
				"service", s.name,
				"fwmark", fwmark,
				"ip", ip,
			)
		}
	}

	ctrl.LoggerFrom(ctx).Info("nfqlb: target added", "service", s.name, "ips", ips, "identifier", identifier)

	return nil
}

// DeleteTarget deletes a target identifier to the nfqlb service
// and deletes the policy route associated.
func (s *Service) DeleteTarget(ctx context.Context, ips []string, identifier int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.deleteTargetNoLock(ctx, ips, identifier)
}

func (s *Service) deleteTargetNoLock(ctx context.Context, ips []string, identifier int) error {
	_, exists := s.targets[identifier]
	if !exists {
		return nil
	}

	ctrl.LoggerFrom(ctx).Info("nfqlb: delete target", "service", s.name, "ips", ips, "identifier", identifier)

	delete(s.targets, identifier)

	//nolint:gosec
	stdoutStderr, err := exec.CommandContext(
		ctx,
		s.nfqlbPath,
		"deactivate",
		fmt.Sprintf("--index=%d", identifier),
		fmt.Sprintf("--shm=%s", s.name),
	).CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed deactivating nfqlb target ; %w; %s", err, stdoutStderr)
	}

	for _, ip := range ips {
		_ = deletePolicyRoute(identifier+s.offset, ip)
	}

	ctrl.LoggerFrom(ctx).Info("nfqlb: target deleted", "service", s.name, "ips", ips, "identifier", identifier)

	return nil
}
