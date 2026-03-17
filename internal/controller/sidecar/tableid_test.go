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

package sidecar

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTableIDAllocator_Allocate(t *testing.T) {
	t.Run("SequentialAllocation", func(t *testing.T) {
		a := newTableIDAllocator(100, 200)

		id1, err := a.allocate("gw-a")
		assert.NoError(t, err)
		assert.Equal(t, 100, id1)

		id2, err := a.allocate("gw-b")
		assert.NoError(t, err)
		assert.Equal(t, 101, id2)
	})

	t.Run("StableMapping", func(t *testing.T) {
		a := newTableIDAllocator(100, 200)

		id1, _ := a.allocate("gw-a")
		id2, _ := a.allocate("gw-a")
		assert.Equal(t, id1, id2)
	})

	t.Run("RangeExhausted", func(t *testing.T) {
		a := newTableIDAllocator(100, 101)

		_, err := a.allocate("gw-a")
		assert.NoError(t, err)
		_, err = a.allocate("gw-b")
		assert.NoError(t, err)
		_, err = a.allocate("gw-c")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "exhausted")
	})
}

func TestTableIDAllocator_Release(t *testing.T) {
	t.Run("FreedIDReused", func(t *testing.T) {
		a := newTableIDAllocator(100, 200)

		_, _ = a.allocate("gw-a")
		id2, _ := a.allocate("gw-b")
		assert.Equal(t, 101, id2)

		a.release("gw-a")

		id3, _ := a.allocate("gw-c")
		assert.Equal(t, 100, id3) // reused from gw-a
	})

	t.Run("ReleaseUnknown", func(t *testing.T) {
		a := newTableIDAllocator(100, 200)
		a.release("nonexistent") // should not panic
	})
}

func TestTableIDAllocator_Lookup(t *testing.T) {
	a := newTableIDAllocator(100, 200)

	_, exists := a.lookup("gw-a")
	assert.False(t, exists)

	_, _ = a.allocate("gw-a")
	id, exists := a.lookup("gw-a")
	assert.True(t, exists)
	assert.Equal(t, 100, id)
}

func TestTableIDAllocator_ActiveGateways(t *testing.T) {
	a := newTableIDAllocator(100, 200)

	_, _ = a.allocate("gw-a")
	_, _ = a.allocate("gw-b")
	a.release("gw-a")

	active := a.activeGateways()
	assert.Len(t, active, 1)
	assert.Contains(t, active, "gw-b")
}
