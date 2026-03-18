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
	"strings"
	"testing"

	netdefv1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	meridio2v1alpha1 "github.com/nordix/meridio-2/api/v1alpha1"
	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func TestLbDeploymentName(t *testing.T) {
	gw := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "my-gateway"},
	}

	name := lbDeploymentName(gw)

	assert.Equal(t, "sllb-my-gateway", name)
}

func TestSetControllerLabels(t *testing.T) {
	t.Run("SetsRequiredLabels", func(t *testing.T) {
		meta := &metav1.ObjectMeta{}

		setControllerLabels(meta, "sllb-test", "test-gw")

		assert.Equal(t, "sllb-test", meta.Labels["app"])
		assert.Equal(t, "test-gw", meta.Labels[labelGatewayName])
		assert.Equal(t, managedByValue, meta.Labels[labelManagedBy])
	})

	t.Run("InitializesLabelsMapIfNil", func(t *testing.T) {
		meta := &metav1.ObjectMeta{Labels: nil}

		setControllerLabels(meta, "sllb-test", "test-gw")

		assert.NotNil(t, meta.Labels)
		assert.Len(t, meta.Labels, 3)
	})

	t.Run("PreservesExistingLabels", func(t *testing.T) {
		meta := &metav1.ObjectMeta{
			Labels: map[string]string{"existing": "label"},
		}

		setControllerLabels(meta, "sllb-test", "test-gw")

		assert.Equal(t, "label", meta.Labels["existing"])
		assert.Len(t, meta.Labels, 4)
	})
}

func TestMergeInfrastructureMetadata(t *testing.T) {
	t.Run("MergesLabelsAndAnnotations", func(t *testing.T) {
		gw := &gatewayv1.Gateway{
			Spec: gatewayv1.GatewaySpec{
				Infrastructure: &gatewayv1.GatewayInfrastructure{
					Labels: map[gatewayv1.LabelKey]gatewayv1.LabelValue{
						"infra-label": "value1",
					},
					Annotations: map[gatewayv1.AnnotationKey]gatewayv1.AnnotationValue{
						"infra-annotation": "value2",
					},
				},
			},
		}
		meta := &metav1.ObjectMeta{}

		mergeInfrastructureMetadata(meta, gw)

		assert.Equal(t, "value1", meta.Labels["infra-label"])
		assert.Equal(t, "value2", meta.Annotations["infra-annotation"])
	})

	t.Run("NoOpIfInfrastructureIsNil", func(t *testing.T) {
		gw := &gatewayv1.Gateway{
			Spec: gatewayv1.GatewaySpec{Infrastructure: nil},
		}
		meta := &metav1.ObjectMeta{}

		mergeInfrastructureMetadata(meta, gw)

		assert.Nil(t, meta.Labels)
		assert.Nil(t, meta.Annotations)
	})

	t.Run("CanOverrideExistingLabels", func(t *testing.T) {
		gw := &gatewayv1.Gateway{
			Spec: gatewayv1.GatewaySpec{
				Infrastructure: &gatewayv1.GatewayInfrastructure{
					Labels: map[gatewayv1.LabelKey]gatewayv1.LabelValue{
						"app": "overridden",
					},
				},
			},
		}
		meta := &metav1.ObjectMeta{
			Labels: map[string]string{"app": "original"},
		}

		mergeInfrastructureMetadata(meta, gw)

		assert.Equal(t, "overridden", meta.Labels["app"])
	})
}

func TestDeploymentNeedsUpdate(t *testing.T) {
	t.Run("NoChangeNeeded", func(t *testing.T) {
		existing := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Labels:      map[string]string{"app": "test"},
				Annotations: map[string]string{"key": "value"},
			},
			Spec: appsv1.DeploymentSpec{
				Replicas: ptr(int32(2)),
			},
		}
		desired := existing.DeepCopy()

		assert.False(t, deploymentNeedsUpdate(existing, desired))
	})

	t.Run("SpecChanged", func(t *testing.T) {
		existing := &appsv1.Deployment{
			Spec: appsv1.DeploymentSpec{Replicas: ptr(int32(2))},
		}
		desired := &appsv1.Deployment{
			Spec: appsv1.DeploymentSpec{Replicas: ptr(int32(3))},
		}

		assert.True(t, deploymentNeedsUpdate(existing, desired))
	})

	t.Run("LabelsChanged", func(t *testing.T) {
		existing := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "old"}},
		}
		desired := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "new"}},
		}

		assert.True(t, deploymentNeedsUpdate(existing, desired))
	})

	t.Run("AnnotationsChanged", func(t *testing.T) {
		existing := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{"key": "old"}},
		}
		desired := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{"key": "new"}},
		}

		assert.True(t, deploymentNeedsUpdate(existing, desired))
	})
}

