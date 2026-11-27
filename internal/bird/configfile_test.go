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
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr/funcr"
	meridio2v1alpha1 "github.com/nordix/meridio-2/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Tests for issue #326: the config file on disk must always be one BIRD can
// parse, so a rejected config cannot turn a container restart into a
// CrashLoopBackOff.

// Peer address and interface of the routers built by testRouter.
const (
	testRouterAddress   = "169.254.100.1"
	testRouterInterface = "ext1"
)

func acceptConfig(context.Context, string) error { return nil }

// rejectMarked rejects any config containing "REJECT-ME", like BIRD would
// reject an unparseable file.
func rejectMarked(_ context.Context, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if strings.Contains(string(data), "REJECT-ME") {
		return &ConfigRejectedError{Path: path, Output: path + ":1:1 syntax error", Err: errors.New("exit status 1")}
	}
	return nil
}

func newTestBird(t *testing.T, check func(context.Context, string) error) *Bird {
	t.Helper()
	dir := t.TempDir()
	b, err := New(Config{
		SocketPath:     dir + "/bird.ctl",
		ConfigFile:     dir + "/bird.conf",
		TableID:        4096,
		RulePriority:   100,
		KernelScanTime: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	b.nl = &netlinkMock{}
	b.checkConfig = check
	return b
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func assertNotExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("%s should not exist (stat err: %v)", path, err)
	}
}

// testRouter returns a BGP router whose name ends up in the generated config,
// so a name containing "REJECT-ME" makes rejectMarked refuse the config.
func testRouter(name string) []*meridio2v1alpha1.GatewayRouter {
	return []*meridio2v1alpha1.GatewayRouter{{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: meridio2v1alpha1.GatewayRouterSpec{
			Interface: testRouterInterface, Address: testRouterAddress, BGP: &testBGPSpec,
		},
	}}
}

func TestConfigure_RejectedConfigKeepsLastGoodFile(t *testing.T) {
	b := newTestBird(t, rejectMarked)
	ctx := context.Background()

	if err := b.Configure(ctx, nil, testRouter("good"), nil); err != nil {
		t.Fatal(err)
	}
	good := readFile(t, b.ConfigFile)

	err := b.Configure(ctx, nil, testRouter("REJECT-ME"), nil)
	if _, ok := errors.AsType[*ConfigRejectedError](err); !ok {
		t.Fatalf("expected *ConfigRejectedError, got %v", err)
	}
	if !strings.Contains(err.Error(), "syntax error") || !strings.Contains(err.Error(), ".rejected") {
		t.Errorf("error should carry BIRD's diagnostic and the kept file: %v", err)
	}
	if got := readFile(t, b.ConfigFile); got != good {
		t.Error("config file was replaced by a rejected config")
	}
	if !strings.Contains(readFile(t, b.rejectedConfigFile()), "REJECT-ME") {
		t.Error("rejected config was not kept for inspection")
	}
	assertNotExist(t, b.ConfigFile+".tmp")
	if b.lastConfig != good {
		t.Error("lastConfig must still be the last accepted config")
	}

	// The same rejected input must be re-checked (and fail) on retry, not
	// skipped by the unchanged-config guard.
	if err := b.Configure(ctx, nil, testRouter("REJECT-ME"), nil); err == nil {
		t.Error("retry of a rejected config unexpectedly succeeded")
	}
	// A fixed input is accepted and replaces the file.
	if err := b.Configure(ctx, nil, testRouter("fixed"), nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readFile(t, b.ConfigFile), "fixed") {
		t.Error("fixed config was not written")
	}
}

func TestConfigure_CheckFailureIsNotARejection(t *testing.T) {
	b := newTestBird(t, func(context.Context, string) error { return errors.New("exec: bird: not found") })

	err := b.Configure(context.Background(), nil, testRouter("r"), nil)
	if err == nil {
		t.Fatal("expected error when the config cannot be checked")
	}
	if _, ok := errors.AsType[*ConfigRejectedError](err); ok {
		t.Error("an infrastructure failure must not be reported as a rejection")
	}
	assertNotExist(t, b.ConfigFile)
	assertNotExist(t, b.ConfigFile+".tmp")
	assertNotExist(t, b.rejectedConfigFile())
}

func TestPrepareConfigFile(t *testing.T) {
	ctx := context.Background()

	t.Run("missing file gets the base config", func(t *testing.T) {
		b := newTestBird(t, acceptConfig)
		if err := b.prepareConfigFile(ctx); err != nil {
			t.Fatal(err)
		}
		base := readFile(t, b.ConfigFile)
		if base == "" || b.lastConfig != base {
			t.Error("base config not written or lastConfig not set")
		}
	})

	t.Run("valid existing file is kept", func(t *testing.T) {
		b := newTestBird(t, rejectMarked)
		const previous = "# config from the previous container\n"
		if err := os.WriteFile(b.ConfigFile, []byte(previous), 0644); err != nil {
			t.Fatal(err)
		}
		if err := b.prepareConfigFile(ctx); err != nil {
			t.Fatal(err)
		}
		if readFile(t, b.ConfigFile) != previous {
			t.Error("valid existing config was replaced")
		}
		assertNotExist(t, b.rejectedConfigFile())
	})

	t.Run("rejected existing file is moved aside and replaced", func(t *testing.T) {
		// An unloadable file left by the previous container.
		b := newTestBird(t, rejectMarked)
		const stale = "REJECT-ME stale config\n"
		if err := os.WriteFile(b.ConfigFile, []byte(stale), 0644); err != nil {
			t.Fatal(err)
		}
		if err := b.prepareConfigFile(ctx); err != nil {
			t.Fatalf("startup must recover from a rejected file, got %v", err)
		}
		if readFile(t, b.rejectedConfigFile()) != stale {
			t.Error("stale config not kept as .rejected")
		}
		got := readFile(t, b.ConfigFile)
		if strings.Contains(got, "REJECT-ME") || b.lastConfig != got {
			t.Error("config file not replaced by the base config, or lastConfig out of sync")
		}
	})

	t.Run("unable to check keeps the existing file and fails", func(t *testing.T) {
		b := newTestBird(t, func(context.Context, string) error { return context.DeadlineExceeded })
		const previous = "# maybe valid\n"
		if err := os.WriteFile(b.ConfigFile, []byte(previous), 0644); err != nil {
			t.Fatal(err)
		}
		if err := b.prepareConfigFile(ctx); err == nil {
			t.Error("expected error")
		}
		if readFile(t, b.ConfigFile) != previous {
			t.Error("existing config must not be discarded when it could not be checked")
		}
	})

	t.Run("rejected base config is an error", func(t *testing.T) {
		b := newTestBird(t, func(_ context.Context, p string) error {
			return &ConfigRejectedError{Path: p, Output: "bad log path", Err: errors.New("exit status 1")}
		})
		err := b.prepareConfigFile(ctx)
		if err == nil || !strings.Contains(err.Error(), "base BIRD config") || !strings.Contains(err.Error(), "bad log path") {
			t.Errorf("expected base-config error with BIRD's diagnostic, got %v", err)
		}
		assertNotExist(t, b.ConfigFile)
	})
}

func TestLineWriter(t *testing.T) {
	var lines []string
	w := &lineWriter{log: funcr.New(func(_, args string) { lines = append(lines, args) }, funcr.Options{})}

	for _, chunk := range []string{"bird: first", " line\r\nsecond\n", "\n", "partial"} {
		if n, err := w.Write([]byte(chunk)); err != nil || n != len(chunk) {
			t.Fatalf("Write(%q) = %d, %v", chunk, n, err)
		}
	}
	w.Flush()

	want := []string{`"line"="bird: first line"`, `"line"="second"`, `"line"="partial"`}
	if len(lines) != len(want) {
		t.Fatalf("got %d lines %q, want %q", len(lines), lines, want)
	}
	for i := range want {
		if !strings.Contains(lines[i], want[i]) {
			t.Errorf("line %d = %q, want it to contain %q", i, lines[i], want[i])
		}
	}

	// An over-long line without a newline is emitted rather than buffered forever.
	lines = nil
	_, _ = w.Write([]byte(strings.Repeat("x", maxOutputLine+1)))
	if len(lines) != 1 || len(w.buf) != 0 {
		t.Errorf("over-long line: got %d entries, %d bytes buffered", len(lines), len(w.buf))
	}
}

// TestCheckConfigWithBird runs the real `bird -p`. It is skipped when no bird
// binary is on PATH (e.g. on developer machines and in unit-test CI).
func TestCheckConfigWithBird(t *testing.T) {
	if _, err := exec.LookPath(birdBinary); err != nil {
		t.Skip("bird binary not found on PATH")
	}
	b := newTestBird(t, checkConfigWithBird)
	ctx := context.Background()

	if err := b.Configure(ctx, nil, testRouter("good"), nil); err != nil {
		t.Fatalf("generated config rejected by real BIRD: %v", err)
	}

	bad := b.ConfigFile + ".bad"
	if err := os.WriteFile(bad, []byte("protocol bgp {\n"), 0644); err != nil {
		t.Fatal(err)
	}
	err := checkConfigWithBird(ctx, bad)
	rejected, ok := errors.AsType[*ConfigRejectedError](err)
	if !ok || rejected.Output == "" {
		t.Fatalf("expected *ConfigRejectedError with BIRD's diagnostic, got %v", err)
	}
	t.Logf("BIRD diagnostic: %s", rejected.Output)
}

// TestRun_StartsDespiteRejectedConfigFile reproduces #326 against a real BIRD
// daemon: a stale, unloadable config file left on disk by a previous container
// must not stop BIRD from starting, and BIRD's own output must reach the
// router log. Skipped when bird/birdc are not on PATH.
func TestRun_StartsDespiteRejectedConfigFile(t *testing.T) {
	for _, bin := range []string{birdBinary, "birdc"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s binary not found on PATH", bin)
		}
	}
	b := newTestBird(t, checkConfigWithBird)
	// Same path as the --bird-log flag.
	if err := b.LogParams.Set("stderr:all"); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var output []string
	saved := daemonLog
	daemonLog = funcr.New(func(_, args string) { mu.Lock(); output = append(output, args); mu.Unlock() }, funcr.Options{})
	t.Cleanup(func() { daemonLog = saved })

	// An over-length TCP-AO secret, which BIRD rejects at parse time.
	stale := "protocol bgp X { local as 65001; neighbor 10.0.0.2 as 65000; authentication ao; " +
		"keys { key { send id 1; recv id 1; secret \"" + strings.Repeat("a", 81) + "\"; algorithm hmac sha256; }; }; " +
		"ipv4 { import none; export none; }; }\n"
	if err := os.WriteFile(b.ConfigFile, []byte(stale), 0644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	client := NewClient(b.SocketPath)
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := client.run(ctx, "show", "status"); err == nil {
			break
		}
		select {
		case err := <-done:
			cancel()
			t.Fatalf("BIRD exited instead of starting: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("BIRD did not come up")
		}
		time.Sleep(100 * time.Millisecond)
	}

	if !strings.Contains(readFile(t, b.rejectedConfigFile()), strings.Repeat("a", 81)) {
		t.Error("stale config not kept as .rejected")
	}
	cancel()
	if err := <-done; err != nil {
		t.Errorf("Run returned %v after cancellation", err)
	}

	mu.Lock()
	defer mu.Unlock()
	joined := strings.Join(output, "\n")
	if !strings.Contains(joined, "BIRD output") {
		t.Errorf("BIRD output not forwarded to the log; got:\n%s", joined)
	}
}
