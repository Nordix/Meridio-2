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

// Package cmdtest holds shared test helpers for the binary command packages
// under cmd/. It lives in its own package so the assertion logic is written
// once without coupling the individual binary cmd packages to one another:
// each binary's test imports this helper and passes in its own root command.
package cmdtest

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// AssertErrorGoesToStderrOnly guards the behaviour main() relies on: with
// SilenceErrors/SilenceUsage left unset, Cobra writes the error and usage
// itself, and because the root command's output/error writers are left at their
// defaults both land on os.Stderr — so os.Stdout stays clean and main() only
// needs to set the exit code. If a future change sets SilenceErrors (removing
// the error from stderr) or wires Cobra's out writer to os.Stdout (leaking usage
// to stdout), this fails.
//
// It captures the real os.Stdout/os.Stderr rather than cmd.SetOut/SetErr,
// because the guarantee is about process streams: Cobra routes the error to its
// err writer and usage to its out writer, and the point is that both default to
// os.Stderr in the built binary. It drives root.Execute() directly (the same
// call main() makes, minus the os.Exit) so the exit path is not taken.
func AssertErrorGoesToStderrOnly(t *testing.T, root *cobra.Command) {
	t.Helper()

	stdout, stderr := captureStdStreams(t, func() {
		root.SetArgs([]string{"--bogus-flag"})
		if err := root.Execute(); err == nil {
			t.Error("expected error for unknown flag")
		}
	})

	if stdout != "" {
		t.Errorf("stdout should be empty, got %q", stdout)
	}
	if !strings.Contains(stderr, "unknown flag") {
		t.Errorf("stderr should carry the error, got %q", stderr)
	}
}

// captureStdStreams redirects os.Stdout and os.Stderr through pipes for the
// duration of fn and returns whatever was written to each.
func captureStdStreams(t *testing.T, fn func()) (stdoutStr, stderrStr string) {
	t.Helper()

	origOut, origErr := os.Stdout, os.Stderr
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout, os.Stderr = outW, errW
	t.Cleanup(func() { os.Stdout, os.Stderr = origOut, origErr })

	outCh := make(chan string, 1)
	errCh := make(chan string, 1)
	go func() { b, _ := io.ReadAll(outR); outCh <- string(b) }()
	go func() { b, _ := io.ReadAll(errR); errCh <- string(b) }()

	fn()

	// Close write ends so the reader goroutines see EOF, then restore.
	_ = outW.Close()
	_ = errW.Close()
	os.Stdout, os.Stderr = origOut, origErr

	return <-outCh, <-errCh
}