func TestMergeMaps(t *testing.T) {
	t.Run("MergesNonOverlapping", func(t *testing.T) {
		base := map[string]string{"a": "1", "b": "2"}
		overwrite := map[string]string{"c": "3", "d": "4"}

		result := mergeMaps(base, overwrite)

		assert.Len(t, result, 4)
		assert.Equal(t, "1", result["a"])
		assert.Equal(t, "3", result["c"])
	})

	t.Run("OverwritesTakesPrecedence", func(t *testing.T) {
		base := map[string]string{"a": "old", "b": "2"}
		overwrite := map[string]string{"a": "new", "c": "3"}

		result := mergeMaps(base, overwrite)

		assert.Equal(t, "new", result["a"])
		assert.Equal(t, "2", result["b"])
		assert.Equal(t, "3", result["c"])
	})

	t.Run("HandlesNilMaps", func(t *testing.T) {
		result := mergeMaps(nil, map[string]string{"a": "1"})
		assert.Equal(t, "1", result["a"])

		result = mergeMaps(map[string]string{"a": "1"}, nil)
		assert.Equal(t, "1", result["a"])
	})
}

func ptr[T any](v T) *T {
	return &v
}

func TestReconcileLBDeployment(t *testing.T) {
	template := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "placeholder",
			Labels: map[string]string{"template-label": "template-value"},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr(int32(1)),
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "placeholder"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "placeholder"}},
				Spec: corev1.PodSpec{
					ServiceAccountName: "placeholder",
					Containers: []corev1.Container{
						{Name: "loadbalancer", Image: "registry.nordix.org/cloud-native/meridio-2/loadbalancer:latest"},
					},
				},
			},
		},
	}

	t.Run("CreatesDeploymentFromTemplate", func(t *testing.T) {
		gwClass := newGatewayClass(testControllerName)
		gw := newGateway(gwClass.Name)
		gwConfig := newGatewayConfiguration()
		reconciler, fakeClient := setupReconciler(gwClass, gw, gwConfig)

		err := reconciler.reconcileLBDeployment(context.Background(), gw, gwConfig, template)
		assert.NoError(t, err)

		var deployment appsv1.Deployment
		err = fakeClient.Get(context.Background(), client.ObjectKey{
			Namespace: gw.Namespace,
			Name:      "sllb-" + gw.Name,
		}, &deployment)
		assert.NoError(t, err)
		assert.Equal(t, "sllb-"+gw.Name, deployment.Name)
		assert.Equal(t, "template-value", deployment.Labels["template-label"])
		assert.Equal(t, gw.Name, deployment.Labels[labelGatewayName])
	})

	t.Run("UpdatesExistingDeployment", func(t *testing.T) {
		gwClass := newGatewayClass(testControllerName)
		gw := newGateway(gwClass.Name)
		gwConfig := newGatewayConfiguration()

		updatedTemplate := template.DeepCopy()
		updatedTemplate.Labels["template-label"] = "new-value"
		updatedTemplate.Spec.Template.Spec.Containers[0].Image = "registry.nordix.org/cloud-native/meridio-2/loadbalancer:v2"

		existing := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "sllb-" + gw.Name,
				Namespace: gw.Namespace,
				Labels: map[string]string{
					"template-label": "old-value",
					labelGatewayName: gw.Name,
					"external-label": "preserved",
				},
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion:         gatewayv1.GroupVersion.String(),
						Kind:               kindGateway,
						Name:               gw.Name,
						UID:                gw.UID,
						Controller:         ptr(true),
						BlockOwnerDeletion: ptr(true),
					},
				},
			},
			Spec: appsv1.DeploymentSpec{
				Replicas: ptr(int32(1)),
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"app": "sllb-" + gw.Name},
				},
			},
		}

		reconciler, fakeClient := setupReconciler(gwClass, gw, gwConfig, existing)

		err := reconciler.reconcileLBDeployment(context.Background(), gw, gwConfig, updatedTemplate)
		assert.NoError(t, err)

		var deployment appsv1.Deployment
		err = fakeClient.Get(context.Background(), client.ObjectKey{
			Namespace: gw.Namespace,
			Name:      "sllb-" + gw.Name,
		}, &deployment)
		assert.NoError(t, err)
		assert.Equal(t, "new-value", deployment.Labels["template-label"])
		assert.Equal(t, "preserved", deployment.Labels["external-label"])
	})

	t.Run("NameCollision_ReturnsError", func(t *testing.T) {
		gwClass := newGatewayClass(testControllerName)
		gw := newGateway(gwClass.Name)
		gwConfig := newGatewayConfiguration()
		deploymentName := lbDeploymentName(gw)
		existing := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      deploymentName,
				Namespace: gw.Namespace,
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion:         gatewayv1.GroupVersion.String(),
						Kind:               kindGateway,
						Name:               "other-gateway",
						UID:                "other-uid",
						Controller:         ptr(true),
						BlockOwnerDeletion: ptr(true),
					},
				},
			},
			Spec: appsv1.DeploymentSpec{
				Replicas: ptr(int32(1)),
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"app": deploymentName},
				},
			},
		}

		reconciler, _ := setupReconciler(gwClass, gw, gwConfig, existing)

		err := reconciler.reconcileLBDeployment(context.Background(), gw, gwConfig, template)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "name collision")
		assert.Contains(t, err.Error(), "Gateway/other-gateway")

		var permErr *permanentDeploymentError
		assert.True(t, errors.As(err, &permErr))
	})
}

