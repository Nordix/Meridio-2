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
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/go-logr/logr"
	meridio2v1alpha1 "github.com/nordix/meridio-2/api/v1alpha1"
	"github.com/vishvananda/netlink"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// ResolvedAuth carries the Secret-resolved BGP authentication material for a
// single GatewayRouter. Exactly one mechanism is populated, matching the
// router's authentication type; the other field is zero.
type ResolvedAuth struct {
	// TcpAoPasswords maps each TCP-AO key's Send_ID to its resolved master key.
	TcpAoPasswords map[uint8]string

	// Md5Password is the resolved MD5 key.
	//
	// Deprecated: MD5 (RFC2385) is a weak, legacy mechanism superseded by
	// TCP-AO (RFC5925). Populated only for routers using MD5 authentication.
	Md5Password string
}

// BirdInterface defines the interface for BIRD operations
type BirdInterface interface {
	Run(ctx context.Context) error
	Configure(ctx context.Context, vips []string, routers []*meridio2v1alpha1.GatewayRouter, auth map[string]ResolvedAuth) error
	Monitor(ctx context.Context, interval time.Duration) (<-chan MonitorStatus, error)
}

// Config holds all configurable parameters for a Bird instance.
type Config struct {
	SocketPath     string
	ConfigFile     string
	TableID        int
	RulePriority   int
	LogParams      BirdLogParams
	KernelScanTime int

	// ConfigErrors counts failed netlink operations in the VIP routing path by reason.
	// Optional: leaving it unset disables the counting (the routing path treats a nil recorder
	// as a no-op), so callers with metrics disabled can simply omit it.
	ConfigErrors ConfigErrorRecorder
}

type Bird struct {
	Config
	nl         abstractNetlink
	running    bool
	lastConfig string // last written config for skip-if-unchanged guard
	mu         sync.Mutex

	// checkConfig reports whether BIRD would accept the config file at path.
	// It is checkConfigWithBird in production and replaceable in tests.
	checkConfig func(ctx context.Context, path string) error
}

// daemonLog receives the BIRD daemon's own stdout/stderr output.
var daemonLog = logf.Log.WithName("bird")

// configLog reports the router's own handling of the BIRD config file.
var configLog = logf.Log.WithName("bird-config")

// birdBinary is the BIRD daemon executable, looked up on PATH.
const birdBinary = "bird"

// configCheckTimeout bounds a single `bird -p` run.
const configCheckTimeout = 10 * time.Second

// ConfigRejectedError reports that BIRD refused to parse a config file. Output
// holds BIRD's own diagnostic (file:line:column and reason).
type ConfigRejectedError struct {
	Path   string
	Output string
	Err    error
}

func (e *ConfigRejectedError) Error() string {
	return fmt.Sprintf("BIRD rejected config %s: %v: %s", e.Path, e.Err, e.Output)
}

func (e *ConfigRejectedError) Unwrap() error { return e.Err }

// checkConfigWithBird parses path with `bird -p` (parse only, no daemon, no
// effect on a running BIRD). A non-zero exit is returned as a
// *ConfigRejectedError; any other failure (binary missing, timeout) is returned
// as is, so callers can tell "BIRD said no" from "could not ask BIRD".
func checkConfigWithBird(ctx context.Context, path string) error {
	ctx, cancel := context.WithTimeout(ctx, configCheckTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, birdBinary, "-p", "-c", path).CombinedOutput()
	if err == nil {
		return nil
	}
	if _, ok := errors.AsType[*exec.ExitError](err); ok && ctx.Err() == nil {
		return &ConfigRejectedError{Path: path, Output: strings.TrimSpace(string(out)), Err: err}
	}
	return fmt.Errorf("checking BIRD config %s: %w", path, err)
}

// birdClient returns a Client for this Bird's control socket. The Client is stateless (socket path
// plus command runner, no lifecycle or lock), so it is built on demand rather than stored. This
// keeps every Bird usable regardless of how it was constructed: the client derives only from
// SocketPath, with no field that a constructor must populate first.
func (b *Bird) birdClient() *Client {
	return NewClient(b.SocketPath)
}

func New(cfg Config) (*Bird, error) {
	if cfg.SocketPath == "" || cfg.ConfigFile == "" {
		return nil, fmt.Errorf("bird config: SocketPath and ConfigFile are required")
	}
	if cfg.TableID < 1 || cfg.TableID > math.MaxUint32-1 {
		return nil, fmt.Errorf("bird config: TableID must be between 1 and %d, got %d", math.MaxUint32-1, cfg.TableID)
	}
	if isReservedTable(cfg.TableID) || isReservedTable(cfg.TableID+1) {
		return nil, fmt.Errorf("bird config: TableID %d conflicts with reserved kernel tables (0, 253, 254, 255)", cfg.TableID)
	}
	if cfg.RulePriority < 0 || cfg.RulePriority > math.MaxInt16-1 {
		return nil, fmt.Errorf("bird config: RulePriority must be between 0 and %d, got %d", math.MaxInt16-1, cfg.RulePriority)
	}
	if cfg.KernelScanTime < 1 || cfg.KernelScanTime > 3600 {
		return nil, fmt.Errorf("bird config: KernelScanTime must be between 1 and 3600 seconds, got %d", cfg.KernelScanTime)
	}
	cfg.LogParams = slices.Clone(cfg.LogParams)
	return &Bird{
		Config:      cfg,
		nl:          &netlink.Handle{},
		checkConfig: checkConfigWithBird,
	}, nil
}

