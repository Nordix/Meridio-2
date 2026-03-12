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

package gateway

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	netdefv1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	meridio2v1alpha1 "github.com/nordix/meridio-2/api/v1alpha1"
	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// --- validationError ---

func TestValidationError(t *testing.T) {
	err := &validationError{message: "test error"}
	assert.Equal(t, "test error", err.Error())

	var valErr *validationError
	assert.True(t, errors.As(err, &valErr))
}

// --- validateGateway ---

func TestValidateGateway(t *testing.T) {
	t.Run("MissingInfrastructure", func(t *testing.T) {
		gw := &gatewayv1.Gateway{
			Spec: gatewayv1.GatewaySpec{Infrastructure: nil},
		}
		reconciler, _ := setupReconciler(gw)

		_, _, err := reconciler.validateGateway(context.Background(), gw)

		var valErr *validationError
		assert.True(t, errors.As(err, &valErr))
		assert.Contains(t, err.Error(), "GatewayConfiguration reference is required")
	})

	t.Run("MissingParametersRef", func(t *testing.T) {
		gw := &gatewayv1.Gateway{
			Spec: gatewayv1.GatewaySpec{
				Infrastructure: &gatewayv1.GatewayInfrastructure{ParametersRef: nil},
			},
		}
		reconciler, _ := setupReconciler(gw)

		_, _, err := reconciler.validateGateway(context.Background(), gw)

		var valErr *validationError
		assert.True(t, errors.As(err, &valErr))
		assert.Contains(t, err.Error(), "GatewayConfiguration reference is required")
	})

	t.Run("GatewayConfigurationNotFound", func(t *testing.T) {
		gw := &gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{Name: "test-gw", Namespace: testNamespace},
			Spec: gatewayv1.GatewaySpec{
				Infrastructure: &gatewayv1.GatewayInfrastructure{
					ParametersRef: &gatewayv1.LocalParametersReference{
						Group: gatewayv1.Group(meridio2v1alpha1.GroupVersion.Group),
						Kind:  gatewayv1.Kind(kindGatewayConfiguration),
						Name:  "missing-config",
					},
				},
			},
		}
		reconciler, _ := setupReconciler(gw)

		_, _, err := reconciler.validateGateway(context.Background(), gw)

		var valErr *validationError
		assert.True(t, errors.As(err, &valErr))
		assert.Contains(t, err.Error(), "not found")
	})

	t.Run("UnsupportedParametersRefGroupKind", func(t *testing.T) {
		gw := &gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{Name: "test-gw", Namespace: testNamespace},
			Spec: gatewayv1.GatewaySpec{
				Infrastructure: &gatewayv1.GatewayInfrastructure{
					ParametersRef: &gatewayv1.LocalParametersReference{
						Group: "wrong.group.io",
						Kind:  "WrongKind",
						Name:  "some-config",
					},
				},
			},
		}
		reconciler, _ := setupReconciler(gw)

		_, _, err := reconciler.validateGateway(context.Background(), gw)

		var valErr *validationError
		assert.True(t, errors.As(err, &valErr))
		assert.Contains(t, err.Error(), "unsupported parametersRef")
	})

	t.Run("TemplateMissing", func(t *testing.T) {
		gwConfig := newGatewayConfiguration()
		gw := &gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{Name: "test-gw", Namespace: testNamespace},
		}
		attachGatewayConfiguration(gw, gwConfig)
		reconciler, _ := setupReconciler(gw, gwConfig)
		reconciler.TemplatePath = "/nonexistent/path"

		_, _, err := reconciler.validateGateway(context.Background(), gw)

		var tmplErr *templateError
		assert.True(t, errors.As(err, &tmplErr))
	})

	t.Run("InvalidGatewayConfiguration", func(t *testing.T) {
		gwConfig := &meridio2v1alpha1.GatewayConfiguration{
			ObjectMeta: metav1.ObjectMeta{Name: "test-gwconfig", Namespace: testNamespace},
			Spec: meridio2v1alpha1.GatewayConfigurationSpec{
				NetworkSubnets: []meridio2v1alpha1.NetworkSubnet{}, // empty = invalid
			},
		}
		gw := &gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{Name: "test-gw", Namespace: testNamespace},
		}
		attachGatewayConfiguration(gw, gwConfig)

		tmpDir := t.TempDir()
		writeTemplate(t, tmpDir, minimalTemplateYAML)
		reconciler, _ := setupReconciler(gw, gwConfig)
		reconciler.TemplatePath = tmpDir

		_, _, err := reconciler.validateGateway(context.Background(), gw)

		var valErr *validationError
		assert.True(t, errors.As(err, &valErr))
		assert.Contains(t, err.Error(), "at least one networkSubnet")
	})

	t.Run("Valid", func(t *testing.T) {
		gwConfig := newGatewayConfiguration()
		gw := &gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{Name: "test-gw", Namespace: testNamespace},
		}
		attachGatewayConfiguration(gw, gwConfig)

		tmpDir := t.TempDir()
		writeTemplate(t, tmpDir, minimalTemplateYAML)
		reconciler, _ := setupReconciler(gw, gwConfig)
		reconciler.TemplatePath = tmpDir

		gwConfigResult, template, err := reconciler.validateGateway(context.Background(), gw)

		assert.NoError(t, err)
		assert.NotNil(t, gwConfigResult)
		assert.NotNil(t, template)
	})
}