func TestInjectGatewayEnvVars(t *testing.T) {
	deployment := &appsv1.Deployment{
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name: "loadbalancer",
							Env: []corev1.EnvVar{
								{Name: "MERIDIO_GATEWAY_NAME", Value: ""},
								{Name: "OTHER_VAR", Value: "keep-me"},
							},
						},
						{
							Name: "router",
							Env: []corev1.EnvVar{
								{Name: "MERIDIO_GATEWAY_NAME", Value: ""},
							},
						},
					},
				},
			},
		},
	}

	injectGatewayEnvVars(deployment, "my-gateway")

	assert.Equal(t, "my-gateway", deployment.Spec.Template.Spec.Containers[0].Env[0].Value)
	assert.Equal(t, "keep-me", deployment.Spec.Template.Spec.Containers[0].Env[1].Value)
	assert.Equal(t, "my-gateway", deployment.Spec.Template.Spec.Containers[1].Env[0].Value)
}

func TestApplyNetworkAttachments(t *testing.T) {
	t.Run("EmptyAttachments", func(t *testing.T) {
		deployment := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Namespace: "default"},
		}
		applyNetworkAttachments(deployment, "", []meridio2v1alpha1.NetworkAttachment{})
		assert.Empty(t, deployment.Spec.Template.Annotations)
	})

	t.Run("SingleNAD", func(t *testing.T) {
		deployment := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Namespace: "default"},
		}
		attachments := []meridio2v1alpha1.NetworkAttachment{
			{
				Type: attachmentTypeNAD,
				NAD: &meridio2v1alpha1.NAD{
					Namespace: "test-ns",
					Name:      "test-nad",
					Interface: "net1",
				},
			},
		}
		applyNetworkAttachments(deployment, "", attachments)
		annot := deployment.Spec.Template.Annotations[netdefv1.NetworkAttachmentAnnot]
		assert.Contains(t, annot, `"name":"test-nad"`)
		assert.Contains(t, annot, `"namespace":"test-ns"`)
		assert.Contains(t, annot, `"interface":"net1"`)
	})

	t.Run("MultipleNADs", func(t *testing.T) {
		deployment := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Namespace: "default"},
		}
		attachments := []meridio2v1alpha1.NetworkAttachment{
			{
				Type: attachmentTypeNAD,
				NAD: &meridio2v1alpha1.NAD{
					Namespace: "ns1",
					Name:      "nad1",
					Interface: "net1",
				},
			},
			{
				Type: attachmentTypeNAD,
				NAD: &meridio2v1alpha1.NAD{
					Namespace: "ns2",
					Name:      "nad2",
					Interface: "net2",
				},
			},
		}
		applyNetworkAttachments(deployment, "", attachments)
		annot := deployment.Spec.Template.Annotations[netdefv1.NetworkAttachmentAnnot]
		assert.Contains(t, annot, `"name":"nad1"`)
		assert.Contains(t, annot, `"name":"nad2"`)
	})

	t.Run("NADWithoutInterface", func(t *testing.T) {
		deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "default"}}
		attachments := []meridio2v1alpha1.NetworkAttachment{
			{
				Type: attachmentTypeNAD,
				NAD: &meridio2v1alpha1.NAD{
					Namespace: "test-ns",
					Name:      "test-nad",
					Interface: "",
				},
			},
		}
		applyNetworkAttachments(deployment, "", attachments)
		annot := deployment.Spec.Template.Annotations[netdefv1.NetworkAttachmentAnnot]
		assert.Contains(t, annot, `"name":"test-nad"`)
		assert.NotContains(t, annot, `"interface"`)
	})

	t.Run("SkipsDRAAttachments", func(t *testing.T) {
		deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "default"}}
		attachments := []meridio2v1alpha1.NetworkAttachment{
			{Type: "DRA", DRA: &meridio2v1alpha1.DRA{}},
		}
		applyNetworkAttachments(deployment, "", attachments)
		assert.Empty(t, deployment.Spec.Template.Annotations)
	})

	t.Run("PreservesExistingAnnotations", func(t *testing.T) {
		deployment := &appsv1.Deployment{
			Spec: appsv1.DeploymentSpec{
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{"existing": "value"},
					},
				},
			},
		}
		attachments := []meridio2v1alpha1.NetworkAttachment{
			{
				Type: attachmentTypeNAD,
				NAD: &meridio2v1alpha1.NAD{
					Namespace: "test-ns",
					Name:      "test-nad",
					Interface: "net1",
				},
			},
		}
		applyNetworkAttachments(deployment, "", attachments)
		assert.Equal(t, "value", deployment.Spec.Template.Annotations["existing"])
		assert.Contains(t, deployment.Spec.Template.Annotations, netdefv1.NetworkAttachmentAnnot)
	})

	t.Run("UsesTemplateNADs", func(t *testing.T) {
		deployment := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Namespace: "default"},
		}
		templateAnnotation := `[{"name":"template-nad","namespace":"template-ns","interface":"net0"}]`
		attachments := []meridio2v1alpha1.NetworkAttachment{
			{
				Type: attachmentTypeNAD,
				NAD: &meridio2v1alpha1.NAD{
					Namespace: "gwconfig-ns",
					Name:      "gwconfig-nad",
					Interface: "net1",
				},
			},
		}
		applyNetworkAttachments(deployment, templateAnnotation, attachments)
		annot := deployment.Spec.Template.Annotations[netdefv1.NetworkAttachmentAnnot]
		// Both template and GatewayConfiguration NADs should be present
		assert.Contains(t, annot, `"name":"template-nad"`)
		assert.Contains(t, annot, `"name":"gwconfig-nad"`)
	})

	t.Run("GatewayConfigurationOverridesTemplate", func(t *testing.T) {
		deployment := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Namespace: "default"},
		}
		// Template has nad1 with extra fields (ips)
		templateAnnotation := `[{"name":"nad1","namespace":"ns1","interface":"net1","ips":["192.168.1.10"]}]`
		// GatewayConfiguration also specifies nad1 (same namespace/name/interface)
		attachments := []meridio2v1alpha1.NetworkAttachment{
			{
				Type: attachmentTypeNAD,
				NAD: &meridio2v1alpha1.NAD{
					Namespace: "ns1",
					Name:      "nad1",
					Interface: "net1",
				},
			},
		}
		applyNetworkAttachments(deployment, templateAnnotation, attachments)
		annot := deployment.Spec.Template.Annotations[netdefv1.NetworkAttachmentAnnot]
		// GatewayConfiguration NAD should win (no ips field)
		assert.Contains(t, annot, `"name":"nad1"`)
		assert.NotContains(t, annot, `"ips"`)
	})

	t.Run("SkipsDuplicates_ExactMatch", func(t *testing.T) {
		deployment := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Namespace: "default"},
			Spec: appsv1.DeploymentSpec{
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							netdefv1.NetworkAttachmentAnnot: `[{"name":"nad1","namespace":"ns1","interface":"net1"}]`,
						},
					},
				},
			},
		}
		attachments := []meridio2v1alpha1.NetworkAttachment{
			{
				Type: attachmentTypeNAD,
				NAD: &meridio2v1alpha1.NAD{
					Namespace: "ns1",
					Name:      "nad1",
					Interface: "net1",
				},
			},
		}
		applyNetworkAttachments(deployment, "", attachments)
		annot := deployment.Spec.Template.Annotations[netdefv1.NetworkAttachmentAnnot]
		assert.Equal(t, 1, strings.Count(annot, `"name":"nad1"`))
	})

	t.Run("AllowsSameNADWithDifferentInterface", func(t *testing.T) {
		deployment := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Namespace: "default"},
		}
		// Template has nad1 with net1 interface
		templateAnnotation := `[{"name":"nad1","namespace":"ns1","interface":"net1"}]`
		// GatewayConfiguration adds nad1 with net2 interface (different interface = not a duplicate)
		attachments := []meridio2v1alpha1.NetworkAttachment{
			{
				Type: attachmentTypeNAD,
				NAD: &meridio2v1alpha1.NAD{
					Namespace: "ns1",
					Name:      "nad1",
					Interface: "net2",
				},
			},
		}
		applyNetworkAttachments(deployment, templateAnnotation, attachments)
		annot := deployment.Spec.Template.Annotations[netdefv1.NetworkAttachmentAnnot]
		// Both interfaces should be present (different interface = different NAD)
		assert.Equal(t, 2, strings.Count(annot, `"name":"nad1"`))
		assert.Contains(t, annot, `"interface":"net1"`)
		assert.Contains(t, annot, `"interface":"net2"`)
	})
}