func isReservedTable(id int) bool {
	return id == 0 || id == 253 || id == 254 || id == 255
}

func (b *Bird) Run(ctx context.Context) error {
	b.mu.Lock()
	if b.running {
		b.mu.Unlock()
		return fmt.Errorf("BIRD is already running")
	}

	if err := b.prepareConfigFile(ctx); err != nil {
		b.mu.Unlock()
		return err
	}

	// -f: run in the foreground. (-d would additionally send BIRD's internal
	// debug messages to stderr, which is now forwarded to the router log.)
	cmd := exec.CommandContext(ctx, birdBinary, "-f", "-c", b.ConfigFile, "-s", b.SocketPath)
	cmd.Cancel = func() error {
		return cmd.Process.Signal(syscall.SIGTERM)
	}
	cmd.WaitDelay = 3 * time.Second
	// Forward BIRD's output (startup errors before its own logging is set up,
	// and anything logged via `log stderr`) to the router log instead of
	// discarding it. A single comparable writer for both streams means exec
	// calls it from one goroutine only.
	output := &lineWriter{log: daemonLog}
	cmd.Stdout = output
	cmd.Stderr = output
	if err := cmd.Start(); err != nil {
		b.mu.Unlock()
		return fmt.Errorf("BIRD failed to start: %w", err)
	}

	b.running = true
	b.mu.Unlock()

	err := cmd.Wait()
	output.Flush()
	if err != nil && !errors.Is(err, context.Cause(ctx)) {
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

func (b *Bird) Configure(ctx context.Context, vips []string, routers []*meridio2v1alpha1.GatewayRouter, auth map[string]ResolvedAuth) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	vips = vipsToCidr(vips)

	// Install policy routes first to minimize misrouting window.
	// Blackhole fallback ensures VIP traffic is dropped rather than
	// leaked before BGP routes are available.
	if err := setPolicyRoutes(b.nl, vips, b.TableID, b.RulePriority, b.ConfigErrors); err != nil {
		return err
	}

	conf, err := b.generateConfig(vips, routers, auth)
	if err != nil {
		return err
	}

	// Skip file write and BIRD reload if config is unchanged.
	// Relies on generateConfig producing deterministic output (sorted VIPs and routers).
	if conf == b.lastConfig {
		return nil
	}

	// The file is replaced only if `bird -p` accepts the new config, so the
	// file on disk always parses (also after a container restart).
	if err := b.writeConfig(ctx, conf); err != nil {
		return err
	}

	// From here on bird.conf holds conf. If the reload below fails, the
	// running BIRD and lastConfig keep the previous config and the next
	// reconcile retries the reload. A container restart in between starts
	// BIRD on conf, which `bird -p` has accepted.
	if b.running {
		// A cancellation-induced failure is tolerated: the caller's context going away is not a
		// configuration problem, and the next reconcile retries.
		if err := b.birdClient().Configure(ctx, b.ConfigFile); err != nil && !errors.Is(err, context.Cause(ctx)) {
			return fmt.Errorf("birdc configure failed: %w", err)
		}
	}

	// Update lastConfig only after successful write and reload.
	// If either fails, the next reconcile must retry.
	b.lastConfig = conf

	return nil
}

func (b *Bird) generateConfig(vips []string, routers []*meridio2v1alpha1.GatewayRouter, auth map[string]ResolvedAuth) (string, error) {
	data := birdConfigData{KernelTableID: b.TableID, KernelScanTime: b.KernelScanTime, LogParams: b.LogParams}

	for _, vip := range vips {
		if isIPv6(vip) {
			data.IPv6VIPs = append(data.IPv6VIPs, vip)
		} else {
			data.IPv4VIPs = append(data.IPv4VIPs, vip)
		}
	}

	// Sort VIPs for deterministic config output: input order depends on
	// Gateway status addresses which are pre-sorted, but sort defensively
	// to guarantee stable BIRD config regardless of caller ordering.
	slices.Sort(data.IPv4VIPs)
	slices.Sort(data.IPv6VIPs)

	slices.SortFunc(routers, func(a, b *meridio2v1alpha1.GatewayRouter) int {
		return cmp.Compare(a.Name, b.Name)
	})

	ifset := make(map[string]bool)
	bfdParams := make(map[string]*meridio2v1alpha1.BfdSpec)
	for _, r := range routers {

		switch r.Spec.Protocol {
		case meridio2v1alpha1.RoutingProtocolStatic:
			data.StaticRouters = append(data.StaticRouters, toStaticRouterData(r))
			if _, exists := bfdParams[r.Spec.Interface]; !exists && isStaticBFDOn(r) {
				bfdParams[r.Spec.Interface] = r.Spec.Static.BFD
			}
		case meridio2v1alpha1.RoutingProtocolBGP, "":
			rd, err := toBGPRouterData(r, auth[r.Name])
			if err != nil {
				return "", err
			}
			data.BGPRouters = append(data.BGPRouters, rd)
		default:
			log.Info("unknown gateway protocol, skipping", "router", r.Name, "protocol", r.Spec.Protocol)
			continue
		}
		ifset[r.Spec.Interface] = true
	}
	for _, iface := range slices.Sorted(maps.Keys(ifset)) {
		fmtParams := ""
		if spec, ok := bfdParams[iface]; ok {
			fmtParams = formatBFDInterfaceParams(*spec)
		}
		data.BFDInterfaces = append(data.BFDInterfaces, bfdInterfaceData{
			Name:   iface,
			Params: fmtParams,
		})
	}

	var buf strings.Builder
	if err := birdConfigTmpl.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// rejectedConfigFile is where a config BIRD refused is kept for inspection.
func (b *Bird) rejectedConfigFile() string {
	return b.ConfigFile + ".rejected"
}

// writeConfig replaces the config file with conf, but only if BIRD accepts
// conf. The candidate is written next to the config file (same filesystem, so
// the final rename is atomic) and checked with `bird -p` before the rename. A
// rejected candidate is kept as <config>.rejected and the config file is left
// untouched.
func (b *Bird) writeConfig(ctx context.Context, conf string) error {
	tmp := b.ConfigFile + ".tmp"
	if err := os.WriteFile(tmp, []byte(conf), 0644); err != nil {
		return err
	}
	if err := b.checkConfig(ctx, tmp); err != nil {
		if _, ok := errors.AsType[*ConfigRejectedError](err); ok && os.Rename(tmp, b.rejectedConfigFile()) == nil {
			return fmt.Errorf("%w (kept as %s)", err, b.rejectedConfigFile())
		}
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, b.ConfigFile)
}

// prepareConfigFile makes sure BIRD is started on a config it can parse.
//
// The config file lives on an emptyDir that survives container restarts, so at
// startup it may hold a config written by the previous container. If BIRD
// rejected that file, BIRD would exit, the container would restart into the
// same file, and the reconciler, which only runs while BIRD is up, could never
// replace it. So an existing file is checked first: a valid one is kept (BIRD
// resumes with the last applied config, matching the "retain previous config"
// behaviour), a rejected one is moved to <config>.rejected and replaced by the
// base config, which the first reconcile then extends.
//
// Must be called with b.mu held.
func (b *Bird) prepareConfigFile(ctx context.Context) error {
	_, err := os.Stat(b.ConfigFile)
	switch {
	case err == nil:
		checkErr := b.checkConfig(ctx, b.ConfigFile)
		if checkErr == nil {
			return nil
		}
		if _, ok := errors.AsType[*ConfigRejectedError](checkErr); !ok {
			// Could not check (e.g. timeout): do not discard a possibly
			// valid config; fail and let the container restart retry.
			return checkErr
		}
		if err := os.Rename(b.ConfigFile, b.rejectedConfigFile()); err != nil {
			return fmt.Errorf("moving rejected BIRD config aside: %w", err)
		}
		configLog.Error(checkErr, "Existing BIRD config is invalid, starting BIRD with the base config",
			"rejectedConfig", b.rejectedConfigFile())
	case errors.Is(err, os.ErrNotExist):
	default:
		return fmt.Errorf("checking for existing BIRD config: %w", err)
	}

	conf, err := b.generateConfig([]string{}, []*meridio2v1alpha1.GatewayRouter{}, nil)
	if err != nil {
		return err
	}
	if err := b.writeConfig(ctx, conf); err != nil {
		return fmt.Errorf("writing base BIRD config: %w", err)
	}
	b.lastConfig = conf
	return nil
}

// maxOutputLine bounds how many bytes of an unterminated line are buffered.
// When exceeded, the buffer is emitted as one entry, which may be longer than
// maxOutputLine if a single Write delivered more.
const maxOutputLine = 4096

// lineWriter forwards a process's output to a logger, one entry per line.
// It is not safe for concurrent use; exec.Cmd calls Write from a single
// goroutine when Stdout and Stderr are the same writer.
type lineWriter struct {
	log logr.Logger
	buf []byte
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		w.emit(w.buf[:i])
		w.buf = w.buf[i+1:]
	}
	if len(w.buf) > maxOutputLine {
		w.Flush()
	}
	return len(p), nil
}

// Flush emits any buffered partial line.
func (w *lineWriter) Flush() {
	if len(w.buf) > 0 {
		w.emit(w.buf)
	}
	w.buf = nil
}

func (w *lineWriter) emit(line []byte) {
	if s := strings.TrimRight(string(line), "\r"); s != "" {
		w.log.Info("BIRD output", "line", s)
	}
}
