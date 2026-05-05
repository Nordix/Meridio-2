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

package router

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nordix/meridio-2/internal/common/readiness"
	"github.com/stretchr/testify/assert"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

func TestDirHasLBReadinessFiles_Empty(t *testing.T) {
	dir := t.TempDir()
	assert.False(t, dirHasLBReadinessFiles(dir))
}

func TestDirHasLBReadinessFiles_WithFile(t *testing.T) {
	dir := t.TempDir()
	f, _ := os.Create(filepath.Join(dir, readiness.LBReadyFilePrefix+"test-dg"))
	_ = f.Close()
	assert.True(t, dirHasLBReadinessFiles(dir))
}

func TestDirHasLBReadinessFiles_UnrelatedFile(t *testing.T) {
	dir := t.TempDir()
	f, _ := os.Create(filepath.Join(dir, "other-file"))
	_ = f.Close()
	assert.False(t, dirHasLBReadinessFiles(dir))
}

func TestWatchLBReadinessDir_TriggersOnTransition(t *testing.T) {
	dir := t.TempDir()
	ch := make(chan event.GenericEvent, 1)

	r := &RouterReconciler{
		GatewayName:      "test-gw",
		GatewayNamespace: "default",
		LBReadinessPath:  dir,
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- r.watchLBReadinessDir(ctx, ch)
	}()

	// Give watcher time to start
	time.Sleep(100 * time.Millisecond)

	// Create file → should trigger (empty→non-empty)
	f, _ := os.Create(filepath.Join(dir, readiness.LBReadyFilePrefix+"dg1"))
	_ = f.Close()

	select {
	case ev := <-ch:
		assert.Equal(t, "test-gw", ev.Object.GetName())
		assert.Equal(t, "default", ev.Object.GetNamespace())
	case <-time.After(2 * time.Second):
		t.Fatal("expected event on file creation")
	}

	// Remove file → should trigger (non-empty→empty)
	_ = os.Remove(filepath.Join(dir, readiness.LBReadyFilePrefix+"dg1"))

	select {
	case <-ch:
		// good
	case <-time.After(2 * time.Second):
		t.Fatal("expected event on file removal")
	}

	cancel()
	assert.NoError(t, <-errCh)
}

func TestWatchLBReadinessDir_NoTriggerWithinSameState(t *testing.T) {
	dir := t.TempDir()
	ch := make(chan event.GenericEvent, 1)

	// Pre-create a file so dir starts non-empty
	f, _ := os.Create(filepath.Join(dir, readiness.LBReadyFilePrefix+"dg1"))
	_ = f.Close()

	r := &RouterReconciler{
		GatewayName:      "test-gw",
		GatewayNamespace: "default",
		LBReadinessPath:  dir,
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	go func() {
		_ = r.watchLBReadinessDir(ctx, ch)
	}()

	time.Sleep(100 * time.Millisecond)

	// Add another file → still non-empty, no transition
	f2, _ := os.Create(filepath.Join(dir, readiness.LBReadyFilePrefix+"dg2"))
	_ = f2.Close()

	select {
	case <-ch:
		t.Fatal("should not trigger when state doesn't change")
	case <-time.After(300 * time.Millisecond):
		// good — no event
	}
}

func TestWatchLBReadinessDir_ErrorOnMissingDir(t *testing.T) {
	ch := make(chan event.GenericEvent, 1)

	r := &RouterReconciler{
		LBReadinessPath: "/nonexistent/path",
	}

	err := r.watchLBReadinessDir(context.Background(), ch)
	assert.Error(t, err)
}