func TestUpdateAntiAffinityLabels(t *testing.T) {
	t.Run("UpdatesMatchExpressionValues", func(t *testing.T) {
		deployment := &appsv1.Deployment{
			Spec: appsv1.DeploymentSpec{
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Affinity: &corev1.Affinity{
							PodAntiAffinity: &corev1.PodAntiAffinity{
								RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
									{
										LabelSelector: &metav1.LabelSelector{
											MatchExpressions: []metav1.LabelSelectorRequirement{
												{Key: "app", Operator: metav1.LabelSelectorOpIn, Values: []string{"old-name"}},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		}

		updateAntiAffinityLabels(deployment, "new-name")

		values := deployment.Spec.Template.Spec.Affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution[0].LabelSelector.MatchExpressions[0].Values
		assert.Equal(t, []string{"new-name"}, values)
	})

	t.Run("NoOpWithoutAffinity", func(t *testing.T) {
		deployment := &appsv1.Deployment{}
		updateAntiAffinityLabels(deployment, "new-name") // should not panic
	})

	t.Run("IgnoresNonAppKeys", func(t *testing.T) {
		deployment := &appsv1.Deployment{
			Spec: appsv1.DeploymentSpec{
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Affinity: &corev1.Affinity{
							PodAntiAffinity: &corev1.PodAntiAffinity{
								RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
									{
										LabelSelector: &metav1.LabelSelector{
											MatchExpressions: []metav1.LabelSelectorRequirement{
												{Key: "other", Operator: metav1.LabelSelectorOpIn, Values: []string{"keep"}},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		}

		updateAntiAffinityLabels(deployment, "new-name")

		values := deployment.Spec.Template.Spec.Affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution[0].LabelSelector.MatchExpressions[0].Values
		assert.Equal(t, []string{"keep"}, values)
	})
}

func TestApplyGatewayConfiguration(t *testing.T) {
	t.Run("InitialCreation_SetsReplicas", func(t *testing.T) {
		deployment := &appsv1.Deployment{}
		gwConfig := &meridio2v1alpha1.GatewayConfiguration{
			Spec: meridio2v1alpha1.GatewayConfigurationSpec{
				HorizontalScaling: meridio2v1alpha1.HorizontalScaling{Replicas: 3},
			},
		}

		applyGatewayConfiguration(deployment, gwConfig, "", true)

		assert.Equal(t, int32(3), *deployment.Spec.Replicas)
	})

	t.Run("Update_EnforceReplicasTrue", func(t *testing.T) {
		deployment := &appsv1.Deployment{Spec: appsv1.DeploymentSpec{Replicas: ptr(int32(5))}}
		gwConfig := &meridio2v1alpha1.GatewayConfiguration{
			Spec: meridio2v1alpha1.GatewayConfigurationSpec{
				HorizontalScaling: meridio2v1alpha1.HorizontalScaling{Replicas: 3, EnforceReplicas: true},
			},
		}

		applyGatewayConfiguration(deployment, gwConfig, "", false)

		assert.Equal(t, int32(3), *deployment.Spec.Replicas)
	})

	t.Run("Update_EnforceReplicasFalse_DefersToHPA", func(t *testing.T) {
		deployment := &appsv1.Deployment{Spec: appsv1.DeploymentSpec{Replicas: ptr(int32(5))}}
		gwConfig := &meridio2v1alpha1.GatewayConfiguration{
			Spec: meridio2v1alpha1.GatewayConfigurationSpec{
				HorizontalScaling: meridio2v1alpha1.HorizontalScaling{Replicas: 3, EnforceReplicas: false},
			},
		}

		applyGatewayConfiguration(deployment, gwConfig, "", false)

		assert.Equal(t, int32(5), *deployment.Spec.Replicas) // unchanged
	})
}

func TestApplyVerticalScaling(t *testing.T) {
	t.Run("InitialCreation_SetsResources", func(t *testing.T) {
		deployment := &appsv1.Deployment{
			Spec: appsv1.DeploymentSpec{
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{Name: "loadbalancer"}},
					},
				},
			},
		}
		cpu := resource.MustParse("100m")
		vs := &meridio2v1alpha1.VerticalScaling{
			Containers: []meridio2v1alpha1.ContainerArgs{
				{
					Name:             "loadbalancer",
					EnforceResources: false, // doesn't matter on initial creation
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{corev1.ResourceCPU: cpu},
					},
				},
			},
		}

		applyVerticalScaling(deployment, vs, true)

		assert.Equal(t, "100m", deployment.Spec.Template.Spec.Containers[0].Resources.Requests.Cpu().String())
	})

	t.Run("Update_EnforceTrue", func(t *testing.T) {
		existing := resource.MustParse("50m")
		desired := resource.MustParse("200m")
		deployment := &appsv1.Deployment{
			Spec: appsv1.DeploymentSpec{
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Name: "loadbalancer", Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{corev1.ResourceCPU: existing},
							}},
						},
					},
				},
			},
		}
		vs := &meridio2v1alpha1.VerticalScaling{
			Containers: []meridio2v1alpha1.ContainerArgs{
				{
					Name:             "loadbalancer",
					EnforceResources: true,
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{corev1.ResourceCPU: desired},
					},
				},
			},
		}

		applyVerticalScaling(deployment, vs, false)

		assert.Equal(t, "200m", deployment.Spec.Template.Spec.Containers[0].Resources.Requests.Cpu().String())
	})

	t.Run("Update_EnforceFalse_LeavesUnchanged", func(t *testing.T) {
		// With enforce=false on update, applyVerticalScaling skips the container.
		// Resources are already correct (restored by reconcileDeploymentSpec upstream).
		existing := resource.MustParse("500m")
		deployment := &appsv1.Deployment{
			Spec: appsv1.DeploymentSpec{
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Name: "loadbalancer", Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{corev1.ResourceCPU: existing},
							}},
						},
					},
				},
			},
		}
		vs := &meridio2v1alpha1.VerticalScaling{
			Containers: []meridio2v1alpha1.ContainerArgs{
				{
					Name:             "loadbalancer",
					EnforceResources: false,
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("999m")},
					},
				},
			},
		}

		applyVerticalScaling(deployment, vs, false)

		assert.Equal(t, "500m", deployment.Spec.Template.Spec.Containers[0].Resources.Requests.Cpu().String())
	})

	t.Run("EnforceTrue_ClearsExistingResources", func(t *testing.T) {
		existing := resource.MustParse("500m")
		deployment := &appsv1.Deployment{
			Spec: appsv1.DeploymentSpec{
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Name: "loadbalancer", Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{corev1.ResourceCPU: existing},
								Limits:   corev1.ResourceList{corev1.ResourceCPU: existing},
							}},
						},
					},
				},
			},
		}
		vs := &meridio2v1alpha1.VerticalScaling{
			Containers: []meridio2v1alpha1.ContainerArgs{
				{Name: "loadbalancer", EnforceResources: true},
			},
		}

		applyVerticalScaling(deployment, vs, false)

		assert.Nil(t, deployment.Spec.Template.Spec.Containers[0].Resources.Requests)
		assert.Nil(t, deployment.Spec.Template.Spec.Containers[0].Resources.Limits)
	})

	t.Run("UnmatchedContainer_Ignored", func(t *testing.T) {
		existing := resource.MustParse("50m")
		desired := resource.MustParse("999m")
		deployment := &appsv1.Deployment{
			Spec: appsv1.DeploymentSpec{
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Name: "router", Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{corev1.ResourceCPU: existing},
							}},
						},
					},
				},
			},
		}
		vs := &meridio2v1alpha1.VerticalScaling{
			Containers: []meridio2v1alpha1.ContainerArgs{
				{
					Name:             "nonexistent",
					EnforceResources: true,
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{corev1.ResourceCPU: desired},
					},
				},
			},
		}

		applyVerticalScaling(deployment, vs, true)

		assert.Equal(t, "50m", deployment.Spec.Template.Spec.Containers[0].Resources.Requests.Cpu().String())
	})
}

