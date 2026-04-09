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
	"github.com/spf13/pflag"
)

// LoadBalancerConfig holds configuration for the stateless-load-balancer
type LoadBalancerConfig struct {
	GatewayName              string
	GatewayNamespace         string
	NFQueue                  string
	DefragExcludedIfPrefixes []string
	ProbeAddr                string
	LogLevel                 string
	MetricsAddr              string
	SecureMetrics            bool
	MetricsCertPath          string
	MetricsCertName          string
	MetricsCertKey           string
	EnableHTTP2              bool
}

// AddFlags adds configuration flags to the provided FlagSet
func (c *LoadBalancerConfig) AddFlags(fs *pflag.FlagSet) {
	fs.StringVar(&c.GatewayName, "gateway-name", "",
		"Name of the Gateway this LB belongs to")
	fs.StringVar(&c.GatewayNamespace, "gateway-namespace", "",
		"Namespace of the Gateway")
	fs.StringVar(&c.NFQueue, "nfqueue", "0:3",
		"Netfilter queue(s) to be used by NFQLB")
	fs.StringSliceVar(&c.DefragExcludedIfPrefixes, "defrag-excluded-if-prefixes", nil,
		"Interface name prefixes excluded from defragmentation (target-facing interfaces, to preserve PMTU info in outbound packets)")
	fs.StringVar(&c.ProbeAddr, "health-probe-bind-address", ":8081",
		"The address the probe endpoint binds to")
	fs.StringVar(&c.LogLevel, "log-level", "info",
		"Log level (debug, info, error)")
	fs.StringVar(&c.MetricsAddr, "metrics-bind-address", "0", "The address the metrics endpoint binds to. "+
		"Use :8443 for HTTPS or :8080 for HTTP, or leave as 0 to disable the metrics service.")
	fs.BoolVar(&c.SecureMetrics, "metrics-secure", true,
		"If set, the metrics endpoint is served securely via HTTPS. Use --metrics-secure=false to use HTTP instead.")
	fs.StringVar(&c.MetricsCertPath, "metrics-cert-path", "",
		"The directory that contains the metrics server certificate.")
	fs.StringVar(&c.MetricsCertName, "metrics-cert-name", "tls.crt", "The name of the metrics server certificate file.")
	fs.StringVar(&c.MetricsCertKey, "metrics-cert-key", "tls.key", "The name of the metrics server key file.")
	fs.BoolVar(&c.EnableHTTP2, "enable-http2", false,
		"If set, HTTP/2 will be enabled for the metrics and webhook servers")
}

// BindEnv binds environment variables to configuration fields
// Only applies env vars if the corresponding flag was not explicitly set
func (c *LoadBalancerConfig) BindEnv(fs *pflag.FlagSet) {
	bindString(fs, "gateway-name", "MERIDIO_GATEWAY_NAME", &c.GatewayName)
	bindString(fs, "gateway-namespace", "MERIDIO_GATEWAY_NAMESPACE", &c.GatewayNamespace)
	bindString(fs, "nfqueue", "MERIDIO_NFQUEUE", &c.NFQueue)
	bindStringSlice(fs, "defrag-excluded-if-prefixes", "MERIDIO_DEFRAG_EXCLUDED_IF_PREFIXES", &c.DefragExcludedIfPrefixes)
	bindString(fs, "health-probe-bind-address", "MERIDIO_PROBE_ADDR", &c.ProbeAddr)
	bindString(fs, "log-level", "MERIDIO_LOG_LEVEL", &c.LogLevel)
	bindString(fs, "metrics-bind-address", "MERIDIO_METRICS_ADDR", &c.MetricsAddr)
	bindBool(fs, "metrics-secure", "MERIDIO_METRICS_SECURE", &c.SecureMetrics)
	bindString(fs, "metrics-cert-path", "MERIDIO_METRICS_CERT_PATH", &c.MetricsCertPath)
	bindString(fs, "metrics-cert-name", "MERIDIO_METRICS_CERT_NAME", &c.MetricsCertName)
	bindString(fs, "metrics-cert-key", "MERIDIO_METRICS_CERT_KEY", &c.MetricsCertKey)
	bindBool(fs, "enable-http2", "MERIDIO_ENABLE_HTTP2", &c.EnableHTTP2)
}
