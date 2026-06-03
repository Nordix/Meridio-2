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
	"os"
	"strings"
	"time"

	"github.com/nordix/meridio-2/internal/bird"
	"github.com/nordix/meridio-2/internal/common/readiness"
	"github.com/spf13/pflag"
)

// RouterConfig holds configuration for the router
type RouterConfig struct {
	GatewayName          string
	GatewayNamespace     string
	PodName              string
	PodNamespace         string
	PodUID               string
	ConnectivityHoldTime time.Duration
	ProbeAddr            string
	LogLevel             string
	MetricsAddr          string
	SecureMetrics        bool
	MetricsCertPath      string
	MetricsCertName      string
	MetricsCertKey       string
	EnableHTTP2          bool
	BirdLogs             bird.BirdLogParams
	BirdKernelScanTime   int
	LBReadinessDir       string
}

// AddFlags adds configuration flags to the provided FlagSet
func (c *RouterConfig) AddFlags(fs *pflag.FlagSet) {
	fs.StringVar(&c.GatewayName, "gateway-name", "",
		"Name of the Gateway resource to watch (required)")
	fs.StringVar(&c.GatewayNamespace, "gateway-namespace", "default",
		"Namespace of the Gateway resource")
	fs.StringVar(&c.PodName, "pod-name", "",
		"Pod name (from Downward API)")
	fs.StringVar(&c.PodNamespace, "pod-namespace", "",
		"Pod namespace (from Downward API)")
	fs.StringVar(&c.PodUID, "pod-uid", "",
		"Pod UID (from Downward API)")
	fs.DurationVar(&c.ConnectivityHoldTime, "connectivity-hold-time", 3*time.Second,
		"Hold time before setting connectivity gate to True after BGP session establishes")
	fs.StringVar(&c.ProbeAddr, "health-probe-bind-address", ":8082",
		"The address the probe endpoint binds to.")
	fs.StringVar(&c.LogLevel, "log-level", "info",
		"Log level (debug, info, error)")
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
		"If set, HTTP/2 will be enabled for the metrics and webhook servers")
	fs.IntVar(&c.BirdKernelScanTime, "bird-kernel-scan-time", 10,
		"Interval in seconds for BIRD kernel protocol route table scanning")
	fs.StringVar(&c.LBReadinessDir, "readiness-dir", readiness.DefaultReadinessDir,
		"Path to LB readiness directory. VIPs are only advertised when lb-ready-* files exist. "+
			"Empty string disables the check (always advertise).")
	fs.Var(&c.BirdLogs, "bird-log",
		"BIRD log destination (repeatable).\n"+
			"Format: type:params:classes\n"+
			"Types:\n"+
			"  stderr:classes\n"+
			"  file:path:classes                    (no rotation)\n"+
			"  file:path:size:backup:classes         (with rotation, size in bytes)\n"+
			"  fixed:path:size:classes               (ring buffer, size in bytes)\n"+
			"  syslog:name:classes\n"+
			"  udp:address:port:classes              (IPv4)\n"+
			"  udp:[address]:port:classes            (IPv6)\n"+
			"Classes: all, or comma-separated subset: debug,trace,info,remote,auth,warning,error,bug,fatal\n"+
			"Examples:\n"+
			"  --bird-log stderr:all\n"+
			"  --bird-log file:/var/log/bird.log:info,warning,error\n"+
			"  --bird-log file:/var/log/bird.log:1048576:/var/log/bird.log.1:all\n"+
			"  --bird-log udp:10.0.0.1:514:all\n"+
			"  --bird-log 'udp:[fd00::1]:514:all'")
}

// BindEnv binds environment variables to configuration fields
// Only applies env vars if the corresponding flag was not explicitly set
// Precedence: Flags > Env vars > Defaults
func (c *RouterConfig) BindEnv(fs *pflag.FlagSet) {
	bindString(fs, "gateway-name", "MERIDIO_GATEWAY_NAME", &c.GatewayName)
	bindString(fs, "gateway-namespace", "MERIDIO_GATEWAY_NAMESPACE", &c.GatewayNamespace)
	bindString(fs, "pod-name", "POD_NAME", &c.PodName)
	bindString(fs, "pod-namespace", "POD_NAMESPACE", &c.PodNamespace)
	bindString(fs, "pod-uid", "POD_UID", &c.PodUID)
	bindDuration(fs, "connectivity-hold-time", "MERIDIO_CONNECTIVITY_HOLD_TIME", &c.ConnectivityHoldTime)
	bindString(fs, "health-probe-bind-address", "MERIDIO_PROBE_ADDR", &c.ProbeAddr)
	bindString(fs, "log-level", "MERIDIO_LOG_LEVEL", &c.LogLevel)
	bindString(fs, "metrics-bind-address", "MERIDIO_METRICS_ADDR", &c.MetricsAddr)
	bindBool(fs, "metrics-secure", "MERIDIO_METRICS_SECURE", &c.SecureMetrics)
	bindString(fs, "metrics-cert-path", "MERIDIO_METRICS_CERT_PATH", &c.MetricsCertPath)
	bindString(fs, "metrics-cert-name", "MERIDIO_METRICS_CERT_NAME", &c.MetricsCertName)
	bindString(fs, "metrics-cert-key", "MERIDIO_METRICS_CERT_KEY", &c.MetricsCertKey)
	bindBool(fs, "enable-http2", "MERIDIO_ENABLE_HTTP2", &c.EnableHTTP2)
	bindInt(fs, "bird-kernel-scan-time", "MERIDIO_BIRD_KERNEL_SCAN_TIME", &c.BirdKernelScanTime)
	bindString(fs, "readiness-dir", "MERIDIO_READINESS_DIR", &c.LBReadinessDir)
	bindBirdLogs(fs, "bird-log", "MERIDIO_BIRD_LOG", &c.BirdLogs)
}

// bindBirdLogs binds a semicolon-separated environment variable to a BirdLogParams.
// Each entry is parsed as a bird log spec (e.g. "stderr:all;file:/var/log/bird.log:all").
// Only applies if the corresponding flag was not explicitly set.
func bindBirdLogs(fs *pflag.FlagSet, flagName, envName string, target *bird.BirdLogParams) {
	if !fs.Changed(flagName) {
		if val := os.Getenv(envName); val != "" {
			for entry := range strings.SplitSeq(val, ";") {
				entry = strings.TrimSpace(entry)
				if entry != "" {
					_ = target.Set(entry)
				}
			}
		}
	}
}
