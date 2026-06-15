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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	meridio2v1alpha1 "github.com/nordix/meridio-2/api/v1alpha1"
)

func setupEnvTest(t *testing.T) client.Client {
	t.Helper()
	logf.SetLogger(zap.New(zap.WriteTo(os.Stderr), zap.UseDevMode(true)))

	testEnv := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}
	if dir := getFirstFoundEnvTestBinaryDir(); dir != "" {
		testEnv.BinaryAssetsDirectory = dir
	}

	cfg, err := testEnv.Start()
	require.NoError(t, err)
	t.Cleanup(func() { _ = testEnv.Stop() })

	require.NoError(t, meridio2v1alpha1.AddToScheme(scheme.Scheme))
	require.NoError(t, gatewayapiv1.Install(scheme.Scheme))

	k8sClient, err := client.New(cfg, client.Options{Scheme: scheme.Scheme})
	require.NoError(t, err)

	return k8sClient
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

// baseRouter returns a minimal valid GatewayRouter skeleton. Callers customize
// the returned object (protocol, bgp, static, address, etc.) before creating it.
func baseRouter(name string) *meridio2v1alpha1.GatewayRouter {
	return &meridio2v1alpha1.GatewayRouter{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: meridio2v1alpha1.GatewayRouterSpec{
			GatewayRef: gatewayapiv1.ParentReference{Name: "test-gateway"},
			Interface:  "ext-vlan",
			Address:    "169.254.100.1",
		},
	}
}

// fromYAML parses raw YAML into an unstructured object for cases where we need
// to test missing fields that Go structs would zero-fill.
func fromYAML(t *testing.T, raw string) *unstructured.Unstructured {
	t.Helper()
	obj := &unstructured.Unstructured{}
	require.NoError(t, yaml.NewYAMLToJSONDecoder(strings.NewReader(raw)).Decode(&obj.Object))
	return obj
}

