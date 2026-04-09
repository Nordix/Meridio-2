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

	"github.com/google/nftables"
	"github.com/nordix/meridio/pkg/loadbalancer/nfqlb"
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
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	meridio2v1alpha1 "github.com/nordix/meridio-2/api/v1alpha1"
	"github.com/nordix/meridio-2/internal/common/config"
	"github.com/nordix/meridio-2/internal/controller/loadbalancer"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
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
			// Setup logger based on log level
			zapOpts := zap.Options{Development: cfg.LogLevel == "debug"}
			ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zapOpts)))
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLoadBalancer(cmd.Context(), cfg)
		},
	}

	cfg.AddFlags(cmd.Flags())

	return cmd
}

func runLoadBalancer(ctx context.Context, cfg *config.LoadBalancerConfig) error {
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

	// Initialize NFQLB
	lbFactory := nfqlb.NewLbFactory(nfqlb.WithNFQueue(cfg.NFQueue))

	go func() {
		setupLog.Info("Starting NFQLB process")
		if err := lbFactory.Start(ctx); err != nil {
			setupLog.Error(err, "NFQLB process failed")
		}
		setupLog.Info("NFQLB process terminated")
	}()

	// Initialize nftables
	nftConn := &nftables.Conn{}
	nftTable := nftConn.AddTable(&nftables.Table{
		Name:   "meridio",
		Family: nftables.TableFamilyINet,
	})
	if err := nftConn.Flush(); err != nil {
		setupLog.Error(err, "failed to create nftables table")
		return err
	}

	nftChain := nftConn.AddChain(&nftables.Chain{
		Name:     "prerouting",
		Table:    nftTable,
		Type:     nftables.ChainTypeFilter,
		Hooknum:  nftables.ChainHookPrerouting,
		Priority: nftables.ChainPriorityFilter,
	})
	if err := nftConn.Flush(); err != nil {
		setupLog.Error(err, "failed to create nftables chain")
		return err
	}

	setupLog.Info("nftables initialized", "table", nftTable.Name, "chain", nftChain.Name)

	// Cleanup nftables on shutdown
	defer func() {
		setupLog.Info("Cleaning up nftables")
		conn := &nftables.Conn{}
		conn.FlushTable(nftTable)
		conn.DelTable(nftTable)
		if err := conn.Flush(); err != nil {
			setupLog.Error(err, "failed to cleanup nftables")
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

	// Setup LoadBalancer controller
	if err := (&loadbalancer.Controller{
		Client:                   mgr.GetClient(),
		Scheme:                   mgr.GetScheme(),
		GatewayName:              cfg.GatewayName,
		GatewayNamespace:         cfg.GatewayNamespace,
		LBFactory:                lbFactory,
		NFTConn:                  nftConn,
		NFTTable:                 nftTable,
		NFTChain:                 nftChain,
		DefragExcludedIfPrefixes: cfg.DefragExcludedIfPrefixes,
	}).SetupWithManager(mgr); err != nil {
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

	setupLog.Info("starting manager for Gateway %s/%s", cfg.GatewayName, cfg.GatewayNamespace)
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		return err
	}

	return nil
}
