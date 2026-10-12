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

package config

import (
	"testing"
	"time"

	"github.com/spf13/pflag"
	"k8s.io/client-go/rest"
)

func newKubeAPIFlagSet(c *KubeAPIConfig) *pflag.FlagSet {
	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	c.AddKubeAPIFlags(fs)
	return fs
}

func TestKubeAPIConfig_Defaults(t *testing.T) {
	cfg := &KubeAPIConfig{}
	fs := newKubeAPIFlagSet(cfg)
	if err := fs.Parse(nil); err != nil {
		t.Fatalf("parse: %v", err)
	}
	cfg.BindKubeAPIEnv(fs)

	if cfg.CacheSyncTimeout != DefaultCacheSyncTimeout {
		t.Errorf("CacheSyncTimeout = %s, want %s", cfg.CacheSyncTimeout, DefaultCacheSyncTimeout)
	}
	// The default must preserve controller-runtime's behaviour: client-side
	// rate limiting disabled, relying on API Priority and Fairness.
	if cfg.RateLimitingEnabled() {
		t.Error("client-side rate limiting must be disabled by default")
	}
	restCfg := &rest.Config{}
	cfg.applyRateLimits(restCfg)
	if restCfg.QPS != DisabledKubeAPIQPS {
		t.Errorf("QPS = %v, want %v", restCfg.QPS, DisabledKubeAPIQPS)
	}
}

func TestKubeAPIConfig_FlagsOverrideEnv(t *testing.T) {
	t.Setenv("MERIDIO_CACHE_SYNC_TIMEOUT", "9m")
	t.Setenv("MERIDIO_KUBE_API_QPS", "11")
	t.Setenv("MERIDIO_KUBE_API_BURST", "22")

	cfg := &KubeAPIConfig{}
	fs := newKubeAPIFlagSet(cfg)
	if err := fs.Parse([]string{
		"--cache-sync-timeout=5m", "--kube-api-qps=50", "--kube-api-burst=100",
	}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	cfg.BindKubeAPIEnv(fs)

	if cfg.CacheSyncTimeout != 5*time.Minute {
		t.Errorf("CacheSyncTimeout = %s, want 5m", cfg.CacheSyncTimeout)
	}
	if cfg.KubeAPIQPS != 50 {
		t.Errorf("KubeAPIQPS = %v, want 50", cfg.KubeAPIQPS)
	}
	if cfg.KubeAPIBurst != 100 {
		t.Errorf("KubeAPIBurst = %d, want 100", cfg.KubeAPIBurst)
	}
}

func TestKubeAPIConfig_EnvOverridesDefaults(t *testing.T) {
	t.Setenv("MERIDIO_CACHE_SYNC_TIMEOUT", "7m30s")
	t.Setenv("MERIDIO_KUBE_API_QPS", "12.5")
	t.Setenv("MERIDIO_KUBE_API_BURST", "40")

	cfg := &KubeAPIConfig{}
	fs := newKubeAPIFlagSet(cfg)
	if err := fs.Parse(nil); err != nil {
		t.Fatalf("parse: %v", err)
	}
	cfg.BindKubeAPIEnv(fs)

	if cfg.CacheSyncTimeout != 7*time.Minute+30*time.Second {
		t.Errorf("CacheSyncTimeout = %s, want 7m30s", cfg.CacheSyncTimeout)
	}
	if cfg.KubeAPIQPS != 12.5 {
		t.Errorf("KubeAPIQPS = %v, want 12.5", cfg.KubeAPIQPS)
	}
	if cfg.KubeAPIBurst != 40 {
		t.Errorf("KubeAPIBurst = %d, want 40", cfg.KubeAPIBurst)
	}
	if !cfg.RateLimitingEnabled() {
		t.Error("expected rate limiting to be enabled with positive QPS")
	}
}

func TestKubeAPIConfig_ApplyRateLimits(t *testing.T) {
	tests := []struct {
		name      string
		qps       float32
		burst     int
		wantQPS   float32
		wantBurst int
	}{
		{"disabled by default", DisabledKubeAPIQPS, DefaultKubeAPIBurst, DisabledKubeAPIQPS, DefaultKubeAPIBurst},
		// Zero QPS would make client-go fall back to DefaultQPS (5), silently
		// throttling the client. It must be normalized to the disabled value.
		{"zero qps normalized to disabled", 0, DefaultKubeAPIBurst, DisabledKubeAPIQPS, DefaultKubeAPIBurst},
		{"positive qps enables limiter", 50, 100, 50, 100},
		{"zero burst leaves client-go default", 50, 0, 50, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &KubeAPIConfig{KubeAPIQPS: tt.qps, KubeAPIBurst: tt.burst}
			restCfg := &rest.Config{}
			cfg.applyRateLimits(restCfg)

			if restCfg.QPS != tt.wantQPS {
				t.Errorf("QPS = %v, want %v", restCfg.QPS, tt.wantQPS)
			}
			if restCfg.Burst != tt.wantBurst {
				t.Errorf("Burst = %d, want %d", restCfg.Burst, tt.wantBurst)
			}
		})
	}
}

