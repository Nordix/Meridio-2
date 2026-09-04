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

package app

import (
	"testing"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/nordix/meridio-2/internal/common/config"
	"github.com/nordix/meridio-2/internal/common/metrics"
)

func TestValidate_InvalidPodCacheLabel(t *testing.T) {
	tests := []struct {
		name    string
		label   string
		wantErr bool
	}{
		{"empty is valid", "", false},
		{"valid key=value", "app=meridio", false},
		{"missing value", "app=", true},
		{"missing key", "=value", true},
		{"no equals sign", "appmeridio", true},
		{"multiple equals", "app=foo=bar", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validatePodCacheLabel(tt.label)
			if tt.wantErr && err == nil {
				t.Errorf("expected error for %q, got nil", tt.label)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("expected no error for %q, got: %v", tt.label, err)
			}
		})
	}
}

func TestValidate_CertWaitTimeoutExceedsMax(t *testing.T) {
	cfg := &config.ManagerConfig{
		CertWaitTimeout: 2 * time.Minute,
		MetricsPrefix:   metrics.DefaultPrefix,
	}
	err := validateConfig(cfg)
	if err == nil {
		t.Error("expected cert-wait-timeout error, got nil")
	}
}

func TestValidate_CertWaitTimeoutValid(t *testing.T) {
	cfg := &config.ManagerConfig{
		CertWaitTimeout: 30 * time.Second,
		MetricsPrefix:   metrics.DefaultPrefix,
	}
	err := validateConfig(cfg)
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestValidate_MetricsPrefixInvalid(t *testing.T) {
	cfg := &config.ManagerConfig{
		MetricsPrefix: "0invalid",
	}
	err := validateConfig(cfg)
	if err == nil {
		t.Error("expected metrics-prefix error, got nil")
	}
}

func TestValidate_MetricsPrefixValid(t *testing.T) {
	cfg := &config.ManagerConfig{
		MetricsPrefix: metrics.DefaultPrefix,
	}
	err := validateConfig(cfg)
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestControllerSetupType(t *testing.T) {
	// Compile-time check: ControllerSetup matches the expected pattern.
	var _ ControllerSetup = func(mgr ctrl.Manager, cfg Config) error {
		_ = mgr
		_ = cfg.Namespace
		_ = cfg.ControllerName
		return nil
	}
}

func TestNewCommand_CreatesCommand(t *testing.T) {
	cmd := NewCommand()
	if cmd == nil {
		t.Fatal("NewCommand returned nil")
	}
	if cmd.Use != "run" {
		t.Errorf("expected Use='run', got %q", cmd.Use)
	}
	if cmd.Flags().Lookup("namespace") == nil {
		t.Error("expected --namespace flag to be registered")
	}
	if cmd.Flags().Lookup("controller-name") == nil {
		t.Error("expected --controller-name flag to be registered")
	}
}

// TestRegisterMetricsCollectors_DisabledIsNoOp confirms registerMetricsCollectors returns
// immediately without touching mgr when metrics are disabled (--metrics-bind-address "0"), the
// project-wide sentinel for "metrics off". This is checked with mgr left nil: if the disabled
// gate were ever removed or reordered after a call that dereferences mgr, this test would panic
// rather than silently pass, which is a stronger signal than passing a non-nil mgr and merely
// asserting "no error".
func TestRegisterMetricsCollectors_DisabledIsNoOp(t *testing.T) {
	cfg := &config.ManagerConfig{MetricsAddr: "0"}

	err := registerMetricsCollectors(nil, cfg)
	if err != nil {
		t.Errorf("expected no error when metrics disabled, got: %v", err)
	}
}
