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

	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// clientLog names birdc invocation failures for what they are. The package's other logger is
// named "routing" (see routing.go) and logging under it would mislabel control-client errors.
var clientLog = logf.Log.WithName("birdc")

// CommandRunner invokes the `birdc` control client and returns its combined output. It exists as
// the single seam where this package touches a subprocess, so callers can be tested against forged
// `birdc` output while the real parsing still runs.
//
// args are the `birdc` arguments after the control socket, which Client supplies.
type CommandRunner interface {
	Run(ctx context.Context, args ...string) ([]byte, error)
}

// execRunner is the production CommandRunner, executing the `birdc` binary found on PATH.
type execRunner struct{}

// birdcWaitDelay bounds how long Run waits after the context is cancelled before force-closing
// birdc's I/O pipes. Without it, CombinedOutput reads the pipes until EOF, which only occurs once
// every holder of the write end exits; a killed process whose descendants still hold the pipe
// would block Wait past the deadline, so --metrics-collect-timeout would not be a hard bound.
// birdc does not fork, so this is a safety net rather than an expected path. Kept small: it is a
// grace period to let a signalled birdc flush and exit, not a second query budget, and it is
// additive to the collect timeout (worst case collectTimeout + birdcWaitDelay), which must stay
// under Prometheus's scrape_timeout.
//
// This acts only on the birdc client process we spawn, never on the long-running BIRD daemon,
// which is a separate process reached over the control socket. A force-killed birdc is, to the
// daemon, an ordinary client disconnect; a read-only `show` holds no lock and mutates no state.
// CommandContext already kills birdc on timeout today, so this bounds our wait without changing
// what birdc itself receives.
const birdcWaitDelay = 1 * time.Second

// Note: If WaitDelay force-closes the pipes while birdc had otherwise exited 0, CombinedOutput
// returns ErrWaitDelay instead of nil, surfacing as a fetch error to every caller of this shared
// runner. We do not special-case ErrWaitDelay because birdc does not spawn children or orphan its
// pipes, so this path does not occur in practice.
func (execRunner) Run(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "birdc", args...)
	cmd.WaitDelay = birdcWaitDelay
	return cmd.CombinedOutput()
}

// Client issues `birdc` commands against one BIRD control socket and parses the responses.
//
// It carries no lock and no lifecycle state, only the socket path and the command runner, so it is
// safe to hand a Client to callers unrelated to the monitor's control loop (notably the metrics
// collector) and it honors only the caller's context deadline.
type Client struct {
	socketPath string
	runner     CommandRunner
}

// ClientOption customizes a Client.
type ClientOption func(*Client)

// WithCommandRunner replaces how `birdc` is invoked. Intended for tests, which supply canned
// output so that command construction and response parsing are exercised without a live BIRD.
func WithCommandRunner(r CommandRunner) ClientOption {
	return func(c *Client) { c.runner = r }
}

// NewClient returns a Client for the BIRD instance listening on socketPath, executing the real
// `birdc` binary unless a different CommandRunner is supplied.
func NewClient(socketPath string, opts ...ClientOption) *Client {
	c := &Client{socketPath: socketPath, runner: execRunner{}}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// run executes a `birdc` command against this Client's socket. Centralizing it keeps the socket
// argument, the failure log and the error wrapping in one place for every command.
func (c *Client) run(ctx context.Context, args ...string) ([]byte, error) {
	full := append([]string{"-s", c.socketPath}, args...)
	out, err := c.runner.Run(ctx, full...)
	if err != nil {
		cmd := "birdc " + strings.Join(full, " ")
		clientLog.V(1).Info("birdc command failed", "cmd", cmd, "error", err, "output", strings.TrimSpace(string(out)))
		return nil, fmt.Errorf("%s: %w: %s", cmd, err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

// GetProtocolStatuses runs `birdc show protocols all "NBR-*"` and parses the result into
// ProtocolStatus values.
func (c *Client) GetProtocolStatuses(ctx context.Context) ([]ProtocolStatus, error) {
	out, err := c.run(ctx, "show", "protocols", "all", `"NBR-*"`)
	if err != nil {
		return nil, err
	}
	return parseShowProtocolsOutput(string(out)), nil
}

// GetBfdSessions runs `birdc show bfd sessions` and parses the result into BfdSession values.
func (c *Client) GetBfdSessions(ctx context.Context) ([]BfdSession, error) {
	out, err := c.run(ctx, "show", "bfd", "sessions")
	if err != nil {
		return nil, err
	}
	return parseShowBfdSessionsOutput(string(out)), nil
}

// Configure runs `birdc configure "<configFile>"` to make a running BIRD reload its configuration.
func (c *Client) Configure(ctx context.Context, configFile string) error {
	_, err := c.run(ctx, "configure", `"`+configFile+`"`)
	return err
}
