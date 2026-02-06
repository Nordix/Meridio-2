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

package v1alpha1

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDistributionGroup_DeepCopy(t *testing.T) {
	// 1. Setup original object with nested pointers
	group := "gateway.networking.k8s.io"
	original := &DistributionGroup{
		Spec: DistributionGroupSpec{
			Type: DistributionGroupTypeMaglev,
			Maglev: &MaglevConfig{
				MaxEndpoints: 64,
			},
			ParentRefs: []ParentReference{
				{
					Name:  "gw-1",
					Group: &group,
				},
			},
		},
	}

	// 2. Perform DeepCopy
	copy := original.DeepCopy()

	// 3. Assertions
	assert.NotNil(t, copy)
	assert.Equal(t, original.Spec.Maglev.MaxEndpoints, copy.Spec.Maglev.MaxEndpoints)

	// Verify pointers are different (deep copy vs shallow copy)
	assert.False(t, original.Spec.Maglev == copy.Spec.Maglev, "Pointers for MaglevConfig should be different")
	assert.False(t, original.Spec.ParentRefs[0].Group == copy.Spec.ParentRefs[0].Group, "Pointers in ParentRefs should be different")

	// Modify copy and ensure original remains unchanged
	copy.Spec.Maglev.MaxEndpoints = 128
	assert.NotEqual(t, original.Spec.Maglev.MaxEndpoints, copy.Spec.Maglev.MaxEndpoints)
}
