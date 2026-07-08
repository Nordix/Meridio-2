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

// Package app provides the entry point for running the Meridio-2 controller manager
// with optional additional controllers. Downstream consumers can embed Meridio-2's
// controllers in their own binary by calling NewCommand() with additional ControllerSetup
// functions.
//
// Usage:
//
//	cmd := app.NewCommand(mycontroller.Setup)
//	if err := cmd.Execute(); err != nil {
//	    os.Exit(1)
//	}
package app

import (
	"context"
	"crypto/tls"
	goflag "flag"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
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

// ControllerSetup registers an additional controller with the manager.
// It is called after built-in controllers are registered and before the
// manager is started. The manager's scheme can be extended via mgr.GetScheme().
type ControllerSetup func(mgr ctrl.Manager, cfg Config) error

// Config exposes resolved controller-manager configuration to additional controllers.
// Values reflect the final precedence: CLI flags > environment variables > defaults.
type Config struct {
	Namespace      string
	ControllerName string
}

// NewCommand creates a Cobra command that runs the Meridio-2 controller manager
// with the same flags and environment variable bindings as the default binary.
// Additional controllers are registered after built-in ones.
//
// Downstream consumers use this to embed Meridio-2's controllers in their own binary:
//
//	func main() {
//	    cmd := app.NewCommand(mycontroller.SetupWithManager)
//	    if err := cmd.Execute(); err != nil {
//	        os.Exit(1)
//	    }
//	}
func NewCommand(additional ...ControllerSetup) *cobra.Command {
	cfg := &config.ManagerConfig{}
	zapOpts := zap.Options{Development: true}

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run the controller manager",
		Long:  "Start the controller manager to reconcile Gateway API resources",
		PreRunE: func(cmd *cobra.Command, args []string) error {
			cfg.BindEnv(cmd.Flags())
			ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zapOpts)))
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(cfg, additional...)
		},
	}

	cfg.AddFlags(cmd.Flags())

	goFlags := goflag.NewFlagSet("", goflag.ContinueOnError)
	zapOpts.BindFlags(goFlags)
	cmd.Flags().AddGoFlagSet(goFlags)

	return cmd
}

// run creates the Meridio-2 controller manager, registers built-in controllers
// and webhooks, applies additional controller setups, and starts the manager.
func run(cfg *config.ManagerConfig, additional ...ControllerSetup) error {
	setupLog := ctrl.Log.WithName("setup")
	setupLog.Info("starting controller-manager", "config", cfg)

	if err := validate(cfg); err != nil {
		return fmt.Errorf("config validation: %w", err)
	}

	if err := waitForCerts(cfg); err != nil {
		return err
	}

	mgr, err := setupManager(cfg)
	if err != nil {
		return fmt.Errorf("setup manager: %w", err)
	}

	if err := registerBuiltinControllers(mgr, cfg); err != nil {
		return fmt.Errorf("register controllers: %w", err)
	}

	appCfg := Config{
		Namespace:      cfg.Namespace,
		ControllerName: cfg.ControllerName,
	}
	for i, setup := range additional {
		if err := setup(mgr, appCfg); err != nil {
			return fmt.Errorf("additional controller [%d]: %w", i, err)
		}
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return fmt.Errorf("healthz: %w", err)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return fmt.Errorf("readyz: %w", err)
	}

	setupLog.Info("starting manager", "namespace", cfg.Namespace, "controllerName", cfg.ControllerName)
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		return fmt.Errorf("manager exited: %w", err)
	}

	return nil
}

// validate checks configuration preconditions.
func validate(cfg *config.ManagerConfig) error {
	if err := validateConfig(cfg); err != nil {
		return err
	}

	if err := validatePodCacheLabel(cfg.PodCacheLabel); err != nil {
		return err
	}

	if err := prerequisites.CheckGatewayAPI(); err != nil {
		return fmt.Errorf("gateway API CRDs not found: %w\n\n"+
			"Install for example with:\n"+
			"  kubectl apply -f https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.5.0/standard-install.yaml",
			err)
	}

	if err := prerequisites.CheckFile(cfg.TemplatePath, gateway.LBDeploymentTemplateFile); err != nil {
		return fmt.Errorf("LB deployment template not found: %w", err)
	}

	return nil
}

// validateConfig checks configuration values that don't require infrastructure access.
func validateConfig(cfg *config.ManagerConfig) error {
	if cfg.CertWaitTimeout > time.Minute {
		return fmt.Errorf("cert-wait-timeout cannot exceed 1 minute (got %s)", cfg.CertWaitTimeout)
	}
	return nil
}

// validatePodCacheLabel checks the pod-cache-label format.
func validatePodCacheLabel(label string) error {
	if label == "" {
		return nil
	}
	k, v, ok := strings.Cut(label, "=")
	if !ok || k == "" || v == "" || strings.Contains(v, "=") {
		return fmt.Errorf("pod-cache-label must be in key=value format, got %q", label)
	}
	return nil
}