// --- validateNetworkSubnets ---

func TestValidateNetworkSubnets(t *testing.T) {
	t.Run("Empty", func(t *testing.T) {
		err := validateNetworkSubnets([]meridio2v1alpha1.NetworkSubnet{})
		assert.Contains(t, err.Error(), "at least one networkSubnet")
	})

	t.Run("UnsupportedAttachmentType", func(t *testing.T) {
		err := validateNetworkSubnets([]meridio2v1alpha1.NetworkSubnet{
			{AttachmentType: "DRA", CIDRs: []string{"10.0.0.0/24"}},
		})
		assert.Contains(t, err.Error(), "only NAD attachment type is supported")
	})

	t.Run("InvalidCIDR", func(t *testing.T) {
		err := validateNetworkSubnets([]meridio2v1alpha1.NetworkSubnet{
			{AttachmentType: "NAD", CIDRs: []string{"not-a-cidr"}},
		})
		assert.Contains(t, err.Error(), "invalid CIDR")
	})

	t.Run("Valid", func(t *testing.T) {
		err := validateNetworkSubnets([]meridio2v1alpha1.NetworkSubnet{
			{AttachmentType: "NAD", CIDRs: []string{"192.168.1.0/24"}},
		})
		assert.NoError(t, err)
	})

	t.Run("OverlappingCIDRsSameSubnet", func(t *testing.T) {
		err := validateNetworkSubnets([]meridio2v1alpha1.NetworkSubnet{
			{AttachmentType: "NAD", CIDRs: []string{"10.0.0.0/8", "10.1.0.0/16"}},
		})
		assert.Contains(t, err.Error(), "overlapping")
	})

	t.Run("OverlappingCIDRsAcrossSubnets", func(t *testing.T) {
		err := validateNetworkSubnets([]meridio2v1alpha1.NetworkSubnet{
			{AttachmentType: "NAD", CIDRs: []string{"192.168.0.0/16"}},
			{AttachmentType: "NAD", CIDRs: []string{"192.168.1.0/24"}},
		})
		assert.Contains(t, err.Error(), "overlapping")
	})

	t.Run("OverlappingIPv6SameSubnet", func(t *testing.T) {
		err := validateNetworkSubnets([]meridio2v1alpha1.NetworkSubnet{
			{AttachmentType: "NAD", CIDRs: []string{"fd00::/48", "fd00:0:0:1::/64"}},
		})
		assert.Contains(t, err.Error(), "overlapping")
	})

	t.Run("OverlappingIPv6AcrossSubnets", func(t *testing.T) {
		err := validateNetworkSubnets([]meridio2v1alpha1.NetworkSubnet{
			{AttachmentType: "NAD", CIDRs: []string{"2001:db8::/32"}},
			{AttachmentType: "NAD", CIDRs: []string{"2001:db8:1::/48"}},
		})
		assert.Contains(t, err.Error(), "overlapping")
	})

	t.Run("NonOverlappingIPv6", func(t *testing.T) {
		err := validateNetworkSubnets([]meridio2v1alpha1.NetworkSubnet{
			{AttachmentType: "NAD", CIDRs: []string{"fd00:1::/64", "fd00:2::/64"}},
		})
		assert.NoError(t, err)
	})

	t.Run("NonOverlappingDualStack", func(t *testing.T) {
		err := validateNetworkSubnets([]meridio2v1alpha1.NetworkSubnet{
			{AttachmentType: "NAD", CIDRs: []string{"192.168.1.0/24"}},
			{AttachmentType: "NAD", CIDRs: []string{"fd00::/64"}},
		})
		assert.NoError(t, err)
	})
}

