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

package v1alpha1_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	meridio2v1alpha1 "github.com/nordix/meridio-2/api/v1alpha1"
)

// The envtest API server is shared by all CRD validation tests in the package
// and started lazily on first use, so tests that don't need it (e.g. the
// DeepCopy unit tests) neither pay the startup cost nor depend on the envtest
// binaries being installed.
var (
	envOnce   sync.Once
	testEnv   *envtest.Environment
	envClient client.Client
	envErr    error
)

func TestMain(m *testing.M) {
	code := m.Run() // runs all TestXxx functions, returns the exit code
	if testEnv != nil {
		_ = testEnv.Stop()
	}
	os.Exit(code)
}

// setupEnvTest returns a client for the shared envtest API server, starting it
// on the first call. Objects persist across tests for the lifetime of the
// server, so on cleanup every object of the given kinds is deleted from the
// "default" namespace the tests create them in. This keeps repeated runs in
// the same process (-count=N) from colliding on object names.
func setupEnvTest(t *testing.T, kinds ...client.Object) client.Client {
	t.Helper()
	envOnce.Do(func() { envClient, envErr = startEnvTest() })
	require.NoError(t, envErr)

	t.Cleanup(func() {
		for _, kind := range kinds {
			assert.NoError(t, envClient.DeleteAllOf(context.Background(), kind, client.InNamespace("default")))
		}
	})

	return envClient
}

func startEnvTest() (client.Client, error) {
	logf.SetLogger(zap.New(zap.WriteTo(os.Stderr), zap.UseDevMode(true)))

	testEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}
	if dir := getFirstFoundEnvTestBinaryDir(); dir != "" {
		testEnv.BinaryAssetsDirectory = dir
	}

	// On failure, testEnv is kept so that TestMain still calls Stop: Start can
	// fail after etcd and kube-apiserver are already running.
	cfg, err := testEnv.Start()
	if err != nil {
		return nil, fmt.Errorf("start envtest: %w", err)
	}

	if err := meridio2v1alpha1.AddToScheme(scheme.Scheme); err != nil {
		return nil, err
	}
	if err := gatewayapiv1.Install(scheme.Scheme); err != nil {
		return nil, err
	}

	return client.New(cfg, client.Options{Scheme: scheme.Scheme})
}

func getFirstFoundEnvTestBinaryDir() string {
	basePath := filepath.Join("..", "..", "bin", "k8s")
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
