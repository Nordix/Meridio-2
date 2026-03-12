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
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTemplateError(t *testing.T) {
	err := &templateError{message: "broken template"}
	assert.Equal(t, "broken template", err.Error())

	var tmplErr *templateError
	assert.True(t, errors.As(err, &tmplErr))
}

func TestLoadLBDeploymentTemplate(t *testing.T) {
	t.Run("FileNotFound", func(t *testing.T) {
		reconciler, _ := setupReconciler()
		reconciler.TemplatePath = "/nonexistent"

		_, err := reconciler.loadLBDeploymentTemplate()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to open template")
	})

	t.Run("MalformedYAML", func(t *testing.T) {
		tmpDir := t.TempDir()
		writeTemplate(t, tmpDir, "not: [valid: yaml: {{{")

		reconciler, _ := setupReconciler()
		reconciler.TemplatePath = tmpDir

		_, err := reconciler.loadLBDeploymentTemplate()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to decode template")
	})

	t.Run("Valid", func(t *testing.T) {
		tmpDir := t.TempDir()
		writeTemplate(t, tmpDir, minimalTemplateYAML)

		reconciler, _ := setupReconciler()
		reconciler.TemplatePath = tmpDir

		deployment, err := reconciler.loadLBDeploymentTemplate()
		assert.NoError(t, err)
		assert.Equal(t, "placeholder", deployment.Name)
	})
}

func TestLoadTemplate(t *testing.T) {
	t.Run("ReturnsTemplateError", func(t *testing.T) {
		reconciler, _ := setupReconciler()
		reconciler.TemplatePath = "/nonexistent"

		_, err := reconciler.loadTemplate()

		var tmplErr *templateError
		assert.True(t, errors.As(err, &tmplErr))
		assert.Contains(t, err.Error(), "failed to load LB deployment template")
	})

	t.Run("Valid", func(t *testing.T) {
		tmpDir := t.TempDir()
		writeTemplate(t, tmpDir, minimalTemplateYAML)

		reconciler, _ := setupReconciler()
		reconciler.TemplatePath = tmpDir

		deployment, err := reconciler.loadTemplate()
		assert.NoError(t, err)
		assert.NotNil(t, deployment)
	})
}
