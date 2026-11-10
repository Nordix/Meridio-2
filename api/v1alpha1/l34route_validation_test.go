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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	meridio2v1alpha1 "github.com/nordix/meridio-2/api/v1alpha1"
)

// baseL34Route returns a minimal valid L34Route. Callers customize the returned
// object before creating it.
func baseL34Route(name string) *meridio2v1alpha1.L34Route {
	return &meridio2v1alpha1.L34Route{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: meridio2v1alpha1.L34RouteSpec{
			ParentRefs:       []gatewayapiv1.ParentReference{{Name: "test-gateway"}},
			DestinationCIDRs: []string{"20.0.0.1/32"},
			DestinationPorts: []string{"80"},
			Protocols:        []meridio2v1alpha1.TransportProtocol{meridio2v1alpha1.TCP},
			Priority:         1,
		},
	}
}

// applyL34Route builds an unstructured L34Route for Server-Side Apply. The
// skeleton (parentRefs incl. defaulted group/kind, priority, protocols) is
// identical for every caller so that field managers only differ in their
// destinationCIDRs/destinationPorts entries.
func applyL34Route(name string, cidrs, ports []any) *unstructured.Unstructured {
	spec := map[string]any{
		"parentRefs": []any{map[string]any{
			"group": gatewayapiv1.GroupName, "kind": "Gateway", "name": "test-gateway",
		}},
		"priority":  int64(1),
		"protocols": []any{string(meridio2v1alpha1.TCP)},
	}
	if cidrs != nil {
		spec["destinationCIDRs"] = cidrs
	}
	if ports != nil {
		spec["destinationPorts"] = ports
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": meridio2v1alpha1.GroupVersion.String(),
		"kind":       "L34Route",
		"metadata":   map[string]any{"name": name, "namespace": "default"},
		"spec":       spec,
	}}
}

