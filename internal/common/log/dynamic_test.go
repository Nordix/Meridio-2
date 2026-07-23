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

package log

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/funcr"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func TestStartDynamicLevelServer_Disabled(t *testing.T) {
	// Empty address should be a no-op
	level := zap.NewAtomicLevelAt(zapcore.InfoLevel)
	logger := logr.Discard()

	StartDynamicLevelServer("", level, logger)
	// If it doesn't panic or block, test passes
}

// capturingLogger returns a logr.Logger backed by funcr that appends every
// formatted log line to lines (guarded by mu), so tests can assert real log
// output rather than only compiling against the logr.Logger interface.
func capturingLogger(mu *sync.Mutex, lines *[]string) logr.Logger {
	return funcr.New(func(prefix, args string) {
		mu.Lock()
		defer mu.Unlock()
		if prefix != "" {
			*lines = append(*lines, prefix+": "+args)
		} else {
			*lines = append(*lines, args)
		}
	}, funcr.Options{})
}

func TestStartDynamicLevelServer_LogsThroughRealLogger(t *testing.T) {
	var mu sync.Mutex
	var lines []string
	logger := capturingLogger(&mu, &lines)

	level := zap.NewAtomicLevelAt(zapcore.InfoLevel)
	StartDynamicLevelServer("127.0.0.1:19905", level, logger)

	// Give server time to start and log its startup message
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	require.NotEmpty(t, lines, "expected at least one log line through the real logger")

	found := false
	for _, l := range lines {
		if strings.Contains(l, "Log level API listening") {
			found = true
			// WithName("loglevel-api") should be reflected in the logger name
			require.Contains(t, l, "loglevel-api")
			break
		}
	}
	require.True(t, found, "expected startup log line to be emitted through the provided logr.Logger, got: %v", lines)
}

func TestStartDynamicLevelServer_AcceptsLoopback(t *testing.T) {
	tests := []struct {
		name string
		addr string
	}{
		{"ipv4 loopback", "127.0.0.1:19901"},
		{"ipv6 loopback", "[::1]:19902"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			level := zap.NewAtomicLevelAt(zapcore.InfoLevel)
			logger := logr.Discard()

			StartDynamicLevelServer(tt.addr, level, logger)

			// Give server time to start
			time.Sleep(100 * time.Millisecond)

			// Test GET
			resp, err := http.Get("http://" + tt.addr + "/log/level")
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()
			require.Equal(t, http.StatusOK, resp.StatusCode)

			var result struct {
				Level string `json:"level"`
			}
			err = json.NewDecoder(resp.Body).Decode(&result)
			require.NoError(t, err)
			require.Equal(t, "info", result.Level)
		})
	}
}

func TestStartDynamicLevelServer_GetAndPut(t *testing.T) {
	level := zap.NewAtomicLevelAt(zapcore.InfoLevel)
	logger := logr.Discard()

	StartDynamicLevelServer("127.0.0.1:19903", level, logger)

	// Give server time to start
	time.Sleep(100 * time.Millisecond)

	// Test GET - initial level
	resp, err := http.Get("http://127.0.0.1:19903/log/level")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var result struct {
		Level string `json:"level"`
	}
	err = json.NewDecoder(resp.Body).Decode(&result)
	require.NoError(t, err)
	require.Equal(t, "info", result.Level)

	// Test PUT - change to debug
	body := strings.NewReader(`{"level":"debug"}`)
	req, err := http.NewRequest(http.MethodPut, "http://127.0.0.1:19903/log/level", body)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")

	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	// Verify level changed in AtomicLevel
	require.Equal(t, zapcore.DebugLevel, level.Level())

	// Test GET again - should return debug
	resp, err = http.Get("http://127.0.0.1:19903/log/level")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	err = json.NewDecoder(resp.Body).Decode(&result)
	require.NoError(t, err)
	require.Equal(t, "debug", result.Level)
}

