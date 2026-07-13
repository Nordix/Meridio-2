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
	"time"

	"github.com/spf13/pflag"
)

// ManagerConfig holds configuration for the controller manager
type ManagerConfig struct {
	// Core operational
	Namespace            string
	ControllerName       string
	MetricsAddr          string
	ProbeAddr            string
	EnableLeaderElection bool
	EnableWebhooks       bool

	// Leader election tuning
	LeaseDuration                 time.Duration
	RenewDeadline                 time.Duration
	RetryPeriod                   time.Duration
	LeaderElectionReleaseOnCancel bool

	// Security
	SecureMetrics bool
	EnableHTTP2   bool

	// Logging
	LogLevel    string
	LogLevelAPI string

	// Features
	EnableTopologyHints  bool
	MaxEndpointsPerSlice int

	// Filtering
	PodCacheLabel string

	// Templates
	TemplatePath string

	// ServiceAccounts
	LBServiceAccount string

	// Certificates
	CertWaitTimeout time.Duration
	WebhookPort     int
	WebhookCertPath string
	WebhookCertName string
	WebhookCertKey  string
	MetricsCertPath string
	MetricsCertName string
	MetricsCertKey  string
}

// AddFlags adds configuration flags to the provided FlagSet
func (c *ManagerConfig) AddFlags(fs *pflag.FlagSet) {
	fs.StringVar(&c.Namespace, "namespace", "",
		"Namespace to watch for resources. If empty, watches all namespaces.")
	fs.StringVar(&c.ControllerName, "controller-name", "meridio-2.nordix.org/gateway-controller",
		"The controller name to match in GatewayClass.spec.controllerName")
	fs.StringVar(&c.MetricsAddr, "metrics-bind-address", "0",
		"The address the metrics endpoint binds to. Use :8443 for HTTPS or :8080 for HTTP, or leave as 0 to disable.")
	fs.StringVar(&c.ProbeAddr, "health-probe-bind-address", ":8081",
		"The address the probe endpoint binds to.")
	fs.BoolVar(&c.EnableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager.")
	fs.DurationVar(&c.LeaseDuration, "leader-elect-lease-duration", 15*time.Second,
		"Duration that non-leader candidates will wait to force acquire leadership.")
	fs.DurationVar(&c.RenewDeadline, "leader-elect-renew-deadline", 10*time.Second,
		"Duration that the acting leader will retry refreshing leadership before giving up.")
	fs.DurationVar(&c.RetryPeriod, "leader-elect-retry-period", 2*time.Second,
		"Duration between each leader election retry.")
	fs.BoolVar(&c.LeaderElectionReleaseOnCancel, "leader-elect-release-on-cancel", true,
		"Release the leader lease on graceful shutdown for faster failover.")
	fs.BoolVar(&c.EnableWebhooks, "enable-webhooks", true,
		"Enable webhook server. Set to false for testing.")
	fs.BoolVar(&c.SecureMetrics, "metrics-secure", true,
		"If set, the metrics endpoint is served securely via HTTPS.")
	fs.BoolVar(&c.EnableHTTP2, "enable-http2", false,
		"If set, HTTP/2 will be enabled for the metrics and webhook servers")
	fs.StringVar(&c.LogLevel, "log-level", "info",
		"Log level (debug, info, warn, error)")
	fs.StringVar(&c.LogLevelAPI, "log-level-api", "",
		"Address for dynamic log level HTTP endpoint (e.g., 127.0.0.1:9901). Empty disables the feature.")
	fs.BoolVar(&c.EnableTopologyHints, "enable-topology-hints", false,
		"Enable Node watching for topology-aware endpoint hints. Requires RBAC permissions for nodes.")
	fs.IntVar(&c.MaxEndpointsPerSlice, "max-endpoints-per-slice", 200,
		"Maximum number of endpoints per LoadBalancerEndpointSlice.")
	fs.StringVar(&c.PodCacheLabel, "pod-cache-label", "",
		"Label key=value to filter Pods in the informer cache (e.g., meridio-2.nordix.org/managed=true). "+
			"When set, only Pods with this label are cached. Empty means all Pods are cached.")
	fs.StringVar(&c.TemplatePath, "template-path", "/templates",
		"Path to template directory containing deployment templates.")
	fs.StringVar(&c.LBServiceAccount, "lb-service-account", "stateless-load-balancer",
		"ServiceAccount name for LB Deployment pods.")
	fs.IntVar(&c.WebhookPort, "webhook-port", 9443,
		"The port the webhook server binds to.")
	fs.DurationVar(&c.CertWaitTimeout, "cert-wait-timeout", 10*time.Second,
		"Duration to wait for certificate files before starting. 0 means no wait. Maximum 1 minute.")
	fs.StringVar(&c.WebhookCertPath, "webhook-cert-path", "",
		"The directory that contains the webhook certificate.")
	fs.StringVar(&c.WebhookCertName, "webhook-cert-name", "tls.crt",
		"The name of the webhook certificate file.")
	fs.StringVar(&c.WebhookCertKey, "webhook-cert-key", "tls.key",
		"The name of the webhook key file.")
	fs.StringVar(&c.MetricsCertPath, "metrics-cert-path", "",
		"The directory that contains the metrics server certificate.")
	fs.StringVar(&c.MetricsCertName, "metrics-cert-name", "tls.crt",
		"The name of the metrics server certificate file.")
	fs.StringVar(&c.MetricsCertKey, "metrics-cert-key", "tls.key",
		"The name of the metrics server key file.")
}

// BindEnv binds environment variables to configuration fields
// Only applies env vars if the corresponding flag was not explicitly set
// Precedence: Flags > Env vars > Defaults
func (c *ManagerConfig) BindEnv(fs *pflag.FlagSet) {
	bindString(fs, "namespace", "MERIDIO_NAMESPACE", &c.Namespace)
	bindString(fs, "controller-name", "MERIDIO_CONTROLLER_NAME", &c.ControllerName)
	bindString(fs, "metrics-bind-address", "MERIDIO_METRICS_ADDR", &c.MetricsAddr)
	bindString(fs, "health-probe-bind-address", "MERIDIO_PROBE_ADDR", &c.ProbeAddr)
	bindBool(fs, "leader-elect", "MERIDIO_LEADER_ELECT", &c.EnableLeaderElection)
	bindDuration(fs, "leader-elect-lease-duration", "MERIDIO_LEADER_ELECT_LEASE_DURATION", &c.LeaseDuration)
	bindDuration(fs, "leader-elect-renew-deadline", "MERIDIO_LEADER_ELECT_RENEW_DEADLINE", &c.RenewDeadline)
	bindDuration(fs, "leader-elect-retry-period", "MERIDIO_LEADER_ELECT_RETRY_PERIOD", &c.RetryPeriod)
	bindBool(fs, "leader-elect-release-on-cancel", "MERIDIO_LEADER_ELECT_RELEASE_ON_CANCEL", &c.LeaderElectionReleaseOnCancel)
	bindBool(fs, "enable-webhooks", "MERIDIO_ENABLE_WEBHOOKS", &c.EnableWebhooks)
	bindBool(fs, "metrics-secure", "MERIDIO_METRICS_SECURE", &c.SecureMetrics)
	bindBool(fs, "enable-http2", "MERIDIO_ENABLE_HTTP2", &c.EnableHTTP2)
	bindString(fs, "log-level", "MERIDIO_LOG_LEVEL", &c.LogLevel)
	bindString(fs, "log-level-api", "MERIDIO_LOG_LEVEL_API", &c.LogLevelAPI)
	bindBool(fs, "enable-topology-hints", "MERIDIO_ENABLE_TOPOLOGY_HINTS", &c.EnableTopologyHints)
	bindInt(fs, "max-endpoints-per-slice", "MERIDIO_MAX_ENDPOINTS_PER_SLICE", &c.MaxEndpointsPerSlice)
	bindString(fs, "pod-cache-label", "MERIDIO_POD_CACHE_LABEL", &c.PodCacheLabel)
	bindString(fs, "template-path", "MERIDIO_TEMPLATE_PATH", &c.TemplatePath)
	bindString(fs, "lb-service-account", "MERIDIO_LB_SERVICE_ACCOUNT", &c.LBServiceAccount)
	bindInt(fs, "webhook-port", "MERIDIO_WEBHOOK_PORT", &c.WebhookPort)
	bindDuration(fs, "cert-wait-timeout", "MERIDIO_CERT_WAIT_TIMEOUT", &c.CertWaitTimeout)
	bindString(fs, "webhook-cert-path", "MERIDIO_WEBHOOK_CERT_PATH", &c.WebhookCertPath)
	bindString(fs, "webhook-cert-name", "MERIDIO_WEBHOOK_CERT_NAME", &c.WebhookCertName)
	bindString(fs, "webhook-cert-key", "MERIDIO_WEBHOOK_CERT_KEY", &c.WebhookCertKey)
	bindString(fs, "metrics-cert-path", "MERIDIO_METRICS_CERT_PATH", &c.MetricsCertPath)
	bindString(fs, "metrics-cert-name", "MERIDIO_METRICS_CERT_NAME", &c.MetricsCertName)
	bindString(fs, "metrics-cert-key", "MERIDIO_METRICS_CERT_KEY", &c.MetricsCertKey)
}