func TestRestoreContainerResources(t *testing.T) {
	t.Run("RestoresMatchingContainers", func(t *testing.T) {
		containers := []corev1.Container{
			{Name: "loadbalancer", Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m")},
			}},
			{Name: "router", Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m")},
			}},
		}
		existing := []corev1.Container{
			{Name: "loadbalancer", Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m")},
			}},
			{Name: "router", Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("300m")},
			}},
		}

		restoreContainerResources(containers, existing)

		assert.Equal(t, "500m", containers[0].Resources.Requests.Cpu().String())
		assert.Equal(t, "300m", containers[1].Resources.Requests.Cpu().String())
	})

	t.Run("NewContainerKeepsTemplateValues", func(t *testing.T) {
		containers := []corev1.Container{
			{Name: "loadbalancer", Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m")},
			}},
			{Name: "new-sidecar", Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("50m")},
			}},
		}
		existing := []corev1.Container{
			{Name: "loadbalancer", Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m")},
			}},
		}

		restoreContainerResources(containers, existing)

		assert.Equal(t, "500m", containers[0].Resources.Requests.Cpu().String())
		assert.Equal(t, "50m", containers[1].Resources.Requests.Cpu().String()) // no match, keeps template
	})

	t.Run("RestoresResizePolicy", func(t *testing.T) {
		containers := []corev1.Container{
			{Name: "loadbalancer"},
		}
		existing := []corev1.Container{
			{Name: "loadbalancer", ResizePolicy: []corev1.ContainerResizePolicy{
				{ResourceName: corev1.ResourceCPU, RestartPolicy: corev1.NotRequired},
				{ResourceName: corev1.ResourceMemory, RestartPolicy: corev1.RestartContainer},
			}},
		}

		restoreContainerResources(containers, existing)

		assert.Equal(t, existing[0].ResizePolicy, containers[0].ResizePolicy)
	})
}

