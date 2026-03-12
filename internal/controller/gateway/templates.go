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
	"fmt"
	"os"
	"path/filepath"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/util/yaml"
)

// templateError represents a template load failure (permanent but retriable)
type templateError struct {
	message string
}

func (e *templateError) Error() string {
	return e.message
}

// loadLBDeploymentTemplate loads the stateless load balancer Deployment template from ConfigMap
// TODO: Cache parsed template and use fsnotify to invalidate on ConfigMap update.
// Current approach re-reads from disk on every reconcile, which is fine for moderate
// reconcile rates (OS page cache), but could be optimized for high-throughput scenarios.
func (r *GatewayReconciler) loadLBDeploymentTemplate() (*appsv1.Deployment, error) {
	templateFile := filepath.Join(r.TemplatePath, LBDeploymentTemplateFile)
	file, err := os.Open(templateFile)
	if err != nil {
		return nil, fmt.Errorf("failed to open template: %w", err)
	}
	defer func() { _ = file.Close() }()

	deployment := &appsv1.Deployment{}
	decoder := yaml.NewYAMLOrJSONDecoder(file, yamlDecoderBufferSize)
	if err := decoder.Decode(deployment); err != nil {
		return nil, fmt.Errorf("failed to decode template: %w", err)
	}

	return deployment, nil
}

// loadTemplate loads the LB deployment template
// Returns templateError for load failures (requeue with backoff)
func (r *GatewayReconciler) loadTemplate() (*appsv1.Deployment, error) {
	template, err := r.loadLBDeploymentTemplate()
	if err != nil {
		return nil, &templateError{
			message: fmt.Sprintf("failed to load LB deployment template: %v", err),
		}
	}
	return template, nil
}
