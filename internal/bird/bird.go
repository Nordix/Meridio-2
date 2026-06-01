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
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	meridio2v1alpha1 "github.com/nordix/meridio-2/api/v1alpha1"
	"github.com/vishvananda/netlink"
)

// BirdInterface defines the interface for BIRD operations
type BirdInterface interface {
	Run(ctx context.Context) error
	Configure(ctx context.Context, vips []string, routers []*meridio2v1alpha1.GatewayRouter, passwords map[string]map[uint8]string) error
	Monitor(ctx context.Context, interval time.Duration) (<-chan MonitorStatus, error)
}

type Bird struct {
	SocketPath     string
	ConfigFile     string
	LogParams      BirdLogParams
	KernelScanTime int
	nl             abstractNetlink
	running        bool
	mu             sync.Mutex
}

type Option func(*Bird)

func WithLogParams(params BirdLogParams) Option {
	return func(b *Bird) { b.LogParams = params }
}

func WithKernelScanTime(seconds int) Option {
	return func(b *Bird) { b.KernelScanTime = seconds }
}

func New(opts ...Option) *Bird {
	b := &Bird{
		SocketPath: "/var/run/bird/bird.ctl",
		ConfigFile: "/etc/bird/bird.conf",
		nl:         &netlink.Handle{},
	}
	for _, o := range opts {
		o(b)
	}
	return b
}

func (b *Bird) Run(ctx context.Context) error {
	b.mu.Lock()
	if b.running {
		b.mu.Unlock()
		return fmt.Errorf("BIRD is already running")
	}

	if _, err := os.Stat(b.ConfigFile); errors.Is(err, os.ErrNotExist) {
		if err := b.writeConfig([]string{}, []*meridio2v1alpha1.GatewayRouter{}, nil); err != nil {
			b.mu.Unlock()
			return err
		}
	}

	cmd := exec.CommandContext(ctx, "bird", "-d", "-c", b.ConfigFile, "-s", b.SocketPath)
	cmd.Cancel = func() error {
		return cmd.Process.Signal(syscall.SIGTERM)
	}
	cmd.WaitDelay = 3 * time.Second
	if err := cmd.Start(); err != nil {
		b.mu.Unlock()
		return fmt.Errorf("BIRD failed to start: %w", err)
	}

	b.running = true
	b.mu.Unlock()

	if err := cmd.Wait(); err != nil && !errors.Is(err, context.Cause(ctx)) {
		b.mu.Lock()
		b.running = false
		b.mu.Unlock()
		return fmt.Errorf("BIRD failed: %w", err)
	}
	return nil
}

func vipsToCidr(vips []string) []string {
	cidrs := make([]string, len(vips))
	for i, vip := range vips {
		if isIPv6(vip) {
			cidrs[i] = vip + "/128"
		} else {
			cidrs[i] = vip + "/32"
		}
	}
	return cidrs
}

func (b *Bird) Configure(ctx context.Context, vips []string, routers []*meridio2v1alpha1.GatewayRouter, passwords map[string]map[uint8]string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	vips = vipsToCidr(vips)

	// Install policy routes first to minimize misrouting window.
	// Blackhole fallback ensures VIP traffic is dropped rather than
	// leaked before BGP routes are available.
	if err := setPolicyRoutes(b.nl, vips); err != nil {
		return err
	}

	if err := b.writeConfig(vips, routers, passwords); err != nil {
		return err
	}

	if b.running {
		cmd := exec.CommandContext(ctx, "birdc", "-s", b.SocketPath, "configure", `"`+b.ConfigFile+`"`)
		out, err := cmd.CombinedOutput()
		if err != nil && !errors.Is(err, context.Cause(ctx)) {
			return fmt.Errorf("birdc configure failed: %w: %s", err, out)
		}
	}

	return nil
}

func (b *Bird) generateConfig(vips []string, routers []*meridio2v1alpha1.GatewayRouter, passwords map[string]map[uint8]string) (string, error) {
	data := birdConfigData{KernelTableID: defaultKernelTableID, KernelScanTime: b.KernelScanTime, LogParams: b.LogParams}

	for _, vip := range vips {
		if isIPv6(vip) {
			data.IPv6VIPs = append(data.IPv6VIPs, vip)
		} else {
			data.IPv4VIPs = append(data.IPv4VIPs, vip)
		}
	}

	slices.SortFunc(routers, func(a, b *meridio2v1alpha1.GatewayRouter) int {
		if a.Name < b.Name {
			return -1
		}
		if a.Name > b.Name {
			return 1
		}
		return 0
	})

	ifset := make(map[string]bool)
	for _, r := range routers {
		rd, err := toRouterData(r, passwords[r.Name])
		if err != nil {
			return "", err
		}
		data.Routers = append(data.Routers, rd)
		ifset[r.Spec.Interface] = true
	}
	data.BGPInterfaces = slices.Sorted(maps.Keys(ifset))

	var buf strings.Builder
	if err := birdConfigTmpl.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func (b *Bird) writeConfig(vips []string, routers []*meridio2v1alpha1.GatewayRouter, passwords map[string]map[uint8]string) error {
	conf, err := b.generateConfig(vips, routers, passwords)
	if err != nil {
		return err
	}

	tmp := b.ConfigFile + ".tmp"
	if err := os.WriteFile(tmp, []byte(conf), 0644); err != nil {
		return err
	}
	return os.Rename(tmp, b.ConfigFile)
}