func TestParseNetworkAnnotation(t *testing.T) {
	t.Run("Empty", func(t *testing.T) {
		assert.Nil(t, parseNetworkAnnotation(""))
	})

	t.Run("JSON", func(t *testing.T) {
		result := parseNetworkAnnotation(`[{"name":"nad1","namespace":"ns1","interface":"net1"}]`)
		assert.Len(t, result, 1)
		assert.Equal(t, "nad1", result[0].Name)
		assert.Equal(t, "ns1", result[0].Namespace)
		assert.Equal(t, "net1", result[0].InterfaceRequest)
	})

	t.Run("Shorthand", func(t *testing.T) {
		result := parseNetworkAnnotation("ns1/nad1@net1")
		assert.Len(t, result, 1)
		assert.Equal(t, "nad1", result[0].Name)
		assert.Equal(t, "ns1", result[0].Namespace)
		assert.Equal(t, "net1", result[0].InterfaceRequest)
	})

	t.Run("ShorthandMultiple", func(t *testing.T) {
		result := parseNetworkAnnotation("ns1/nad1@net1, nad2@net2")
		assert.Len(t, result, 2)
		assert.Equal(t, "ns1", result[0].Namespace)
		assert.Equal(t, "", result[1].Namespace)
		assert.Equal(t, "nad2", result[1].Name)
	})

	t.Run("ShorthandNoInterface", func(t *testing.T) {
		result := parseNetworkAnnotation("ns1/nad1")
		assert.Len(t, result, 1)
		assert.Equal(t, "nad1", result[0].Name)
		assert.Equal(t, "", result[0].InterfaceRequest)
	})

	t.Run("MalformedSkipped", func(t *testing.T) {
		for _, input := range []string{
			"@@@",          // multiple @, empty name
			"///",          // multiple /
			"@iface",       // empty name before @
			"a@",           // empty interface after @
			"ns/@iface",    // empty name between / and @
			"/name@iface",  // empty namespace
			"ns/name@",     // empty interface
			"name@if1@if2", // multiple @
			"a/b/c@iface",  // multiple /
		} {
			t.Run(input, func(t *testing.T) {
				assert.Empty(t, parseNetworkAnnotation(input))
			})
		}
	})

	t.Run("MalformedMixedWithValid", func(t *testing.T) {
		result := parseNetworkAnnotation("ns1/nad1@net1, @bad, nad2@net2")
		assert.Len(t, result, 2)
		assert.Equal(t, "nad1", result[0].Name)
		assert.Equal(t, "nad2", result[1].Name)
	})
}