func TestL34RouteCRDValidation(t *testing.T) {
	k8sClient := setupEnvTest(t, &meridio2v1alpha1.L34Route{})
	ctx := context.Background()

	t.Run("valid route accepted", func(t *testing.T) {
		assert.NoError(t, k8sClient.Create(ctx, baseL34Route("valid")))
	})

	t.Run("empty destinationCIDRs rejected", func(t *testing.T) {
		r := baseL34Route("empty-dst-cidrs")
		r.Spec.DestinationCIDRs = []string{}

		err := k8sClient.Create(ctx, r)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "spec.destinationCIDRs")
	})

	t.Run("update to empty destinationCIDRs rejected", func(t *testing.T) {
		r := baseL34Route("update-to-empty")
		require.NoError(t, k8sClient.Create(ctx, r))

		r.Spec.DestinationCIDRs = []string{}
		err := k8sClient.Update(ctx, r)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "spec.destinationCIDRs")
	})

	t.Run("duplicate destinationCIDRs rejected", func(t *testing.T) {
		r := baseL34Route("dup-dst-cidrs")
		r.Spec.DestinationCIDRs = []string{"20.0.0.1/32", "20.0.0.1/32"}

		err := k8sClient.Create(ctx, r)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "Duplicate value")
	})

	t.Run("duplicate destinationPorts rejected", func(t *testing.T) {
		r := baseL34Route("dup-dst-ports")
		r.Spec.DestinationPorts = []string{"80", "80"}

		err := k8sClient.Create(ctx, r)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "Duplicate value")
	})

	// The tests below cover pre-existing (non multi-writer SSA) usage, which the
	// set list type must not change: Update, merge patch and a single SSA owner
	// still replace the whole list.

	t.Run("multiple distinct entries accepted", func(t *testing.T) {
		r := baseL34Route("multi-entries")
		r.Spec.DestinationCIDRs = []string{"20.0.0.1/32", "20.0.0.2/32", "2001:db8::1/128"}
		r.Spec.DestinationPorts = []string{"80", "443", "8000-8080"}

		assert.NoError(t, k8sClient.Create(ctx, r))
	})

	t.Run("Update replaces the whole list", func(t *testing.T) {
		r := baseL34Route("update-replace")
		r.Spec.DestinationCIDRs = []string{"20.0.0.1/32", "20.0.0.2/32"}
		r.Spec.DestinationPorts = []string{"80", "443"}
		require.NoError(t, k8sClient.Create(ctx, r, client.FieldOwner("writer-a")))

		// A different writer removes and replaces entries it did not add.
		r.Spec.DestinationCIDRs = []string{"20.0.0.3/32"}
		r.Spec.DestinationPorts = nil
		require.NoError(t, k8sClient.Update(ctx, r, client.FieldOwner("writer-b")))

		got := &meridio2v1alpha1.L34Route{}
		require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(r), got))
		assert.Equal(t, []string{"20.0.0.3/32"}, got.Spec.DestinationCIDRs)
		assert.Empty(t, got.Spec.DestinationPorts)
	})

	t.Run("merge patch replaces the whole list", func(t *testing.T) {
		r := baseL34Route("merge-patch-replace")
		r.Spec.DestinationCIDRs = []string{"20.0.0.1/32", "20.0.0.2/32"}
		r.Spec.DestinationPorts = []string{"80", "443"}
		require.NoError(t, k8sClient.Create(ctx, r))

		// Same request kubectl's client-side apply sends for custom resources.
		patch := client.RawPatch(types.MergePatchType,
			[]byte(`{"spec":{"destinationCIDRs":["20.0.0.2/32"],"destinationPorts":["8080"]}}`))
		require.NoError(t, k8sClient.Patch(ctx, r, patch))

		got := &meridio2v1alpha1.L34Route{}
		require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(r), got))
		assert.Equal(t, []string{"20.0.0.2/32"}, got.Spec.DestinationCIDRs)
		assert.Equal(t, []string{"8080"}, got.Spec.DestinationPorts)
	})

	t.Run("single Server-Side Apply owner adds and removes entries", func(t *testing.T) {
		const name = "ssa-single-owner"
		apply := func(cidrs, ports []any) error {
			return k8sClient.Apply(ctx, client.ApplyConfigurationFromUnstructured(applyL34Route(name, cidrs, ports)),
				client.FieldOwner("gitops"))
		}

		require.NoError(t, apply([]any{"20.0.2.1/32", "20.0.2.2/32"}, []any{"80", "443"}))
		require.NoError(t, apply([]any{"20.0.2.2/32", "20.0.2.3/32"}, []any{"443"}))

		got := &meridio2v1alpha1.L34Route{}
		require.NoError(t, k8sClient.Get(ctx, client.ObjectKey{Namespace: "default", Name: name}, got))
		assert.ElementsMatch(t, []string{"20.0.2.2/32", "20.0.2.3/32"}, got.Spec.DestinationCIDRs)
		assert.Equal(t, []string{"443"}, got.Spec.DestinationPorts)
	})

	t.Run("Server-Side Apply merges entries from multiple field managers", func(t *testing.T) {
		const name = "ssa-merge"
		apply := func(manager string, cidrs, ports []any) error {
			return k8sClient.Apply(ctx, client.ApplyConfigurationFromUnstructured(applyL34Route(name, cidrs, ports)),
				client.FieldOwner(manager))
		}
		get := func() *meridio2v1alpha1.L34Route {
			r := &meridio2v1alpha1.L34Route{}
			require.NoError(t, k8sClient.Get(ctx, client.ObjectKey{Namespace: "default", Name: name}, r))
			return r
		}

		require.NoError(t, apply("endpoint-a", []any{"20.0.1.1/32"}, []any{"1000"}))
		require.NoError(t, apply("endpoint-b", []any{"20.0.1.2/32"}, []any{"2000"}))

		r := get()
		assert.ElementsMatch(t, []string{"20.0.1.1/32", "20.0.1.2/32"}, r.Spec.DestinationCIDRs)
		assert.ElementsMatch(t, []string{"1000", "2000"}, r.Spec.DestinationPorts)

		// Releasing endpoint-a's entries leaves endpoint-b's in place.
		require.NoError(t, apply("endpoint-a", nil, nil))

		r = get()
		assert.Equal(t, []string{"20.0.1.2/32"}, r.Spec.DestinationCIDRs)
		assert.Equal(t, []string{"2000"}, r.Spec.DestinationPorts)
	})
}
