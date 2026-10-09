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

package httpsec

import (
	"strings"
	"sync"
	"testing"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/funcr"
	"github.com/stretchr/testify/require"
)

func TestValidateLoopbackAddr_AcceptsLoopback(t *testing.T) {
	valid := []struct {
		name string
		addr string
	}{
		{"ipv4 loopback", "127.0.0.1:6060"},
		{"ipv4 loopback other port", "127.0.0.1:9901"},
		{"ipv4 loopback range", "127.0.0.2:6060"},
		{"ipv6 loopback", "[::1]:6060"},
	}

	for _, tt := range valid {
		t.Run(tt.name, func(t *testing.T) {
			require.NoError(t, ValidateLoopbackAddr(tt.addr))
		})
	}
}

func TestValidateLoopbackAddr_RejectsNonLoopback(t *testing.T) {
	dangerous := []struct {
		name string
		addr string
	}{
		{"all interfaces ipv4", "0.0.0.0:6060"},
		{"all interfaces ipv6", "[::]:6060"},
		{"private ip", "192.168.1.100:6060"},
		{"pod ip example", "10.244.0.5:6060"},
	}

	for _, tt := range dangerous {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateLoopbackAddr(tt.addr)
			require.Error(t, err)
			require.Contains(t, err.Error(), "non-loopback",
				"error should explain the loopback requirement")
		})
	}
}

func TestValidateLoopbackAddr_RejectsMalformed(t *testing.T) {
	malformed := []struct {
		name string
		addr string
	}{
		{"missing port", "127.0.0.1"},
		{"non-numeric port", "127.0.0.1:abc"},
		{"port out of range", "127.0.0.1:70000"},
		{"hostname not ip", "localhost:6060"},
		{"empty host", ":6060"},
		{"garbage", "not-an-address"},
	}

	for _, tt := range malformed {
		t.Run(tt.name, func(t *testing.T) {
			require.Error(t, ValidateLoopbackAddr(tt.addr))
		})
	}
}

// capturingLogger returns a logr.Logger backed by funcr that appends every
// formatted log line to lines (guarded by mu), so tests can assert the real
// log output ResolvePprofBindAddress emits, not just its return value.
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

func TestResolvePprofBindAddress_EmptyIsDisabledSilently(t *testing.T) {
	var mu sync.Mutex
	var lines []string
	logger := capturingLogger(&mu, &lines)

	got := ResolvePprofBindAddress("", logger)
	require.Equal(t, "", got, "empty address must stay disabled")

	mu.Lock()
	defer mu.Unlock()
	require.Empty(t, lines, "disabled-by-default must not log anything")
}

func TestResolvePprofBindAddress_ValidLoopbackPassesThrough(t *testing.T) {
	valid := []string{"127.0.0.1:6060", "[::1]:6060"}

	for _, addr := range valid {
		t.Run(addr, func(t *testing.T) {
			var mu sync.Mutex
			var lines []string
			logger := capturingLogger(&mu, &lines)

			got := ResolvePprofBindAddress(addr, logger)
			require.Equal(t, addr, got, "valid loopback address must pass through unchanged")

			mu.Lock()
			defer mu.Unlock()
			found := false
			for _, l := range lines {
				if strings.Contains(l, "pprof endpoint enabled") {
					found = true
					require.Contains(t, l, "pprof", "logger should be named pprof")
					break
				}
			}
			require.True(t, found, "expected an 'enabled' log line, got: %v", lines)
		})
	}
}

func TestResolvePprofBindAddress_NonLoopbackIsFailSafeDisabled(t *testing.T) {
	dangerous := []string{"0.0.0.0:6060", "[::]:6060", "192.168.1.100:6060", "10.244.0.5:6060"}

	for _, addr := range dangerous {
		t.Run(addr, func(t *testing.T) {
			var mu sync.Mutex
			var lines []string
			logger := capturingLogger(&mu, &lines)

			got := ResolvePprofBindAddress(addr, logger)
			require.Equal(t, "", got, "non-loopback address must be disabled (fail-safe)")

			mu.Lock()
			defer mu.Unlock()
			found := false
			for _, l := range lines {
				if strings.Contains(l, "Refusing to enable pprof endpoint") {
					found = true
					break
				}
			}
			require.True(t, found, "expected a refusal log line, got: %v", lines)
		})
	}
}

func TestResolvePprofBindAddress_MalformedIsFailSafeDisabled(t *testing.T) {
	malformed := []string{"127.0.0.1", "127.0.0.1:abc", "localhost:6060", "garbage"}

	for _, addr := range malformed {
		t.Run(addr, func(t *testing.T) {
			var mu sync.Mutex
			var lines []string
			logger := capturingLogger(&mu, &lines)

			got := ResolvePprofBindAddress(addr, logger)
			require.Equal(t, "", got, "malformed address must be disabled (fail-safe)")

			mu.Lock()
			defer mu.Unlock()
			require.NotEmpty(t, lines, "a rejected address must be logged")
		})
	}
}
