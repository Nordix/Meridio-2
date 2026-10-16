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
	"fmt"
	"time"

	"github.com/spf13/pflag"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
)

const (
	// DefaultCacheSyncTimeout mirrors controller-runtime's own default
	// (pkg/controller: 2 minutes) so that logging and documentation can report
	// the effective value without reaching into the library internals.
	DefaultCacheSyncTimeout = 2 * time.Minute

	// DisabledKubeAPIQPS disables client-side rate limiting. controller-runtime's
	// GetConfig() applies this value by default, relying on server-side API
	// Priority and Fairness instead.
	DisabledKubeAPIQPS float32 = -1

	// DefaultKubeAPIBurst is only consulted when client-side rate limiting is
	// enabled (QPS > 0). It is ignored while rate limiting is disabled.
	DefaultKubeAPIBurst = 30
)

// KubeAPIConfig holds the Kubernetes API client and informer cache tuning knobs
// shared by the Meridio-2 binaries that run a controller-runtime manager.
//
// These settings exist for large-scale deployments where the initial informer
// List calls (thousands of objects) may not complete within controller-runtime's
// default cache sync deadline, especially when the container is CPU throttled.
type KubeAPIConfig struct {
	// CacheSyncTimeout is the time limit for the initial informer cache sync of
	// every controller registered with the manager. Zero means "use
	// controller-runtime's default" (2 minutes).
	CacheSyncTimeout time.Duration

	// KubeAPIQPS is the sustained client-side request rate towards the API
	// server. Values <= 0 disable client-side rate limiting entirely.
	KubeAPIQPS float32

	// KubeAPIBurst is the client-side burst allowance. Only effective when
	// KubeAPIQPS > 0.
	KubeAPIBurst int
}

// AddKubeAPIFlags registers the shared cache/API client tuning flags.
func (c *KubeAPIConfig) AddKubeAPIFlags(fs *pflag.FlagSet) {
	fs.DurationVar(&c.CacheSyncTimeout, "cache-sync-timeout", DefaultCacheSyncTimeout,
		"Time limit for the initial informer cache sync of each controller. Increase for large "+
			"deployments (e.g. thousands of L34Routes) where listing and deserializing all objects "+
			"does not finish within the deadline. 0 uses the controller-runtime default (2m).")
	fs.Float32Var(&c.KubeAPIQPS, "kube-api-qps", DisabledKubeAPIQPS,
		"Sustained client-side request rate towards the API server. Values <= 0 disable client-side "+
			"rate limiting (the default), relying on server-side API Priority and Fairness. Set a "+
			"positive value only to deliberately throttle this client.")
	fs.IntVar(&c.KubeAPIBurst, "kube-api-burst", DefaultKubeAPIBurst,
		"Client-side burst allowance towards the API server. Only effective when --kube-api-qps > 0.")
}

// BindKubeAPIEnv binds environment variables for the shared tuning flags.
func (c *KubeAPIConfig) BindKubeAPIEnv(fs *pflag.FlagSet) {
	bindDuration(fs, "cache-sync-timeout", "MERIDIO_CACHE_SYNC_TIMEOUT", &c.CacheSyncTimeout)
	bindFloat32(fs, "kube-api-qps", "MERIDIO_KUBE_API_QPS", &c.KubeAPIQPS)
	bindInt(fs, "kube-api-burst", "MERIDIO_KUBE_API_BURST", &c.KubeAPIBurst)
}

// ValidateKubeAPI checks the shared tuning values.
func (c *KubeAPIConfig) ValidateKubeAPI() error {
	if c.CacheSyncTimeout < 0 {
		return fmt.Errorf("cache-sync-timeout cannot be negative (got %s)", c.CacheSyncTimeout)
	}
	if c.KubeAPIBurst < 0 {
		return fmt.Errorf("kube-api-burst cannot be negative (got %d)", c.KubeAPIBurst)
	}
	return nil
}

// RESTConfig loads the Kubernetes REST configuration and applies the client-side
// rate limiting settings.
//
// Note on defaults: controller-runtime's GetConfig() sets QPS to -1 (rate
// limiting disabled) whenever QPS is left at zero, because client-go treats a
// zero QPS as "use DefaultQPS (5)". Keeping the disabled value is therefore the
// behaviour-preserving default; a zero or negative flag value is normalized to
// -1 so a zero never silently becomes a 5 QPS throttle.
func (c *KubeAPIConfig) RESTConfig() (*rest.Config, error) {
	restCfg, err := ctrl.GetConfig()
	if err != nil {
		return nil, fmt.Errorf("unable to load Kubernetes client configuration: %w", err)
	}

	c.applyRateLimits(restCfg)

	return restCfg, nil
}

// applyRateLimits normalizes and applies the client-side rate limiting settings.
func (c *KubeAPIConfig) applyRateLimits(restCfg *rest.Config) {
	if c.KubeAPIQPS <= 0 {
		restCfg.QPS = DisabledKubeAPIQPS
	} else {
		restCfg.QPS = c.KubeAPIQPS
	}
	if c.KubeAPIBurst > 0 {
		restCfg.Burst = c.KubeAPIBurst
	}
}

// RateLimitingEnabled reports whether client-side rate limiting is in effect.
// Useful for startup logging.
func (c *KubeAPIConfig) RateLimitingEnabled() bool {
	return c.KubeAPIQPS > 0
}