func TestNetworkAttachmentsEqual(t *testing.T) {
	t.Run("BothEmpty", func(t *testing.T) {
		assert.True(t, networkAttachmentsEqual(nil, nil, "default"))
	})

	t.Run("DifferentLengths", func(t *testing.T) {
		a := []*netdefv1.NetworkSelectionElement{{Name: "nad1"}}
		assert.False(t, networkAttachmentsEqual(a, nil, "default"))
	})

	t.Run("SameElements", func(t *testing.T) {
		a := []*netdefv1.NetworkSelectionElement{
			{Name: "nad1", Namespace: "ns1", InterfaceRequest: "net1"},
			{Name: "nad2", Namespace: "ns2", InterfaceRequest: "net2"},
			{Name: "nad3", Namespace: "ns3", InterfaceRequest: "net3"},
		}
		b := []*netdefv1.NetworkSelectionElement{
			{Name: "nad1", Namespace: "ns1", InterfaceRequest: "net1"},
			{Name: "nad2", Namespace: "ns2", InterfaceRequest: "net2"},
			{Name: "nad3", Namespace: "ns3", InterfaceRequest: "net3"},
		}
		assert.True(t, networkAttachmentsEqual(a, b, "default"))
	})

	t.Run("DifferentOrder", func(t *testing.T) {
		a := []*netdefv1.NetworkSelectionElement{
			{Name: "nad1", Namespace: "ns1", InterfaceRequest: "net1"},
			{Name: "nad2", Namespace: "ns2", InterfaceRequest: "net2"},
		}
		b := []*netdefv1.NetworkSelectionElement{
			{Name: "nad2", Namespace: "ns2", InterfaceRequest: "net2"},
			{Name: "nad1", Namespace: "ns1", InterfaceRequest: "net1"},
		}
		assert.True(t, networkAttachmentsEqual(a, b, "default"))
	})

	t.Run("DefaultNamespaceResolution", func(t *testing.T) {
		a := []*netdefv1.NetworkSelectionElement{{Name: "nad1", Namespace: "", InterfaceRequest: "net1"}}
		b := []*netdefv1.NetworkSelectionElement{{Name: "nad1", Namespace: "default", InterfaceRequest: "net1"}}
		assert.True(t, networkAttachmentsEqual(a, b, "default"))
	})

	t.Run("DifferentElements", func(t *testing.T) {
		a := []*netdefv1.NetworkSelectionElement{{Name: "nad1", Namespace: "ns1", InterfaceRequest: "net1"}}
		b := []*netdefv1.NetworkSelectionElement{{Name: "nad2", Namespace: "ns1", InterfaceRequest: "net1"}}
		assert.False(t, networkAttachmentsEqual(a, b, "default"))
	})
}
