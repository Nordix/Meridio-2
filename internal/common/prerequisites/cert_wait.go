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

package prerequisites

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sync/errgroup"
)

const certPollInterval = 1 * time.Second

// CertWaitConfig holds the configuration parameters needed to determine which certificate files to wait for.
type CertWaitConfig struct {
	EnableWebhooks  bool
	WebhookCertPath string
	WebhookCertName string
	WebhookCertKey  string
	MetricsAddr     string
	SecureMetrics   bool
	MetricsCertPath string
	MetricsCertName string
	MetricsCertKey  string
}

// CertFiles returns the list of certificate files that need to exist based on the configuration.
func (c *CertWaitConfig) CertFiles() []string {
	var certFiles []string
	if c.EnableWebhooks && c.WebhookCertPath != "" {
		certFiles = append(certFiles,
			filepath.Join(c.WebhookCertPath, c.WebhookCertName),
			filepath.Join(c.WebhookCertPath, c.WebhookCertKey),
		)
	}
	if c.MetricsAddr != "0" && c.SecureMetrics && c.MetricsCertPath != "" {
		certFiles = append(certFiles,
			filepath.Join(c.MetricsCertPath, c.MetricsCertName),
			filepath.Join(c.MetricsCertPath, c.MetricsCertKey),
		)
	}
	return certFiles
}

// WaitForCerts polls for all specified files concurrently until they exist
// or the context is cancelled. Returns nil if all files are found.
func WaitForCerts(ctx context.Context, files []string) error {
	if len(files) == 0 {
		return nil
	}

	g, ctx := errgroup.WithContext(ctx)
	for _, f := range files {
		g.Go(func() error {
			return waitForFile(ctx, f)
		})
	}

	return g.Wait()
}

func waitForFile(ctx context.Context, path string) error {
	// Check immediately before entering the poll loop
	if _, err := os.Stat(path); err == nil {
		return nil
	}

	ticker := time.NewTicker(certPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for certificate file %q: %w", path, ctx.Err())
		case <-ticker.C:
			if _, err := os.Stat(path); err == nil {
				return nil
			}
		}
	}
}