// --- validateCIDR ---

func TestValidateCIDR(t *testing.T) {
	t.Run("Invalid", func(t *testing.T) {
		assert.Error(t, validateCIDR("not-a-cidr"))
	})
	t.Run("DefaultRouteIPv4", func(t *testing.T) {
		assert.Contains(t, validateCIDR("0.0.0.0/0").Error(), "default route")
	})
	t.Run("DefaultRouteIPv6", func(t *testing.T) {
		assert.Contains(t, validateCIDR("::/0").Error(), "default route")
	})
	t.Run("LinkLocal", func(t *testing.T) {
		assert.Contains(t, validateCIDR("fe80::/10").Error(), "link-local")
	})
	t.Run("ValidIPv4", func(t *testing.T) {
		assert.NoError(t, validateCIDR("10.0.0.0/8"))
	})
	t.Run("ValidIPv6", func(t *testing.T) {
		assert.NoError(t, validateCIDR("2001:db8::/32"))
	})
}

// --- validateNetworkAttachments ---

func TestValidateNetworkAttachments(t *testing.T) {
	t.Run("Empty", func(t *testing.T) {
		assert.NoError(t, validateNetworkAttachments([]meridio2v1alpha1.NetworkAttachment{}))
	})

	t.Run("UnsupportedType", func(t *testing.T) {
		err := validateNetworkAttachments([]meridio2v1alpha1.NetworkAttachment{
			{Type: "DRA"},
		})
		assert.Contains(t, err.Error(), "only NAD type is supported")
	})

	t.Run("MissingNADConfig", func(t *testing.T) {
		err := validateNetworkAttachments([]meridio2v1alpha1.NetworkAttachment{
			{Type: "NAD", NAD: nil},
		})
		assert.Contains(t, err.Error(), "NAD configuration is required")
	})

	t.Run("EmptyName", func(t *testing.T) {
		err := validateNetworkAttachments([]meridio2v1alpha1.NetworkAttachment{
			{Type: "NAD", NAD: &meridio2v1alpha1.NAD{Name: "", Interface: "net1"}},
		})
		assert.Contains(t, err.Error(), "NAD name is required")
	})

	t.Run("EmptyInterface", func(t *testing.T) {
		err := validateNetworkAttachments([]meridio2v1alpha1.NetworkAttachment{
			{Type: "NAD", NAD: &meridio2v1alpha1.NAD{Name: "nad1", Interface: ""}},
		})
		assert.Contains(t, err.Error(), "interface name is required")
	})

	t.Run("DuplicateInterface", func(t *testing.T) {
		err := validateNetworkAttachments([]meridio2v1alpha1.NetworkAttachment{
			{Type: "NAD", NAD: &meridio2v1alpha1.NAD{Name: "nad1", Interface: "net1"}},
			{Type: "NAD", NAD: &meridio2v1alpha1.NAD{Name: "nad2", Interface: "net1"}},
		})
		assert.Contains(t, err.Error(), "duplicate interface name")
	})

	t.Run("Valid", func(t *testing.T) {
		err := validateNetworkAttachments([]meridio2v1alpha1.NetworkAttachment{
			{Type: "NAD", NAD: &meridio2v1alpha1.NAD{Name: "nad1", Interface: "net1"}},
			{Type: "NAD", NAD: &meridio2v1alpha1.NAD{Name: "nad2", Interface: "net2"}},
		})
		assert.NoError(t, err)
	})
}

