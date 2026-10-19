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

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/require"
)

// pprofConfig is the minimal surface every config struct shares for the pprof
// endpoint, so the flag/env binding behavior can be exercised uniformly for
// all binaries exposing pprof without duplicating the table per config type.
type pprofConfig interface {
	AddFlags(fs *pflag.FlagSet)
	BindEnv(fs *pflag.FlagSet)
}

// newPprofConfigs returns one fresh instance of each config type together with
// a getter for its resolved PprofBindAddress field, so each subtest starts from
// a clean struct (BindEnv/flag parsing mutate it).
func newPprofConfigs() []struct {
	name string
	cfg  pprofConfig
	get  func() string
} {
	mgr := &ManagerConfig{}
	lb := &LoadBalancerConfig{}
	sc := &SidecarConfig{}
	rt := &RouterConfig{}

	return []struct {
		name string
		cfg  pprofConfig
		get  func() string
	}{
		{"ManagerConfig", mgr, func() string { return mgr.PprofBindAddress }},
		{"LoadBalancerConfig", lb, func() string { return lb.PprofBindAddress }},
		{"SidecarConfig", sc, func() string { return sc.PprofBindAddress }},
		{"RouterConfig", rt, func() string { return rt.PprofBindAddress }},
	}
}

// parse builds a FlagSet for cfg, parses args into it, then applies env binding
// (mirroring the PreRunE order in every binary: AddFlags at wiring time,
// BindEnv after flag parsing).
func parse(t *testing.T, cfg pprofConfig, args []string) {
	t.Helper()
	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	cfg.AddFlags(fs)
	require.NoError(t, fs.Parse(args))
	cfg.BindEnv(fs)
}

func TestPprofBindAddress_DefaultEmpty(t *testing.T) {
	for _, tc := range newPprofConfigs() {
		t.Run(tc.name, func(t *testing.T) {
			parse(t, tc.cfg, nil)
			require.Equal(t, "", tc.get(), "pprof must be disabled (empty) by default")
		})
	}
}

func TestPprofBindAddress_FlagParsed(t *testing.T) {
	for _, tc := range newPprofConfigs() {
		t.Run(tc.name, func(t *testing.T) {
			parse(t, tc.cfg, []string{"--pprof-bind-address", "127.0.0.1:6060"})
			require.Equal(t, "127.0.0.1:6060", tc.get())
		})
	}
}

func TestPprofBindAddress_EnvBound(t *testing.T) {
	for _, tc := range newPprofConfigs() {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("MERIDIO_PPROF_ADDR", "127.0.0.1:7070")
			parse(t, tc.cfg, nil)
			require.Equal(t, "127.0.0.1:7070", tc.get(),
				"MERIDIO_PPROF_ADDR should bind when the flag is not set")
		})
	}
}

func TestPprofBindAddress_FlagOverridesEnv(t *testing.T) {
	for _, tc := range newPprofConfigs() {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("MERIDIO_PPROF_ADDR", "127.0.0.1:7070")
			parse(t, tc.cfg, []string{"--pprof-bind-address", "127.0.0.1:6060"})
			require.Equal(t, "127.0.0.1:6060", tc.get(),
				"explicit flag must take precedence over the env var")
		})
	}
}
