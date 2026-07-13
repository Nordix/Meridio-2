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
	"fmt"
	"net"
	"net/http"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// StartDynamicLevelServer starts an HTTP server to serve zap.AtomicLevel
// for runtime log level changes via HTTP GET/PUT requests.
//
// If addr is empty, this is a no-op (feature disabled by default).
//
// The server runs in a goroutine and does not block startup. Errors are
// logged but do not cause the application to fail (this is an auxiliary
// operational feature).
//
// SECURITY: The endpoint MUST bind to loopback (127.0.0.1 or ::1) only.
// Non-loopback addresses are rejected to prevent network exposure of the
// unauthenticated endpoint. The security model assumes only trusted processes
// run within the same container.
//
// The endpoint implements the standard zap.AtomicLevel HTTP API:
//   - GET  /log/level  -> {"level":"info"}
//   - PUT  /log/level  <- {"level":"debug"}
//
// Valid log levels: debug, info, warn, error, dpanic, panic, fatal
func StartDynamicLevelServer(addr string, level zap.AtomicLevel, logger *zap.Logger) {
	if addr == "" {
		return // disabled by default
	}

	log := logger.Named("loglevel-api")

	// Parse and validate address
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		log.Error("Invalid log-level-api address",
			zap.String("addr", addr),
			zap.Error(err),
			zap.String("hint", "expected format: 127.0.0.1:9901"))
		return
	}

	// SECURITY: Reject non-loopback addresses
	ip := net.ParseIP(host)
	if ip == nil {
		log.Error("Invalid IP address in log-level-api",
			zap.String("host", host),
			zap.String("addr", addr))
		return
	}

	if !ip.IsLoopback() {
		log.Error("SECURITY VIOLATION: log-level-api must bind to loopback interface only",
			zap.String("rejected_address", addr),
			zap.String("correct_format_ipv4", "127.0.0.1:"+port),
			zap.String("correct_format_ipv6", "[::1]:"+port),
			zap.String("security_note", "non-loopback binding exposes unauthenticated endpoint to network"))
		return // FAIL SAFE: do not start server
	}

	// Create HTTP server with zap's built-in AtomicLevel handler
	mux := http.NewServeMux()
	mux.Handle("/log/level", level) // zap.AtomicLevel implements http.Handler

	server := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second, // Protect against slowloris attacks
	}

	// Start server in goroutine (non-blocking)
	go func() {
		log.Info("Log level API listening",
			zap.String("addr", addr),
			zap.String("endpoint", "http://"+addr+"/log/level"),
			zap.String("usage_get", "curl http://"+addr+"/log/level"),
			zap.String("usage_put", "curl -X PUT http://"+addr+"/log/level -d '{\"level\":\"debug\"}'"))

		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("Log level API server error", zap.Error(err))
			// Non-fatal: logging continues to work even if this auxiliary feature fails
		}
	}()
}

// ParseLevel parses a log level string to zapcore.Level.
// Returns an error if the level string is invalid.
//
// Valid levels: debug, info, warn, error, dpanic, panic, fatal (case-insensitive)
func ParseLevel(levelStr string) (zapcore.Level, error) {
	var level zapcore.Level
	if err := level.UnmarshalText([]byte(levelStr)); err != nil {
		return zapcore.InfoLevel, fmt.Errorf("invalid log level %q: valid options are debug, info, warn, error", levelStr)
	}
	return level, nil
}