func TestKubeAPIConfig_ValidateKubeAPI(t *testing.T) {
	tests := []struct {
		name    string
		cfg     KubeAPIConfig
		wantErr bool
	}{
		{"defaults are valid", KubeAPIConfig{CacheSyncTimeout: DefaultCacheSyncTimeout,
			KubeAPIQPS: DisabledKubeAPIQPS, KubeAPIBurst: DefaultKubeAPIBurst}, false},
		{"zero timeout means library default", KubeAPIConfig{}, false},
		{"negative timeout rejected", KubeAPIConfig{CacheSyncTimeout: -time.Second}, true},
		{"negative burst rejected", KubeAPIConfig{KubeAPIBurst: -1}, true},
		{"negative qps allowed (disables limiting)", KubeAPIConfig{KubeAPIQPS: -1}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.ValidateKubeAPI()
			if tt.wantErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("expected no error, got: %v", err)
			}
		})
	}
}

// The tuning knobs must be reachable through the embedding configs, since those
// are what the binaries actually parse.
func TestKubeAPIConfig_ExposedByBinaryConfigs(t *testing.T) {
	t.Run("manager", func(t *testing.T) {
		cfg := &ManagerConfig{}
		fs := pflag.NewFlagSet("manager", pflag.ContinueOnError)
		cfg.AddFlags(fs)
		if err := fs.Parse([]string{"--cache-sync-timeout=10m"}); err != nil {
			t.Fatalf("parse: %v", err)
		}
		cfg.BindEnv(fs)
		if cfg.CacheSyncTimeout != 10*time.Minute {
			t.Errorf("CacheSyncTimeout = %s, want 10m", cfg.CacheSyncTimeout)
		}
	})

	// The LB timeout is a separate manager-only knob: it is not applied to the
	// manager's own cache, it is propagated to the rendered LB Deployments.
	t.Run("manager lb-cache-sync-timeout", func(t *testing.T) {
		cfg := &ManagerConfig{}
		fs := pflag.NewFlagSet("manager", pflag.ContinueOnError)
		cfg.AddFlags(fs)
		if err := fs.Parse(nil); err != nil {
			t.Fatalf("parse: %v", err)
		}
		cfg.BindEnv(fs)
		if cfg.LBCacheSyncTimeout != 0 {
			t.Errorf("LBCacheSyncTimeout = %s, want 0 (leave the LB template untouched)", cfg.LBCacheSyncTimeout)
		}

		t.Setenv("MERIDIO_LB_CACHE_SYNC_TIMEOUT", "8m")
		cfg = &ManagerConfig{}
		fs = pflag.NewFlagSet("manager", pflag.ContinueOnError)
		cfg.AddFlags(fs)
		if err := fs.Parse(nil); err != nil {
			t.Fatalf("parse: %v", err)
		}
		cfg.BindEnv(fs)
		if cfg.LBCacheSyncTimeout != 8*time.Minute {
			t.Errorf("LBCacheSyncTimeout = %s, want 8m", cfg.LBCacheSyncTimeout)
		}
		if cfg.CacheSyncTimeout != DefaultCacheSyncTimeout {
			t.Errorf("CacheSyncTimeout = %s, want %s — the LB knob must not affect the manager's own cache",
				cfg.CacheSyncTimeout, DefaultCacheSyncTimeout)
		}
	})

	t.Run("loadbalancer", func(t *testing.T) {
		cfg := &LoadBalancerConfig{}
		fs := pflag.NewFlagSet("lb", pflag.ContinueOnError)
		cfg.AddFlags(fs)
		if err := fs.Parse([]string{"--kube-api-qps=25", "--kube-api-burst=60"}); err != nil {
			t.Fatalf("parse: %v", err)
		}
		cfg.BindEnv(fs)
		if cfg.KubeAPIQPS != 25 || cfg.KubeAPIBurst != 60 {
			t.Errorf("got qps=%v burst=%d, want 25/60", cfg.KubeAPIQPS, cfg.KubeAPIBurst)
		}
		if cfg.CacheSyncTimeout != DefaultCacheSyncTimeout {
			t.Errorf("CacheSyncTimeout = %s, want %s", cfg.CacheSyncTimeout, DefaultCacheSyncTimeout)
		}
	})
}
