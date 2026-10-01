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
