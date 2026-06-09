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
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Helper to create a test mapping file
func createTestMappingFile(t *testing.T, content string) string {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "mapping.json")
	require.NoError(t, os.WriteFile(testFile, []byte(content), 0600))
	return testFile
}

func TestLoadMapping_FileNotFound(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "nonexistent.json")

	a := newTableIDAllocator(100, 200)
	err := loadMapping(a, testFile)

	assert.NoError(t, err, "missing file should not be an error")
	assert.Empty(t, a.snapshot())
}

func TestLoadMapping_ValidFile(t *testing.T) {
	testFile := createTestMappingFile(t, `{"gw-a":100,"gw-b":101}`)

	a := newTableIDAllocator(100, 200)
	err := loadMapping(a, testFile)

	assert.NoError(t, err)
	snapshot := a.snapshot()
	assert.Len(t, snapshot, 2)
	assert.Equal(t, 100, snapshot["gw-a"])
	assert.Equal(t, 101, snapshot["gw-b"])
}

func TestLoadMapping_CorruptedJSON(t *testing.T) {
	testFile := createTestMappingFile(t, `{"gw-a":100,invalid json`)

	a := newTableIDAllocator(100, 200)
	err := loadMapping(a, testFile)

	assert.Error(t, err)
}

func TestLoadMapping_InvalidTableID(t *testing.T) {
	testFile := createTestMappingFile(t, `{"gw-a":999}`)

	a := newTableIDAllocator(100, 200)
	err := loadMapping(a, testFile)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "out of range")
}

func TestLoadMapping_DuplicateTableID(t *testing.T) {
	testFile := createTestMappingFile(t, `{"gw-a":100,"gw-b":100}`)

	a := newTableIDAllocator(100, 200)
	err := loadMapping(a, testFile)

	assert.Error(t, err)
}

func TestLoadMapping_EmptyPath(t *testing.T) {
	a := newTableIDAllocator(100, 200)
	_, _ = a.allocate("gw-a")

	err := loadMapping(a, "")

	assert.NoError(t, err, "empty path should be a no-op")
	// Allocator should be unchanged
	assert.Len(t, a.snapshot(), 1)
}

func TestSaveMapping_CreatesFile(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "mapping.json")

	a := newTableIDAllocator(100, 200)
	_, _ = a.allocate("gw-a")
	_, _ = a.allocate("gw-b")

	err := saveMapping(a, testFile)
	assert.NoError(t, err)

	// Verify file exists and is readable
	data, err := os.ReadFile(testFile)
	require.NoError(t, err)
	assert.Contains(t, string(data), "gw-a")
	assert.Contains(t, string(data), "gw-b")
}

func TestSaveMapping_AtomicWrite(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "mapping.json")

	a := newTableIDAllocator(100, 200)
	_, _ = a.allocate("gw-a")

	err := saveMapping(a, testFile)
	require.NoError(t, err)

	// Verify temp file was cleaned up
	tmpFile := testFile + ".tmp"
	_, err = os.Stat(tmpFile)
	assert.True(t, os.IsNotExist(err), "temp file should be removed after rename")
}

func TestSaveMapping_EmptyAllocator(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "mapping.json")

	a := newTableIDAllocator(100, 200)

	err := saveMapping(a, testFile)
	assert.NoError(t, err)

	// Verify file contains empty JSON object
	data, err := os.ReadFile(testFile)
	require.NoError(t, err)
	assert.Equal(t, "{}", string(data))
}

func TestSaveMapping_EmptyPath(t *testing.T) {
	a := newTableIDAllocator(100, 200)
	_, _ = a.allocate("gw-a")

	err := saveMapping(a, "")

	assert.NoError(t, err, "empty path should be a no-op")
	// Verify no file was created (can't really test this without knowing where it would go)
}

func TestRoundTrip_SaveAndLoad(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "mapping.json")

	// Save
	a1 := newTableIDAllocator(100, 200)
	_, _ = a1.allocate("gw-a")
	_, _ = a1.allocate("gw-b")
	_, _ = a1.allocate("gw-c")
	a1.release("gw-b") // Release one

	err := saveMapping(a1, testFile)
	require.NoError(t, err)

	// Load
	a2 := newTableIDAllocator(100, 200)
	err = loadMapping(a2, testFile)
	require.NoError(t, err)

	// Verify
	snapshot1 := a1.snapshot()
	snapshot2 := a2.snapshot()
	assert.Equal(t, snapshot1, snapshot2)
	assert.Len(t, snapshot2, 2) // gw-a and gw-c (gw-b was released)
}
