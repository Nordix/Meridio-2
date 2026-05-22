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

package readiness

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestManager_SetAndRemove(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)

	assert.NoError(t, m.Set("dg1"))
	assert.FileExists(t, filepath.Join(dir, LBReadyFilePrefix+"dg1"))

	assert.NoError(t, m.Remove("dg1"))
	_, err := os.Stat(filepath.Join(dir, LBReadyFilePrefix+"dg1"))
	assert.True(t, os.IsNotExist(err))
}

func TestManager_RemoveNonExistent(t *testing.T) {
	m := NewManager(t.TempDir())
	assert.NoError(t, m.Remove("non-existent"))
}

func TestManager_Cleanup(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)

	_ = m.Set("dg1")
	_ = m.Set("dg2")
	assert.NoError(t, m.Cleanup())

	matches, _ := filepath.Glob(filepath.Join(dir, LBReadyFilePrefix+"*"))
	assert.Empty(t, matches)
}

func TestManager_IsReady(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)

	assert.False(t, m.IsReady())

	_ = m.Set("dg1")
	assert.True(t, m.IsReady())

	_ = m.Remove("dg1")
	assert.False(t, m.IsReady())
}

func TestManager_IsReady_UnrelatedFile(t *testing.T) {
	dir := t.TempDir()
	f, _ := os.Create(filepath.Join(dir, "other-file"))
	_ = f.Close()

	m := NewManager(dir)
	assert.False(t, m.IsReady())
}

func TestManager_Disabled(t *testing.T) {
	m := NewManager("")

	assert.False(t, m.Enabled())
	assert.False(t, m.IsReady())
	assert.NoError(t, m.Set("dg1"))
	assert.NoError(t, m.Remove("dg1"))
	assert.NoError(t, m.Cleanup())
}

func TestManager_Watch_TriggersOnTransition(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	ch, err := m.Watch(ctx)
	assert.NoError(t, err)

	time.Sleep(100 * time.Millisecond)

	// Create file → should trigger
	_ = m.Set("dg1")

	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("expected notification on file creation")
	}

	// Remove file → should trigger
	_ = m.Remove("dg1")

	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("expected notification on file removal")
	}
}

func TestManager_Watch_NoTriggerWithinSameState(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)
	_ = m.Set("dg1")

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	ch, err := m.Watch(ctx)
	assert.NoError(t, err)

	time.Sleep(100 * time.Millisecond)

	// Add another file → still non-empty, no transition
	_ = m.Set("dg2")

	select {
	case <-ch:
		t.Fatal("should not trigger when state doesn't change")
	case <-time.After(300 * time.Millisecond):
	}
}

func TestManager_Watch_ErrorOnMissingDir(t *testing.T) {
	m := NewManager("/nonexistent/path")

	_, err := m.Watch(context.Background())
	assert.Error(t, err)
}

func TestManager_Watch_FullStateTransitions(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	ch, err := m.Watch(ctx)
	assert.NoError(t, err)

	time.Sleep(100 * time.Millisecond)

	// empty → one file: should trigger (not ready → ready)
	_ = m.Set("dg1")
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("expected notification on first file creation")
	}

	// add second file: still ready, no transition
	_ = m.Set("dg2")
	select {
	case <-ch:
		t.Fatal("should not trigger when adding second file")
	case <-time.After(300 * time.Millisecond):
	}

	// remove one file: still ready (dg1 remains), no transition
	_ = m.Remove("dg2")
	select {
	case <-ch:
		t.Fatal("should not trigger when one file still remains")
	case <-time.After(300 * time.Millisecond):
	}

	// remove last file: should trigger (ready → not ready)
	_ = m.Remove("dg1")
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("expected notification when all files removed")
	}
}
