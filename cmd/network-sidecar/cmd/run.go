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

package cmd

import (
	"crypto/tls"
	"fmt"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	meridio2v1alpha1 "github.com/nordix/meridio-2/api/v1alpha1"
	"github.com/nordix/meridio-2/internal/common/config"
	"github.com/nordix/meridio-2/internal/controller/sidecar"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(meridio2v1alpha1.AddToScheme(scheme))
}

func newCmdRun() *cobra.Command {
	cfg := &config.SidecarConfig{}

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run the network sidecar controller",
		Long:  `Run the network sidecar controller to configure VIPs and source-based routing`,
		PreRunE: func(cmd *cobra.Command, args []string) error {
			cfg.BindEnv(cmd.Flags())
			zapOpts := zap.Options{Development: cfg.LogLevel == "debug"}
			ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zapOpts)))
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSidecar(cfg)
		},
	}

	cfg.AddFlags(cmd.Flags())

	return cmd
}

func runSidecar(cfg *config.SidecarConfig) error {
	setupLog.Info("Starting sidecar controller", "config", cfg)

	if cfg.PodName == "" || cfg.PodNamespace == "" || cfg.PodUID == "" {
		return fmt.Errorf("pod-name, pod-namespace and pod-uid are required")
	}

	var tlsOpts []func(*tls.Config)
	if !cfg.EnableHTTP2 {
		tlsOpts = append(tlsOpts, func(c *tls.Config) {
			c.NextProtos = []string{"http/1.1"}
		})
	}

	metricsServerOptions := metricsserver.Options{
		BindAddress:   cfg.MetricsAddr,
		SecureServing: cfg.SecureMetrics,
		TLSOpts:       tlsOpts,
	}
	if cfg.SecureMetrics {
		metricsServerOptions.FilterProvider = filters.WithAuthenticationAndAuthorization
		if cfg.MetricsCertPath != "" {
			metricsServerOptions.CertDir = cfg.MetricsCertPath
			metricsServerOptions.CertName = cfg.MetricsCertName
			metricsServerOptions.KeyName = cfg.MetricsCertKey
		}
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:         scheme,
		LeaderElection: false,
		Cache: cache.Options{
			DefaultNamespaces: map[string]cache.Config{
				cfg.PodNamespace: {},
			},
			ByObject: map[client.Object]cache.ByObject{
				&meridio2v1alpha1.EndpointNetworkConfiguration{}: {
					Field: fields.OneTermEqualSelector("metadata.name", cfg.PodName),
				},
			},
		},
		Metrics:                metricsServerOptions,
		HealthProbeBindAddress: cfg.ProbeAddr,
	})
	if err != nil {
		return fmt.Errorf("failed to create manager: %w", err)
	}

	if err := (&sidecar.Controller{
		Client:     mgr.GetClient(),
		Scheme:     mgr.GetScheme(),
		PodName:    cfg.PodName,
		PodUID:     cfg.PodUID,
		MinTableID: cfg.MinTableID,
		MaxTableID: cfg.MaxTableID,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("failed to setup controller: %w", err)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return fmt.Errorf("unable to set up health check: %w", err)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return fmt.Errorf("unable to set up ready check: %w", err)
	}

	setupLog.Info("starting manager", "podName", cfg.PodName, "podNamespace", cfg.PodNamespace)
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		return fmt.Errorf("problem running manager: %w", err)
	}

	return nil
}