// --- validateVerticalScaling ---

func TestValidateVerticalScaling(t *testing.T) {
	t.Run("Nil", func(t *testing.T) {
		assert.NoError(t, validateVerticalScaling(nil))
	})

	t.Run("ResourceClaims", func(t *testing.T) {
		err := validateVerticalScaling(&meridio2v1alpha1.VerticalScaling{
			Containers: []meridio2v1alpha1.ContainerArgs{
				{
					Name: "test",
					Resources: corev1.ResourceRequirements{
						Claims: []corev1.ResourceClaim{{Name: "test-claim"}},
					},
				},
			},
		})
		assert.Contains(t, err.Error(), "ResourceClaims are not supported")
	})

	t.Run("Valid", func(t *testing.T) {
		err := validateVerticalScaling(&meridio2v1alpha1.VerticalScaling{
			Containers: []meridio2v1alpha1.ContainerArgs{
				{Name: "test"},
			},
		})
		assert.NoError(t, err)
	})
}

// --- validateMergedNetworkAttachments ---

func TestValidateMergedNetworkAttachments(t *testing.T) {
	t.Run("NoTemplateAnnotations", func(t *testing.T) {
		gwConfig := &meridio2v1alpha1.GatewayConfiguration{
			ObjectMeta: metav1.ObjectMeta{Namespace: testNamespace},
		}
		template := &appsv1.Deployment{}

		assert.NoError(t, validateMergedNetworkAttachments(gwConfig, template))
	})

	t.Run("EmptyTemplateNADAnnotation", func(t *testing.T) {
		gwConfig := &meridio2v1alpha1.GatewayConfiguration{
			ObjectMeta: metav1.ObjectMeta{Namespace: testNamespace},
		}
		template := &appsv1.Deployment{}
		template.Spec.Template.Annotations = map[string]string{"other": "value"}

		assert.NoError(t, validateMergedNetworkAttachments(gwConfig, template))
	})

	t.Run("NoConflict", func(t *testing.T) {
		gwConfig := &meridio2v1alpha1.GatewayConfiguration{
			ObjectMeta: metav1.ObjectMeta{Namespace: testNamespace},
			Spec: meridio2v1alpha1.GatewayConfigurationSpec{
				NetworkAttachments: []meridio2v1alpha1.NetworkAttachment{
					{Type: "NAD", NAD: &meridio2v1alpha1.NAD{Name: "nad1", Namespace: testNamespace, Interface: "net1"}},
				},
			},
		}
		template := templateWithNADs(`[{"name":"nad2","namespace":"default","interface":"net2"}]`)

		assert.NoError(t, validateMergedNetworkAttachments(gwConfig, template))
	})

	t.Run("SameTripletRecognizedAsOverride_NoDuplicate", func(t *testing.T) {
		gwConfig := &meridio2v1alpha1.GatewayConfiguration{
			ObjectMeta: metav1.ObjectMeta{Namespace: testNamespace},
			Spec: meridio2v1alpha1.GatewayConfigurationSpec{
				NetworkAttachments: []meridio2v1alpha1.NetworkAttachment{
					{Type: "NAD", NAD: &meridio2v1alpha1.NAD{Name: "nad1", Namespace: testNamespace, Interface: "net1"}},
				},
			},
		}
		template := templateWithNADs(`[{"name":"nad1","namespace":"default","interface":"net1"}]`)

		assert.NoError(t, validateMergedNetworkAttachments(gwConfig, template))
	})

	t.Run("DuplicateInterfaceBetweenTemplateAndGwConfig", func(t *testing.T) {
		gwConfig := &meridio2v1alpha1.GatewayConfiguration{
			ObjectMeta: metav1.ObjectMeta{Namespace: testNamespace},
			Spec: meridio2v1alpha1.GatewayConfigurationSpec{
				NetworkAttachments: []meridio2v1alpha1.NetworkAttachment{
					{Type: "NAD", NAD: &meridio2v1alpha1.NAD{Name: "nad1", Namespace: testNamespace, Interface: "net1"}},
				},
			},
		}
		// Different NAD but same interface name
		template := templateWithNADs(`[{"name":"nad2","namespace":"default","interface":"net1"}]`)

		err := validateMergedNetworkAttachments(gwConfig, template)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), `duplicate interface name "net1"`)
	})

	t.Run("TemplateNADDefaultsToGwConfigNamespace", func(t *testing.T) {
		gwConfig := &meridio2v1alpha1.GatewayConfiguration{
			ObjectMeta: metav1.ObjectMeta{Namespace: "prod"},
			Spec: meridio2v1alpha1.GatewayConfigurationSpec{
				NetworkAttachments: []meridio2v1alpha1.NetworkAttachment{
					{Type: "NAD", NAD: &meridio2v1alpha1.NAD{Name: "nad1", Namespace: "prod", Interface: "net1"}},
				},
			},
		}
		// Template NAD without namespace - defaults to gwConfig.Namespace ("prod")
		// Same triplet as GwConfig NAD, so GwConfig overrides - no duplicate
		template := templateWithNADs(`[{"name":"nad1","interface":"net1"}]`)

		assert.NoError(t, validateMergedNetworkAttachments(gwConfig, template))
	})

	t.Run("GwConfigNADDefaultsToGwConfigNamespace", func(t *testing.T) {
		gwConfig := &meridio2v1alpha1.GatewayConfiguration{
			ObjectMeta: metav1.ObjectMeta{Namespace: "prod"},
			Spec: meridio2v1alpha1.GatewayConfigurationSpec{
				NetworkAttachments: []meridio2v1alpha1.NetworkAttachment{
					// No namespace - defaults to gwConfig.Namespace ("prod")
					{Type: "NAD", NAD: &meridio2v1alpha1.NAD{Name: "nad1", Interface: "net1"}},
				},
			},
		}
		template := templateWithNADs(`[{"name":"nad1","namespace":"prod","interface":"net1"}]`)

		assert.NoError(t, validateMergedNetworkAttachments(gwConfig, template))
	})

	t.Run("DifferentNamespacesNotOverridden", func(t *testing.T) {
		gwConfig := &meridio2v1alpha1.GatewayConfiguration{
			ObjectMeta: metav1.ObjectMeta{Namespace: testNamespace},
			Spec: meridio2v1alpha1.GatewayConfigurationSpec{
				NetworkAttachments: []meridio2v1alpha1.NetworkAttachment{
					{Type: "NAD", NAD: &meridio2v1alpha1.NAD{Name: "nad1", Namespace: "other-ns", Interface: "net1"}},
				},
			},
		}
		// Same name/interface but different namespace - not overridden, duplicate interface
		template := templateWithNADs(`[{"name":"nad1","namespace":"default","interface":"net1"}]`)

		err := validateMergedNetworkAttachments(gwConfig, template)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), `duplicate interface name "net1"`)
	})

	t.Run("NoGwConfigAttachments", func(t *testing.T) {
		gwConfig := &meridio2v1alpha1.GatewayConfiguration{
			ObjectMeta: metav1.ObjectMeta{Namespace: testNamespace},
		}
		template := templateWithNADs(`[{"name":"nad1","namespace":"default","interface":"net1"}]`)

		assert.NoError(t, validateMergedNetworkAttachments(gwConfig, template))
	})
}

// --- helpers ---

const minimalTemplateYAML = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: placeholder
spec:
  replicas: 1
  selector:
    matchLabels:
      app: placeholder
  template:
    metadata:
      labels:
        app: placeholder
    spec:
      containers:
      - name: loadbalancer
        image: registry.nordix.org/cloud-native/meridio-2/loadbalancer:latest
`

func writeTemplate(t *testing.T, dir, yaml string) {
	t.Helper()
	err := os.WriteFile(filepath.Join(dir, LBDeploymentTemplateFile), []byte(yaml), 0644)
	assert.NoError(t, err)
}

func templateWithNADs(nadJSON string) *appsv1.Deployment {
	return &appsv1.Deployment{
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						netdefv1.NetworkAttachmentAnnot: nadJSON,
					},
				},
			},
		},
	}
}
