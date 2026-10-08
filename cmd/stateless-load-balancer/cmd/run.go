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

	"github.com/spf13/cobra"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	ctrlzap "sigs.k8s.io/controller-runtime/pkg/log/zap"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	meridio2v1alpha1 "github.com/nordix/meridio-2/api/v1alpha1"
	"github.com/nordix/meridio-2/internal/common/config"
	"github.com/nordix/meridio-2/internal/common/log"
	commonmetrics "github.com/nordix/meridio-2/internal/common/metrics"
	"github.com/nordix/meridio-2/internal/common/readiness"
	"github.com/nordix/meridio-2/internal/controller/loadbalancer"
	lbmetrics "github.com/nordix/meridio-2/internal/metrics/loadbalancer"
	"github.com/nordix/meridio-2/internal/nfqlb"
	nftablesmanager "github.com/nordix/meridio-2/internal/nftables"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

// Compile-time assertions that the concrete types wired into the LB metrics collectors satisfy
// the collector reader interfaces. These guard against silent drift when either side changes.
var (
	_ lbmetrics.StatsReader    = (*nfqlb.NFQueueLoadBalancer)(nil)
	_ lbmetrics.RouteReader    = (*nfqlb.NFQueueLoadBalancer)(nil)
	_ lbmetrics.NftablesReader = (*nftablesmanager.Manager)(nil)
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(meridio2v1alpha1.AddToScheme(scheme))
	utilruntime.Must(gatewayv1.Install(scheme))
}

func newCmdRun() *cobra.Command {
	cfg := &config.LoadBalancerConfig{}

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run the stateless-load-balancer controller",
		Long:  `Run the stateless-load-balancer controller to manage NFQLB and nftables`,
		PreRunE: func(cmd *cobra.Command, args []string) error {
			cfg.BindEnv(cmd.Flags())

			// Parse initial log level
			initialLevel, err := log.ParseLevel(cfg.LogLevel)
			if err != nil {
				return fmt.Errorf("invalid --log-level: %w", err)
			}

			// Create atomic level for dynamic changes
			atomicLevel := zap.NewAtomicLevelAt(initialLevel)

			// Setup logger with atomic level
			zapOpts := ctrlzap.Options{
				Development: initialLevel == zapcore.DebugLevel,
				Level:       atomicLevel,
			}
			ctrl.SetLogger(ctrlzap.New(ctrlzap.UseFlagOptions(&zapOpts)))

			// Start dynamic log level server (non-blocking, non-fatal)
			log.StartDynamicLevelServer(cmd.Context(), cfg.LogLevelAPI, atomicLevel, ctrl.Log)

			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLoadBalancer(cfg)
		},
	}

	cfg.AddFlags(cmd.Flags())

	return cmd
}

func runLoadBalancer(cfg *config.LoadBalancerConfig) error {
	setupLog.Info("Starting LoadBalancer controller", "config", cfg)
	var tlsOpts []func(*tls.Config)
	if !cfg.EnableHTTP2 {
		disableHTTP2 := func(c *tls.Config) {
			setupLog.Info("disabling http/2")
			c.NextProtos = []string{"http/1.1"}
		}
		tlsOpts = append(tlsOpts, disableHTTP2)
	}
	// Validate required fields
	if cfg.GatewayName == "" || cfg.GatewayNamespace == "" {
		return fmt.Errorf("gateway-name and gateway-namespace are required")
	}

	// Validate the metrics prefix early (fail fast) when metrics are enabled.
	if commonmetrics.Enabled(cfg.MetricsAddr) {
		if err := commonmetrics.ValidatePrefix(cfg.MetricsPrefix); err != nil {
			return fmt.Errorf("metrics-prefix: %w", err)
		}
	}

	// Initialize NFQLB
	nfqlbInstance, err := nfqlb.New(nfqlb.WithQueue(cfg.NFQueue), nfqlb.WithFwmarkBase(cfg.FwmarkBase))
	if err != nil {
		setupLog.Error(err, "failed to create NFQLB instance")
		return err
	}

	// NFQLB lifecycle: if the process fails, cancel context to crash the container.
	// Kubernetes will restart via CrashLoopBackOff. A running LB Pod with dead NFQLB
	// would black-hole traffic since the ENC controller includes it in next-hop lists.
	// ctrl.SetupSignalHandler() provides OS signal handling (SIGTERM/SIGINT).
	ctx, cancel := context.WithCancelCause(ctrl.SetupSignalHandler())
	defer cancel(nil)

	go func() {
		setupLog.Info("Starting NFQLB process")
		err := nfqlbInstance.Start(ctx)
		if err != nil && ctx.Err() == nil {
			setupLog.Error(err, "NFQLB process failed")
			cancel(fmt.Errorf("NFQLB process failed: %w", err))
		} else {
			setupLog.Info("NFQLB process terminated", "error", err)
		}
	}()

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

	// Create manager
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

	// Setup LoadBalancer controller. Construct the route-config-error counter only when metrics
	// are enabled; otherwise leave a TYPED-NIL *lbmetrics.ConfigErrors so the controller's
	// nil-safe Inc no-ops (never an untyped nil, which would make the interface field non-nil
	// and panic on Inc).
	var configErrors *lbmetrics.ConfigErrors
	if commonmetrics.Enabled(cfg.MetricsAddr) {
		configErrors = lbmetrics.NewConfigErrors(cfg.GatewayName, cfg.MetricsPrefix)
	}

	lbController := &loadbalancer.Controller{
		Client:           mgr.GetClient(),
		Scheme:           mgr.GetScheme(),
		GatewayName:      cfg.GatewayName,
		GatewayNamespace: cfg.GatewayNamespace,
		NFQLB:            &loadbalancer.NFQLBManagerAdapter{NFQLB: nfqlbInstance},
		Readiness:        readiness.NewManager(cfg.ReadinessDir),
		ConfigErrors:     configErrors, // typed-nil when metrics disabled; Inc is nil-safe
	}
	if err := lbController.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "failed to setup controller")
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

	// Register custom LB metrics collectors when metrics are enabled. Collectors read lazily at
	// scrape time; the push-style config-error counter is registered too. SetupWithManager has
	// already initialized the shared nftables manager, so NftablesReader() is available here.
	if commonmetrics.Enabled(cfg.MetricsAddr) {
		if err := registerLBMetrics(lbController, nfqlbInstance, configErrors, cfg); err != nil {
			setupLog.Error(err, "failed to register LB metrics")
			return err
		}
	}

	setupLog.Info("starting manager", "gateway", cfg.GatewayName, "namespace", cfg.GatewayNamespace)
	if err := mgr.Start(ctx); err != nil {
		// Check if the manager stopped because NFQLB crashed
		if cause := context.Cause(ctx); cause != nil {
			return cause
		}
		setupLog.Error(err, "problem running manager")
		return err
	}

	return nil
}

