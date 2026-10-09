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

// Package httpsec holds shared, security-sensitive validation for the
// auxiliary HTTP endpoints Meridio-2 exposes.  These endpoints are
// unauthenticated.  The security model is loopback-only binding plus
// `kubectl port-forward` — only someone who can already reach into the
// Pod gets access.
//
// Its current users:
//   - the dynamic log-level API
//   - the pprof endpoint
//
// Their bind accepts whatever address they are given and do not enforce
// loopback, so the config/validation layer must reject non-loopback values
// up front.
package httpsec

import (
	"fmt"
	"net"
	"strconv"

	"github.com/go-logr/logr"
)

// ValidateLoopbackAddr validates that addr is a well-formed host:port whose
// host is an IP address bound to the loopback interface (127.0.0.0/8 or ::1).
//
// Callers MUST treat a non-nil error as "do not start/enable the endpoint".
//
// An empty addr is the caller's "disabled" sentinel and is NOT handled here;
// callers should short-circuit on "" before calling this function.
//
// The returned error is descriptive enough to log directly; it does not wrap
// a sentinel, as callers only need the message and the fail-safe signal.
func ValidateLoopbackAddr(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid address %q: expected host:port format like 127.0.0.1:9901: %w", addr, err)
	}

	if _, err := strconv.ParseUint(port, 10, 16); err != nil {
		return fmt.Errorf("invalid port %q in address %q: expected a number in 0-65535: %w", port, addr, err)
	}

	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("invalid IP %q in address %q: host must be a loopback IP literal (127.0.0.1 or ::1), not a hostname", host, addr)
	}

	if !ip.IsLoopback() {
		return fmt.Errorf("non-loopback address %q rejected: unauthenticated endpoint must bind to loopback only "+
			"(use 127.0.0.1:%s or [::1]:%s and reach it via kubectl port-forward)", addr, port, port)
	}

	return nil
}

// ResolvePprofBindAddress applies the shared fail-safe policy for the pprof
// endpoint and returns the address a binary should hand to
// controller-runtime's manager.Options.PprofBindAddress.
//
// pprof is unauthenticated and lets callers dump memory and trigger expensive
// profiles, so it follows the same opt-in, loopback-only model as the dynamic
// log-level server:
//
//   - An empty addr means the feature is disabled (the default); it returns ""
//     without logging.
//   - A non-loopback or malformed addr is rejected via ValidateLoopbackAddr;
//     the error is logged and "" is returned so the manager starts with pprof
//     disabled rather than exposing the endpoint on the network. (controller-
//     runtime's PprofBindAddress binds whatever it is given and enforces no
//     loopback and no auth, so this is the only gate.)
//   - A valid loopback addr is returned unchanged and a startup line is logged.
//
// Returning "" (never an error) keeps pprof an auxiliary, non-fatal feature:
// a bad value disables profiling but never prevents the process from starting.
func ResolvePprofBindAddress(addr string, logger logr.Logger) string {
	if addr == "" {
		return "" // disabled by default
	}

	log := logger.WithName("pprof")

	if err := ValidateLoopbackAddr(addr); err != nil {
		log.Error(err, "Refusing to enable pprof endpoint",
			"addr", addr,
			"hint", "expected a loopback host:port like 127.0.0.1:6060 or [::1]:6060")
		return "" // FAIL SAFE: do not enable pprof
	}

	log.Info("pprof endpoint enabled",
		"addr", addr,
		"security_note", "unauthenticated; loopback-only, reach it via kubectl port-forward",
		"usage", "kubectl port-forward <pod> "+portOf(addr)+":"+portOf(addr)+
			" then: go tool pprof http://"+addr+"/debug/pprof/heap")

	return addr
}

// portOf returns the port component of a validated host:port address, or the
// address itself if it cannot be split (ValidateLoopbackAddr already ran, so
// this is purely defensive for the log line).
func portOf(addr string) string {
	if _, port, err := net.SplitHostPort(addr); err == nil {
		return port
	}
	return addr
}
