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

// SidecarConfig holds configuration for the network sidecar
type SidecarConfig struct {
	PodName         string
	PodNamespace    string
	PodUID          string
	ProbeAddr       string
	LogLevel        string
	MinTableID      int
	MaxTableID      int
	MetricsAddr     string
	SecureMetrics   bool
	MetricsCertPath string
	MetricsCertName string
	MetricsCertKey  string
	EnableHTTP2     bool
}

// AddFlags adds configuration flags to the provided FlagSet
func (c *SidecarConfig) AddFlags(fs *pflag.FlagSet) {
	fs.StringVar(&c.PodName, "pod-name", "",
		"Name of the Pod this sidecar runs in (injected via Downward API)")
	fs.StringVar(&c.PodNamespace, "pod-namespace", "",
		"Namespace of the Pod this sidecar runs in (injected via Downward API)")
	fs.StringVar(&c.PodUID, "pod-uid", "",
		"UID of the Pod this sidecar runs in (injected via Downward API)")
	fs.StringVar(&c.ProbeAddr, "health-probe-bind-address", ":8082",
		"The address the probe endpoint binds to.")
	fs.StringVar(&c.LogLevel, "log-level", "info",
		"Log level (debug, info, error)")
	fs.IntVar(&c.MinTableID, "min-table-id", 50000,
		"Minimum routing table ID for source-based routing")
	fs.IntVar(&c.MaxTableID, "max-table-id", 55000,
		"Maximum routing table ID for source-based routing")
	fs.StringVar(&c.MetricsAddr, "metrics-bind-address", "0",
		"The address the metrics endpoint binds to. "+
			"Use :8443 for HTTPS or :8080 for HTTP, or leave as 0 to disable the metrics service.")
	fs.BoolVar(&c.SecureMetrics, "metrics-secure", true,
		"If set, the metrics endpoint is served securely via HTTPS. Use --metrics-secure=false to use HTTP instead.")
	fs.StringVar(&c.MetricsCertPath, "metrics-cert-path", "",
		"The directory that contains the metrics server certificate.")
	fs.StringVar(&c.MetricsCertName, "metrics-cert-name", "tls.crt",
		"The name of the metrics server certificate file.")
	fs.StringVar(&c.MetricsCertKey, "metrics-cert-key", "tls.key",
		"The name of the metrics server key file.")
	fs.BoolVar(&c.EnableHTTP2, "enable-http2", false,
		"If set, HTTP/2 will be enabled for the metrics server")
}

// BindEnv binds environment variables to configuration fields
// Only applies env vars if the corresponding flag was not explicitly set
func (c *SidecarConfig) BindEnv(fs *pflag.FlagSet) {
	bindString(fs, "pod-name", "POD_NAME", &c.PodName)
	bindString(fs, "pod-namespace", "POD_NAMESPACE", &c.PodNamespace)
	bindString(fs, "pod-uid", "POD_UID", &c.PodUID)
	bindString(fs, "health-probe-bind-address", "MERIDIO_PROBE_ADDR", &c.ProbeAddr)
	bindString(fs, "log-level", "MERIDIO_LOG_LEVEL", &c.LogLevel)
	bindInt(fs, "min-table-id", "MERIDIO_MIN_TABLE_ID", &c.MinTableID)
	bindInt(fs, "max-table-id", "MERIDIO_MAX_TABLE_ID", &c.MaxTableID)
	bindString(fs, "metrics-bind-address", "MERIDIO_METRICS_ADDR", &c.MetricsAddr)
	bindBool(fs, "metrics-secure", "MERIDIO_METRICS_SECURE", &c.SecureMetrics)
	bindString(fs, "metrics-cert-path", "MERIDIO_METRICS_CERT_PATH", &c.MetricsCertPath)
	bindString(fs, "metrics-cert-name", "MERIDIO_METRICS_CERT_NAME", &c.MetricsCertName)
	bindString(fs, "metrics-cert-key", "MERIDIO_METRICS_CERT_KEY", &c.MetricsCertKey)
	bindBool(fs, "enable-http2", "MERIDIO_ENABLE_HTTP2", &c.EnableHTTP2)
}