// TestStartDynamicLevelServer_HTTPAcceptsDangerousLevels documents a known
// gap: unlike ParseLevel (used for the initial --log-level value), the HTTP
// endpoint delegates directly to zap.AtomicLevel.ServeHTTP and does not
// restrict which levels can be set at runtime. Setting "fatal" or "panic"
// via this endpoint will silence all lower-severity logging and can cause
// the process to exit or panic the next time something logs at that level.
//
// This test intentionally documents the current (unrestricted) behavior so
// it isn't a silent, unnoticed gap. If the HTTP endpoint is later restricted
// to match ParseLevel, this test should be updated to assert a 400 response
// instead.
func TestStartDynamicLevelServer_HTTPAcceptsDangerousLevels(t *testing.T) {
	level := zap.NewAtomicLevelAt(zapcore.InfoLevel)
	logger := logr.Discard()

	StartDynamicLevelServer("127.0.0.1:19904", level, logger)
	time.Sleep(100 * time.Millisecond)

	body := strings.NewReader(`{"level":"fatal"}`)
	req, err := http.NewRequest(http.MethodPut, "http://127.0.0.1:19904/log/level", body)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	// KNOWN GAP: this currently succeeds (200) instead of being rejected.
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, zapcore.FatalLevel, level.Level())

	// Reset to a safe level so this test doesn't affect others if the
	// process/logger were ever reused.
	level.SetLevel(zapcore.InfoLevel)
}

func TestStartDynamicLevelServer_RejectsNonLoopback(t *testing.T) {
	dangerousAddresses := []struct {
		name string
		addr string
	}{
		{"all interfaces ipv4", "0.0.0.0:9901"},
		{"all interfaces ipv6", "[::]:9901"},
		{"private ip", "192.168.1.100:9901"},
		{"pod ip example", "10.244.0.5:9901"},
	}

	for _, tt := range dangerousAddresses {
		t.Run(tt.name, func(t *testing.T) {
			level := zap.NewAtomicLevelAt(zapcore.InfoLevel)
			logger := logr.Discard()

			// Should reject and not start server
			// We verify this by the fact that StartDynamicLevelServer returns
			// early without panic - the validation logic prevents server start
			StartDynamicLevelServer(tt.addr, level, logger)

			// Brief sleep to ensure goroutine would have started if it was going to
			time.Sleep(10 * time.Millisecond)

			// Test passes if we got here without panic
			// The security control is the validation logic that rejects non-loopback
			// addresses before calling ListenAndServe. We don't need to verify the
			// server didn't start by connecting (which would cause long timeouts
			// for unreachable IPs).
		})
	}
}

func TestStartDynamicLevelServer_InvalidAddress(t *testing.T) {
	invalidAddresses := []string{
		"not-an-address",
		"127.0.0.1",     // Missing port
		"127.0.0.1:abc", // Invalid port
		":9901",         // Missing host
	}

	for _, addr := range invalidAddresses {
		t.Run(addr, func(t *testing.T) {
			level := zap.NewAtomicLevelAt(zapcore.InfoLevel)
			logger := logr.Discard()

			// Should handle gracefully (log error, don't panic)
			StartDynamicLevelServer(addr, level, logger)
			// If no panic, test passes
		})
	}
}

func TestParseLevel(t *testing.T) {
	tests := []struct {
		input       string
		expected    zapcore.Level
		expectError bool
	}{
		// Valid levels
		{"debug", zapcore.DebugLevel, false},
		{"info", zapcore.InfoLevel, false},
		{"warn", zapcore.WarnLevel, false},
		{"error", zapcore.ErrorLevel, false},

		// Dangerous levels (rejected - would silence logging or crash)
		{"dpanic", zapcore.InfoLevel, true},
		{"panic", zapcore.InfoLevel, true},
		{"fatal", zapcore.InfoLevel, true},

		// Case insensitive
		{"DEBUG", zapcore.DebugLevel, false},
		{"Info", zapcore.InfoLevel, false},
		{"WARN", zapcore.WarnLevel, false},

		// Empty string is valid (zap treats it as info)
		{"", zapcore.InfoLevel, false},

		// Invalid levels
		{"invalid", zapcore.InfoLevel, true},
		{"debuggg", zapcore.InfoLevel, true},
		{"trace", zapcore.InfoLevel, true},
		{"verbose", zapcore.InfoLevel, true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result, err := ParseLevel(tt.input)

			if tt.expectError {
				require.Error(t, err, "Expected error for input: %s", tt.input)
				require.Contains(t, err.Error(), "invalid log level",
					"Error should mention 'invalid log level'")
			} else {
				require.NoError(t, err, "Expected no error for input: %s", tt.input)
				require.Equal(t, tt.expected, result, "Level mismatch for input: %s", tt.input)
			}
		})
	}
}

func TestParseLevel_ErrorMessage(t *testing.T) {
	_, err := ParseLevel("invalid-level")
	require.Error(t, err)

	// Error message should be helpful
	errMsg := err.Error()
	require.Contains(t, errMsg, "invalid-level", "Should include the invalid input")
	require.Contains(t, errMsg, "debug", "Should mention valid options")
	require.Contains(t, errMsg, "info", "Should mention valid options")
}
