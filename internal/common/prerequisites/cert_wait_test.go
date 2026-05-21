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
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWaitForCerts_EmptyList(t *testing.T) {
	err := WaitForCerts(context.Background(), nil)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestWaitForCerts_FilesAlreadyExist(t *testing.T) {
	dir := t.TempDir()
	f1 := filepath.Join(dir, "tls.crt")
	f2 := filepath.Join(dir, "tls.key")

	if err := os.WriteFile(f1, []byte("cert"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f2, []byte("key"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := WaitForCerts(ctx, []string{f1, f2})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestWaitForCerts_FilesAppearLater(t *testing.T) {
	dir := t.TempDir()
	f1 := filepath.Join(dir, "tls.crt")
	f2 := filepath.Join(dir, "tls.key")

	// Create certs after a short delay
	go func() {
		time.Sleep(500 * time.Millisecond)
		_ = os.WriteFile(f1, []byte("cert"), 0o600)
		_ = os.WriteFile(f2, []byte("key"), 0o600)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := WaitForCerts(ctx, []string{f1, f2})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestWaitForCerts_Timeout(t *testing.T) {
	dir := t.TempDir()
	f1 := filepath.Join(dir, "tls.crt")

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	err := WaitForCerts(ctx, []string{f1})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestWaitForCerts_ContextCancelled(t *testing.T) {
	dir := t.TempDir()
	f1 := filepath.Join(dir, "tls.crt")

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()

	err := WaitForCerts(ctx, []string{f1})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestWaitForCerts_PartialFiles(t *testing.T) {
	dir := t.TempDir()
	f1 := filepath.Join(dir, "tls.crt")
	f2 := filepath.Join(dir, "tls.key")

	// Only create one file
	if err := os.WriteFile(f1, []byte("cert"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	err := WaitForCerts(ctx, []string{f1, f2})
	if err == nil {
		t.Fatal("expected error when one cert is missing, got nil")
	}
}

func TestCertFiles_WebhookOnly(t *testing.T) {
	dir := t.TempDir()
	cfg := &CertWaitConfig{
		EnableWebhooks:  true,
		WebhookCertPath: dir,
		WebhookCertName: "tls.crt",
		WebhookCertKey:  "tls.key",
		MetricsAddr:     "0",
	}
	certFiles := cfg.CertFiles()
	if len(certFiles) != 2 {
		t.Fatalf("expected 2 certs, got %d", len(certFiles))
	}
}

func TestCertFiles_MetricsOnly(t *testing.T) {
	dir := t.TempDir()
	cfg := &CertWaitConfig{
		EnableWebhooks:  false,
		MetricsAddr:     ":8443",
		SecureMetrics:   true,
		MetricsCertPath: dir,
		MetricsCertName: "tls.crt",
		MetricsCertKey:  "tls.key",
	}
	certFiles := cfg.CertFiles()
	if len(certFiles) != 2 {
		t.Fatalf("expected 2 certs, got %d", len(certFiles))
	}
}

func TestCertFiles_NoneNeeded(t *testing.T) {
	cfg := &CertWaitConfig{
		EnableWebhooks: false,
		MetricsAddr:    "0",
	}
	certFiles := cfg.CertFiles()
	if len(certFiles) != 0 {
		t.Fatalf("expected 0 certs, got %d", len(certFiles))
	}
}

func TestCertFiles_WebhooksDisabledIgnoresCertPath(t *testing.T) {
	cfg := &CertWaitConfig{
		EnableWebhooks:  false,
		WebhookCertPath: "/some/path",
		WebhookCertName: "tls.crt",
		WebhookCertKey:  "tls.key",
	}
	certFiles := cfg.CertFiles()
	if len(certFiles) != 0 {
		t.Fatalf("expected 0 certs when webhooks disabled, got %d", len(certFiles))
	}
}

func TestCertFiles_MetricsInsecureIgnoresCertPath(t *testing.T) {
	cfg := &CertWaitConfig{
		MetricsAddr:     ":8443",
		SecureMetrics:   false,
		MetricsCertPath: "/some/path",
		MetricsCertName: "tls.crt",
		MetricsCertKey:  "tls.key",
	}
	certFiles := cfg.CertFiles()
	if len(certFiles) != 0 {
		t.Fatalf("expected 0 certs when metrics insecure, got %d", len(certFiles))
	}
}

func TestCertWaitIntegration_TimeoutWhenCertsMissing(t *testing.T) {
	dir := t.TempDir()
	cfg := &CertWaitConfig{
		EnableWebhooks:  true,
		WebhookCertPath: dir,
		WebhookCertName: "tls.crt",
		WebhookCertKey:  "tls.key",
	}
	certFiles := cfg.CertFiles()

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	err := WaitForCerts(ctx, certFiles)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
}

func TestCertWaitIntegration_SucceedsWhenCertsExist(t *testing.T) {
	dir := t.TempDir()
	cfg := &CertWaitConfig{
		EnableWebhooks:  true,
		WebhookCertPath: dir,
		WebhookCertName: "tls.crt",
		WebhookCertKey:  "tls.key",
		MetricsAddr:     ":8443",
		SecureMetrics:   true,
		MetricsCertPath: dir,
		MetricsCertName: "metrics.crt",
		MetricsCertKey:  "metrics.key",
	}

	// Create all cert files
	for _, f := range cfg.CertFiles() {
		if err := os.WriteFile(f, []byte("data"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := WaitForCerts(ctx, cfg.CertFiles())
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}
