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
	"golang.org/x/sync/errgroup"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
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
	if cfg.BirdKernelScanTime < 1 {
		return fmt.Errorf("bird-kernel-scan-time must be at least 1 second (got %d)", cfg.BirdKernelScanTime)
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

	birdInstance := bird.New(bird.WithLogParams(cfg.BirdLogs), bird.WithKernelScanTime(cfg.BirdKernelScanTime))

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
	g, ctx := errgroup.WithContext(ctx)

	g.Go(func() error {
		if err := birdInstance.Run(ctx); err != nil {
			return fmt.Errorf("BIRD stopped: %w", err)
		}
		if ctx.Err() == nil {
			return fmt.Errorf("BIRD exited unexpectedly")
		}
		return nil
	})

	g.Go(func() error {
		return monitorConnectivity(ctx, mgr, birdInstance, cfg)
	})

	g.Go(func() error {
		return mgr.Start(ctx)
	})

	if err := g.Wait(); err != nil {
		setupLog.Error(err, "router exited")
		return err
	}

	return nil
}

// monitorConnectivity monitors BGP connectivity and manages readiness gates
func monitorConnectivity(
	ctx context.Context, mgr ctrl.Manager, birdInstance *bird.Bird, cfg *config.RouterConfig,
) error {
	log := ctrl.Log.WithName("monitor")

	// Wait for manager cache to sync
	if !mgr.GetCache().WaitForCacheSync(ctx) {
		return fmt.Errorf("failed to wait for cache sync")
	}

	// Initialize connectivity gate manager (if Pod identity is configured)
	var gateMgr *router.ConnectivityGateManager
	if cfg.PodName != "" && cfg.PodNamespace != "" {
		gateMgr = router.NewConnectivityGateManager(
			mgr.GetClient(), cfg.PodName, cfg.PodNamespace,
			types.UID(cfg.PodUID), cfg.ConnectivityHoldTime,
		)
		if err := gateMgr.DiscoverGates(ctx); err != nil {
			log.Error(err, "failed to discover readiness gates, continuing without gate management")
			gateMgr = nil
		} else if gateMgr.HasIPv4Gate() || gateMgr.HasIPv6Gate() {
			log.Info("readiness gates discovered", "ipv4", gateMgr.HasIPv4Gate(), "ipv6", gateMgr.HasIPv6Gate())
			if err := gateMgr.SetAllGatesFalse(ctx); err != nil {
				log.Error(err, "failed to set gates to False on startup")
			}
		} else {
			gateMgr = nil // No gates declared, skip gate management
		}
	}

	// Start monitoring with 1 second interval
	statusCh, err := birdInstance.Monitor(ctx, 1*time.Second)
	if err != nil {
		return fmt.Errorf("failed to start monitoring: %w", err)
	}

	// Build family map from GatewayRouters (refreshed on each status update)
	var familyMap map[string]string
	var lastCount int
	firstUpdate := true

	for {
		select {
		case <-ctx.Done():
			return nil
		case status, ok := <-statusCh:
			if !ok {
				if ctx.Err() != nil {
					return nil
				}
				return fmt.Errorf("monitor channel closed unexpectedly")
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

			// Update readiness gates
			if gateMgr != nil {
				// Rebuild family map if nil or if protocols exist but map yields no matches
				// (handles late GatewayRouter creation)
				if familyMap == nil || (len(status.Protocols) > 0 && len(familyMap) == 0) {
					routers, err := getGatewayRoutersFromCache(ctx, mgr.GetClient(), cfg.GatewayName, cfg.GatewayNamespace)
					if err == nil && len(routers) > 0 {
						familyMap = router.BuildFamilyMap(routers)
					}
				}
				if familyMap != nil {
					ipv4, ipv6 := router.ClassifyConnectivityByFamily(status.Protocols, familyMap)
					if err := gateMgr.OnStatusUpdate(ctx, ipv4, ipv6); err != nil {
						log.Error(err, "failed to update readiness gates")
					}
				}
			}
		}
	}
}

func getGatewayRoutersFromCache(
	ctx context.Context, c client.Client, gatewayName, gatewayNamespace string,
) ([]*meridio2v1alpha1.GatewayRouter, error) {
	list := &meridio2v1alpha1.GatewayRouterList{}
	if err := c.List(ctx, list, client.InNamespace(gatewayNamespace)); err != nil {
		return nil, err
	}
	var result []*meridio2v1alpha1.GatewayRouter
	for i := range list.Items {
		ref := list.Items[i].Spec.GatewayRef
		ns := list.Items[i].Namespace
		if ref.Namespace != nil {
			ns = string(*ref.Namespace)
		}
		if string(ref.Name) == gatewayName && ns == gatewayNamespace {
			result = append(result, &list.Items[i])
		}
	}
	return result, nil
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