func TestGatewayRouterCRDValidation(t *testing.T) {
	k8sClient := setupEnvTest(t)
	ctx := context.Background()

	t.Run("valid BGP router accepted", func(t *testing.T) {
		r := baseRouter("valid-bgp")
		r.Spec.Protocol = meridio2v1alpha1.RoutingProtocolBGP
		r.Spec.BGP = &meridio2v1alpha1.BgpSpec{RemoteASN: 65000, LocalASN: 65001}

		assert.NoError(t, k8sClient.Create(ctx, r))
	})

	t.Run("valid Static router accepted", func(t *testing.T) {
		r := baseRouter("valid-static")
		r.Spec.Protocol = meridio2v1alpha1.RoutingProtocolStatic
		r.Spec.Static = &meridio2v1alpha1.StaticSpec{
			BFD: &meridio2v1alpha1.BfdSpec{
				MinTx: "300ms", MinRx: "300ms", Multiplier: 3,
			},
		}

		assert.NoError(t, k8sClient.Create(ctx, r))
	})

	t.Run("BGP router without bgp field rejected", func(t *testing.T) {
		r := baseRouter("bgp-no-spec")
		r.Spec.Protocol = meridio2v1alpha1.RoutingProtocolBGP

		err := k8sClient.Create(ctx, r)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "bgp is required when protocol is BGP")
	})

	t.Run("Static router without static field rejected", func(t *testing.T) {
		r := baseRouter("static-no-spec")
		r.Spec.Protocol = meridio2v1alpha1.RoutingProtocolStatic

		err := k8sClient.Create(ctx, r)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "static is required when protocol is Static")
	})

	t.Run("BGP router with static field rejected", func(t *testing.T) {
		r := baseRouter("bgp-with-static")
		r.Spec.Protocol = meridio2v1alpha1.RoutingProtocolBGP
		r.Spec.BGP = &meridio2v1alpha1.BgpSpec{RemoteASN: 65000, LocalASN: 65001}
		r.Spec.Static = &meridio2v1alpha1.StaticSpec{
			BFD: &meridio2v1alpha1.BfdSpec{
				MinTx: "300ms", MinRx: "300ms", Multiplier: 3,
			},
		}

		err := k8sClient.Create(ctx, r)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "bgp and static are mutually exclusive")
	})

	t.Run("Static router with bgp field rejected", func(t *testing.T) {
		r := baseRouter("static-with-bgp")
		r.Spec.Protocol = meridio2v1alpha1.RoutingProtocolStatic
		r.Spec.Static = &meridio2v1alpha1.StaticSpec{
			BFD: &meridio2v1alpha1.BfdSpec{
				MinTx: "300ms", MinRx: "300ms", Multiplier: 3,
			},
		}
		r.Spec.BGP = &meridio2v1alpha1.BgpSpec{RemoteASN: 65000, LocalASN: 65001}

		err := k8sClient.Create(ctx, r)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "bgp and static are mutually exclusive")
	})

	t.Run("invalid protocol rejected", func(t *testing.T) {
		r := baseRouter("bad-protocol")
		r.Spec.Protocol = "OSPF"
		r.Spec.BGP = &meridio2v1alpha1.BgpSpec{RemoteASN: 65000, LocalASN: 65001}

		err := k8sClient.Create(ctx, r)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "Unsupported value")
	})

	t.Run("invalid address rejected", func(t *testing.T) {
		r := baseRouter("bad-address")
		r.Spec.Address = "not-an-ip"
		r.Spec.Protocol = meridio2v1alpha1.RoutingProtocolBGP
		r.Spec.BGP = &meridio2v1alpha1.BgpSpec{RemoteASN: 65000, LocalASN: 65001}

		err := k8sClient.Create(ctx, r)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "Must be an ip address")
	})

	t.Run("BGP immutable once set", func(t *testing.T) {
		r := baseRouter("immutable-bgp")
		r.Spec.Protocol = meridio2v1alpha1.RoutingProtocolBGP
		r.Spec.BGP = &meridio2v1alpha1.BgpSpec{RemoteASN: 65000, LocalASN: 65001}
		require.NoError(t, k8sClient.Create(ctx, r))

		fetched := &meridio2v1alpha1.GatewayRouter{}
		require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(r), fetched))

		fetched.Spec.BGP.RemoteASN = 99999
		err := k8sClient.Update(ctx, fetched)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "bgp is immutable once set")
	})

	t.Run("Static immutable once set", func(t *testing.T) {
		r := baseRouter("immutable-static")
		r.Spec.Protocol = meridio2v1alpha1.RoutingProtocolStatic
		r.Spec.Static = &meridio2v1alpha1.StaticSpec{
			BFD: &meridio2v1alpha1.BfdSpec{
				MinTx: "300ms", MinRx: "300ms", Multiplier: 3,
			},
		}
		require.NoError(t, k8sClient.Create(ctx, r))

		fetched := &meridio2v1alpha1.GatewayRouter{}
		require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(r), fetched))

		fetched.Spec.Static.BFD.MinTx = "500ms"
		err := k8sClient.Update(ctx, fetched)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "static is immutable once set")
	})

	t.Run("missing bfd multiplier rejected", func(t *testing.T) {
		obj := fromYAML(t, `
apiVersion: meridio-2.nordix.org/v1alpha1
kind: GatewayRouter
metadata:
  name: missing-multiplier
  namespace: default
spec:
  gatewayRef:
    name: test-gateway
  interface: ext-vlan
  address: "169.254.100.1"
  protocol: Static
  static:
    bfd:
      minTx: 300ms
      minRx: 300ms
`)
		err := k8sClient.Create(ctx, obj)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "multiplier")
	})

	t.Run("missing bfd minTx rejected", func(t *testing.T) {
		obj := fromYAML(t, `
apiVersion: meridio-2.nordix.org/v1alpha1
kind: GatewayRouter
metadata:
  name: missing-mintx
  namespace: default
spec:
  gatewayRef:
    name: test-gateway
  interface: ext-vlan
  address: "169.254.100.1"
  protocol: Static
  static:
    bfd:
      minRx: 300ms
      multiplier: 3
`)
		err := k8sClient.Create(ctx, obj)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "minTx")
	})
}