// registerLBMetrics registers the LB custom metrics collectors with the controller-runtime
// metrics registry. Called only when metrics are enabled and the prefix is validated.
//
//   - nfqlb collector  (lazy): flow matches + active targets, from the nfqlb subprocess.
//   - nftables collector (lazy): VIP set size + drops, from the shared nftables manager — only if
//     the manager exposes the read methods (lbController.NftablesReader() != nil).
//   - route collector (lazy): policy-route counts per family, from nfqlb's netlink rules.
//   - config-errors counter (push): already incremented by the controller; registered here.
func registerLBMetrics(
	lbController *loadbalancer.Controller,
	nfqlbInstance *nfqlb.NFQueueLoadBalancer,
	configErrors *lbmetrics.ConfigErrors,
	cfg *config.LoadBalancerConfig,
) error {
	// Shared collector-error counter: lazy collectors increment it on a read failure and skip the
	// failing series, instead of emitting an invalid metric (which would 500 the whole scrape
	// under controller-runtime's HTTPErrorOnError).
	collectorErrors := lbmetrics.NewCollectorErrors(cfg.GatewayName, cfg.MetricsPrefix)
	if err := ctrlmetrics.Registry.Register(collectorErrors.Collector()); err != nil {
		return fmt.Errorf("register collector-errors counter: %w", err)
	}

	nfqlbCollector := lbmetrics.NewCollector(
		nfqlbInstance, cfg.GatewayName, cfg.MetricsPrefix, cfg.MetricsCollectTimeout, collectorErrors,
	)
	if err := ctrlmetrics.Registry.Register(nfqlbCollector); err != nil {
		return fmt.Errorf("register nfqlb collector: %w", err)
	}

	if nftReader := lbController.NftablesReader(); nftReader != nil {
		nftCollector := lbmetrics.NewNftablesCollector(nftReader, cfg.GatewayName, cfg.MetricsPrefix, collectorErrors)
		if err := ctrlmetrics.Registry.Register(nftCollector); err != nil {
			return fmt.Errorf("register nftables collector: %w", err)
		}
	} else {
		setupLog.Info("nftables manager does not expose read methods; skipping nftables metrics collector")
	}

	routeCollector := lbmetrics.NewRouteCollector(nfqlbInstance, cfg.GatewayName, cfg.MetricsPrefix, collectorErrors)
	if err := ctrlmetrics.Registry.Register(routeCollector); err != nil {
		return fmt.Errorf("register route collector: %w", err)
	}

	if err := ctrlmetrics.Registry.Register(configErrors.Collector()); err != nil {
		return fmt.Errorf("register config-errors counter: %w", err)
	}

	setupLog.Info("Registered custom LB metrics collectors",
		"metricsPrefix", cfg.MetricsPrefix, "collectTimeout", cfg.MetricsCollectTimeout)
	return nil
}