// waitForCerts waits for certificate files if configured.
func waitForCerts(cfg *config.ManagerConfig) error {
	if cfg.CertWaitTimeout <= 0 {
		return nil
	}

	certFiles := (&prerequisites.CertWaitConfig{
		EnableWebhooks:  cfg.EnableWebhooks,
		WebhookCertPath: cfg.WebhookCertPath,
		WebhookCertName: cfg.WebhookCertName,
		WebhookCertKey:  cfg.WebhookCertKey,
		MetricsAddr:     cfg.MetricsAddr,
		SecureMetrics:   cfg.SecureMetrics,
		MetricsCertPath: cfg.MetricsCertPath,
		MetricsCertName: cfg.MetricsCertName,
		MetricsCertKey:  cfg.MetricsCertKey,
	}).CertFiles()

	if len(certFiles) == 0 {
		return nil
	}

	setupLog := ctrl.Log.WithName("setup")
	setupLog.Info("waiting for certificate files", "files", certFiles, "timeout", cfg.CertWaitTimeout)
	ctx, cancel := context.WithTimeout(context.Background(), cfg.CertWaitTimeout)
	defer cancel()
	if err := prerequisites.WaitForCerts(ctx, certFiles); err != nil {
		return fmt.Errorf("certificate wait failed: %w", err)
	}
	setupLog.Info("all certificate files are available")
	return nil
}

// setupManager creates a configured ctrl.Manager.
func setupManager(cfg *config.ManagerConfig) (ctrl.Manager, error) {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(meridio2v1alpha1.AddToScheme(scheme))
	utilruntime.Must(gatewayapiv1.Install(scheme))

	var tlsOpts []func(*tls.Config)
	if !cfg.EnableHTTP2 {
		tlsOpts = append(tlsOpts, func(c *tls.Config) {
			c.NextProtos = []string{"http/1.1"}
		})
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

	// Apply pod cache label filter (already validated)
	if cfg.PodCacheLabel != "" {
		k, v, _ := strings.Cut(cfg.PodCacheLabel, "=")
		if cacheOptions.ByObject == nil {
			cacheOptions.ByObject = map[client.Object]cache.ByObject{}
		}
		cacheOptions.ByObject[&corev1.Pod{}] = cache.ByObject{
			Label: labels.SelectorFromSet(labels.Set{k: v}),
		}
	}

	return ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                        scheme,
		Cache:                         cacheOptions,
		Metrics:                       metricsServerOptions,
		WebhookServer:                 webhook.NewServer(webhookServerOptions),
		HealthProbeBindAddress:        cfg.ProbeAddr,
		LeaderElection:                cfg.EnableLeaderElection,
		LeaderElectionID:              "e9d059a3.nordix.org",
		LeaseDuration:                 &cfg.LeaseDuration,
		RenewDeadline:                 &cfg.RenewDeadline,
		RetryPeriod:                   &cfg.RetryPeriod,
		LeaderElectionReleaseOnCancel: cfg.LeaderElectionReleaseOnCancel,
	})
}

// registerBuiltinControllers registers Gateway, DistributionGroup, ENC controllers and webhooks.
func registerBuiltinControllers(mgr ctrl.Manager, cfg *config.ManagerConfig) error {
	if cfg.EnableWebhooks {
		if err := webhookv1alpha1.SetupL34RouteWebhookWithManager(mgr); err != nil {
			return fmt.Errorf("webhook L34Route: %w", err)
		}
	}

	var podCacheLabelKey, podCacheLabelValue string
	if cfg.PodCacheLabel != "" {
		podCacheLabelKey, podCacheLabelValue, _ = strings.Cut(cfg.PodCacheLabel, "=")
	}

	if err := (&gateway.GatewayReconciler{
		Client:             mgr.GetClient(),
		Scheme:             mgr.GetScheme(),
		ControllerName:     cfg.ControllerName,
		Namespace:          cfg.Namespace,
		TemplatePath:       cfg.TemplatePath,
		LBServiceAccount:   cfg.LBServiceAccount,
		PodCacheLabelKey:   podCacheLabelKey,
		PodCacheLabelValue: podCacheLabelValue,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("controller Gateway: %w", err)
	}

	if err := (&distributiongroup.DistributionGroupReconciler{
		Client:         mgr.GetClient(),
		Scheme:         mgr.GetScheme(),
		ControllerName: cfg.ControllerName,
		Namespace:      cfg.Namespace,
	}).SetupWithManager(mgr, cfg.EnableTopologyHints); err != nil {
		return fmt.Errorf("controller DistributionGroup: %w", err)
	}

	if err := (&endpointnetworkconfiguration.Reconciler{
		Client:         mgr.GetClient(),
		Scheme:         mgr.GetScheme(),
		ControllerName: cfg.ControllerName,
		Namespace:      cfg.Namespace,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("controller EndpointNetworkConfiguration: %w", err)
	}

	return nil
}
