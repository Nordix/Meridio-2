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

package router

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	meridio2v1alpha1 "github.com/nordix/meridio-2/api/v1alpha1"
	"github.com/nordix/meridio-2/internal/common/readiness"
)

func TestIntegration_ReadinessGatingTriggersReconcile(t *testing.T) {
	logf.SetLogger(zap.New(zap.WriteTo(os.Stderr), zap.UseDevMode(true)))

	// Setup envtest
	// Resolve gateway-api CRD path from go module cache
	goModCache, err := exec.Command("go", "env", "GOMODCACHE").Output()
	require.NoError(t, err)

	testEnv := &envtest.Environment{
		CRDDirectoryPaths: []string{
			filepath.Join("..", "..", "..", "config", "crd", "bases"),
			filepath.Join(strings.TrimSpace(string(goModCache)), "sigs.k8s.io", "gateway-api@v1.4.1", "config", "crd", "standard"),
		},
		ErrorIfCRDPathMissing: false,
	}
	if dir := getFirstFoundEnvTestBinaryDir(); dir != "" {
		testEnv.BinaryAssetsDirectory = dir
	}

	cfg, err := testEnv.Start()
	require.NoError(t, err)
	defer func() { _ = testEnv.Stop() }()

	require.NoError(t, meridio2v1alpha1.AddToScheme(scheme.Scheme))
	require.NoError(t, gatewayapiv1.Install(scheme.Scheme))

	// Create manager
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:         scheme.Scheme,
		LeaderElection: false,
		Metrics:        metricsserver.Options{BindAddress: "0"},
	})
	require.NoError(t, err)

	// Setup readiness dir and mock bird
	dir := t.TempDir()
	mock := &mockingBird{}

	reconciler := &RouterReconciler{
		Client:           mgr.GetClient(),
		Scheme:           mgr.GetScheme(),
		GatewayName:      "test-gw",
		GatewayNamespace: "default",
		Bird:             mock,
		Readiness:        readiness.NewManager(dir),
	}
	require.NoError(t, reconciler.SetupWithManager(mgr))

	// Start manager
	ctx := t.Context()

	go func() { _ = mgr.Start(ctx) }()

	// Create the Gateway so reconcile has something to fetch
	k8sClient, err := client.New(cfg, client.Options{Scheme: scheme.Scheme})
	require.NoError(t, err)

	gw := &gatewayapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "test-gw", Namespace: "default"},
		Spec:       gatewayapiv1.GatewaySpec{GatewayClassName: "test-class", Listeners: []gatewayapiv1.Listener{{Name: "tcp", Port: 80, Protocol: gatewayapiv1.TCPProtocolType}}},
	}
	require.NoError(t, k8sClient.Create(ctx, gw))

	// Set VIP on gateway status
	gw.Status.Addresses = []gatewayapiv1.GatewayStatusAddress{
		{Type: ptr(gatewayapiv1.IPAddressType), Value: "10.0.0.1"},
	}
	require.NoError(t, k8sClient.Status().Update(ctx, gw))

	// Wait for initial reconcile (VIPs should be suppressed — dir is empty)
	time.Sleep(500 * time.Millisecond)
	assert.Nil(t, mock.configureVIPs, "VIPs should be suppressed when readiness dir is empty")

	// Create readiness file → should trigger reconcile automatically
	f, err := os.Create(filepath.Join(dir, readiness.LBReadyFilePrefix+"dg1"))
	require.NoError(t, err)
	_ = f.Close()

	// Wait for watcher to trigger reconcile
	assert.Eventually(t, func() bool {
		return len(mock.configureVIPs) > 0
	}, 5*time.Second, 100*time.Millisecond, "VIPs should be advertised after readiness file created")

	// Remove file → should trigger reconcile, VIPs suppressed again
	require.NoError(t, os.Remove(filepath.Join(dir, readiness.LBReadyFilePrefix+"dg1")))

	assert.Eventually(t, func() bool {
		return mock.configureVIPs == nil
	}, 5*time.Second, 100*time.Millisecond, "VIPs should be suppressed after readiness file removed")
}

func getFirstFoundEnvTestBinaryDir() string {
	basePath := filepath.Join("..", "..", "..", "bin", "k8s")
	entries, err := os.ReadDir(basePath)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if entry.IsDir() {
			return filepath.Join(basePath, entry.Name())
		}
	}
	return ""
}
