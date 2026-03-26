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
	"context"
	"crypto/tls"
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	meridio2v1alpha1 "github.com/nordix/meridio-2/api/v1alpha1"
	"github.com/nordix/meridio-2/internal/bird"
	"github.com/nordix/meridio-2/internal/common/config"
	"github.com/nordix/meridio-2/internal/controller/router"
)

// NewRunCmd creates the run subcommand
func NewRunCmd() *cobra.Command {
	cfg := &config.RouterConfig{}

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run the router",
		Long:  `Run the router controller`,
		PreRunE: func(cmd *cobra.Command, args []string) error {
			cfg.BindEnv(cmd.Flags())
			// Setup logger based on log level
			zapOpts := zap.Options{Development: cfg.LogLevel == "debug"}
			ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zapOpts)))
			return nil
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runRouter(cmd.Context(), cfg)
		},
	}

	cfg.AddFlags(cmd.Flags())

	return cmd
}

func runRouter(ctx context.Context, cfg *config.RouterConfig) error {
	scheme := runtime.NewScheme()
	setupLog := ctrl.Log.WithName("setup")

	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(meridio2v1alpha1.AddToScheme(scheme))
	utilruntime.Must(gatewayapiv1.Install(scheme))

	// Validate required fields
	if cfg.GatewayName == "" || cfg.GatewayNamespace == "" {
		return fmt.Errorf("gateway-name and gateway-namespace are required")
	}

	setupLog.Info("Starting Router controller", "config", cfg)

	// Configure TLS options
	var tlsOpts []func(*tls.Config)
	if !cfg.EnableHTTP2 {
		disableHTTP2 := func(c *tls.Config) {
			setupLog.Info("disabling http/2")
			c.NextProtos = []string{"http/1.1"}
		}
		tlsOpts = append(tlsOpts, disableHTTP2)
	}

	// Metrics options
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
				cfg.GatewayNamespace: {},
			},
		},
		Metrics:                metricsServerOptions,
		HealthProbeBindAddress: cfg.ProbeAddr,
	})
	if err != nil {
		setupLog.Error(err, "failed to create manager")
		return err
	}

	birdInstance := bird.New(bird.WithLogFile(cfg.BirdLogFile), bird.WithLogFileSize(cfg.BirdLogFileSize))

	if err = (&router.RouterReconciler{
		Client:           mgr.GetClient(),
		Scheme:           mgr.GetScheme(),
		GatewayName:      cfg.GatewayName,
		GatewayNamespace: cfg.GatewayNamespace,
		Bird:             birdInstance,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "failed to create controller", "controller", "GatewayRouter")
		return err
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		return err
	}

	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		return err
	}

	setupLog.Info("starting router", "gateway", cfg.GatewayName, "namespace", cfg.GatewayNamespace)

	ctx = ctrl.SetupSignalHandler()

	go func() {
		if err := birdInstance.Run(ctx); err != nil {
			setupLog.Error(err, "BIRD stopped")
		}
	}()

	// Start monitoring BGP connectivity
	go monitorConnectivity(ctx, mgr, birdInstance)

	if err := mgr.Start(ctx); err != nil {
		setupLog.Error(err, "problem running manager")
		return err
	}

	return nil
}

// monitorConnectivity monitors BGP connectivity and logs status changes
func monitorConnectivity(ctx context.Context, mgr ctrl.Manager, birdInstance *bird.Bird) {
	log := ctrl.Log.WithName("monitor")

	// Wait for manager cache to sync
	if !mgr.GetCache().WaitForCacheSync(ctx) {
		log.Error(nil, "failed to wait for cache sync")
		return
	}

	// Start monitoring with 1 second interval
	statusCh, err := birdInstance.Monitor(ctx, 1*time.Second)
	if err != nil {
		log.Error(err, "failed to start monitoring")
		return
	}

	var lastCount int
	firstUpdate := true

	for {
		select {
		case <-ctx.Done():
			return
		case status, ok := <-statusCh:
			if !ok {
				return
			}

			count := protocolsUp(status.Protocols)

			// Log when protocol up count changes
			if firstUpdate || count != lastCount {
				if status.HasConnectivity {
					log.Info("Gateway connectivity established", "status", status.StatusString())
				} else {
					log.Info("Gateway connectivity lost", "status", status.StatusString())
				}

				lastCount = count
				firstUpdate = false
			}
		}
	}
}

func protocolsUp(protocols []bird.ProtocolStatus) int {
	count := 0
	for _, p := range protocols {
		if p.IsEstablished() {
			count++
		}
	}
	return count
}
