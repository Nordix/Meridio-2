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
	goflag "flag"
	"fmt"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
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
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	meridio2v1alpha1 "github.com/nordix/meridio-2/api/v1alpha1"
	"github.com/nordix/meridio-2/internal/common/config"
	"github.com/nordix/meridio-2/internal/common/prerequisites"
	"github.com/nordix/meridio-2/internal/controller/distributiongroup"
	"github.com/nordix/meridio-2/internal/controller/endpointnetworkconfiguration"
	"github.com/nordix/meridio-2/internal/controller/gateway"
	webhookv1alpha1 "github.com/nordix/meridio-2/internal/webhook/v1alpha1"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(meridio2v1alpha1.AddToScheme(scheme))
	utilruntime.Must(gatewayapiv1.Install(scheme))
	// +kubebuilder:scaffold:scheme
}

// NewRunCmd creates the run subcommand
func NewRunCmd() *cobra.Command {
	cfg := &config.ManagerConfig{}
	zapOpts := zap.Options{Development: true}

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run the controller manager",
		Long:  "Start the controller manager to reconcile Gateway API resources",
		PreRunE: func(cmd *cobra.Command, args []string) error {
			cfg.BindEnv(cmd.Flags())
			ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zapOpts)))

			// Check if Gateway API CRDs are installed
			if err := prerequisites.CheckGatewayAPI(); err != nil {
				return fmt.Errorf("gateway API CRDs not found: %w\n\n"+
					"Install for example with:\n"+
					"  kubectl apply -f https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.5.0/standard-install.yaml",
					err)
			}

			// Check if LB deployment template exists
			if err := prerequisites.CheckFile(cfg.TemplatePath, gateway.LBDeploymentTemplateFile); err != nil {
				return fmt.Errorf("LB deployment template not found: %w", err)
			}

			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runManager(cfg)
		},
	}

	cfg.AddFlags(cmd.Flags())

	// Add zap flags via Go FlagSet
	goFlags := goflag.NewFlagSet("", goflag.ContinueOnError)
	zapOpts.BindFlags(goFlags)
	cmd.Flags().AddGoFlagSet(goFlags)

	return cmd
}

func runManager(cfg *config.ManagerConfig) error {
	setupLog.Info("starting controller-manager", "config", cfg)
	var tlsOpts []func(*tls.Config)
	if !cfg.EnableHTTP2 {
		disableHTTP2 := func(c *tls.Config) {
			setupLog.Info("disabling http/2")
			c.NextProtos = []string{"http/1.1"}
		}
		tlsOpts = append(tlsOpts, disableHTTP2)
	}

	webhookServerOptions := webhook.Options{
		Port:    cfg.WebhookPort,
		TLSOpts: tlsOpts,
	}
	if cfg.WebhookCertPath != "" {
		webhookServerOptions.CertDir = cfg.WebhookCertPath
		webhookServerOptions.CertName = cfg.WebhookCertName
		webhookServerOptions.KeyName = cfg.WebhookCertKey
	}
	webhookServer := webhook.NewServer(webhookServerOptions)

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

	// Configure cache
	cacheOptions := cache.Options{}
	if cfg.Namespace != "" {
		cacheOptions.DefaultNamespaces = map[string]cache.Config{
			cfg.Namespace: {},
		}
		cacheOptions.ByObject = map[client.Object]cache.ByObject{
			&gatewayapiv1.GatewayClass{}: {},
		}
		if cfg.EnableTopologyHints {
			cacheOptions.ByObject[&corev1.Node{}] = cache.ByObject{}
		}
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                        scheme,
		Cache:                         cacheOptions,
		Metrics:                       metricsServerOptions,
		WebhookServer:                 webhookServer,
		HealthProbeBindAddress:        cfg.ProbeAddr,
		LeaderElection:                cfg.EnableLeaderElection,
		LeaderElectionID:              "e9d059a3.nordix.org",
		LeaseDuration:                 &cfg.LeaseDuration,
		RenewDeadline:                 &cfg.RenewDeadline,
		RetryPeriod:                   &cfg.RetryPeriod,
		LeaderElectionReleaseOnCancel: cfg.LeaderElectionReleaseOnCancel,
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		return err
	}

	if cfg.EnableWebhooks {
		if err = webhookv1alpha1.SetupL34RouteWebhookWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to create webhook", "webhook", "L34Route")
			return err
		}
	}

	if err = (&gateway.GatewayReconciler{
		Client:           mgr.GetClient(),
		Scheme:           mgr.GetScheme(),
		ControllerName:   cfg.ControllerName,
		Namespace:        cfg.Namespace,
		TemplatePath:     cfg.TemplatePath,
		LBServiceAccount: cfg.LBServiceAccount,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Gateway")
		return err
	}

	if err = (&distributiongroup.DistributionGroupReconciler{
		Client:         mgr.GetClient(),
		Scheme:         mgr.GetScheme(),
		ControllerName: cfg.ControllerName,
		Namespace:      cfg.Namespace,
	}).SetupWithManager(mgr, cfg.EnableTopologyHints); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "DistributionGroup")
		return err
	}

	if err = (&endpointnetworkconfiguration.Reconciler{
		Client:         mgr.GetClient(),
		Scheme:         mgr.GetScheme(),
		ControllerName: cfg.ControllerName,
		Namespace:      cfg.Namespace,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "EndpointNetworkConfiguration")
		return err
	}
	// +kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		return err
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		return err
	}

	setupLog.Info("starting manager", "namespace", cfg.Namespace, "controllerName", cfg.ControllerName)
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		return err
	}

	return nil
}
