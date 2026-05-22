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

// Package readiness defines the shared contract between the LB and router
// controllers for signaling target availability via the filesystem.
package readiness

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/fsnotify/fsnotify"
)

// LBReadyFilePrefix is the filename prefix used by the LB controller to signal
// that a DistributionGroup has ready targets. The router controller watches for
// files matching this prefix to gate VIP advertisement.
const LBReadyFilePrefix = "lb-ready-"

// DefaultReadinessDir is the default directory for LB readiness files.
const DefaultReadinessDir = "/var/run/meridio"

// Manager handles creation and removal of readiness files.
// If directory is empty, all operations are no-ops (readiness signaling disabled).
type Manager struct {
	directory string
}

// NewManager creates a Manager for the given directory.
// An empty directory disables readiness signaling.
func NewManager(directory string) *Manager {
	return &Manager{directory: directory}
}

// Enabled returns true if readiness signaling is active.
func (m *Manager) Enabled() bool {
	return m.directory != ""
}

// Path returns the readiness directory path.
func (m *Manager) Path() string {
	return m.directory
}

// Cleanup removes all readiness files. Called on startup for a clean state.
func (m *Manager) Cleanup() error {
	if !m.Enabled() {
		return nil
	}

	if err := os.MkdirAll(m.directory, 0755); err != nil {
		return fmt.Errorf("failed to create readiness directory: %w", err)
	}

	matches, err := filepath.Glob(filepath.Join(m.directory, LBReadyFilePrefix+"*"))
	if err != nil {
		return fmt.Errorf("failed to glob readiness files: %w", err)
	}

	for _, file := range matches {
		if err := os.Remove(file); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("failed to remove readiness file %s: %w", file, err)
		}
	}

	return nil
}

// Set creates a readiness file for the given name.
func (m *Manager) Set(name string) error {
	if !m.Enabled() {
		return nil
	}

	if err := os.MkdirAll(m.directory, 0755); err != nil {
		return fmt.Errorf("failed to create readiness directory: %w", err)
	}

	file, err := os.Create(filepath.Join(m.directory, LBReadyFilePrefix+name))
	if err != nil {
		return fmt.Errorf("failed to create readiness file: %w", err)
	}
	_ = file.Close()

	return nil
}

// Remove removes the readiness file for the given name.
func (m *Manager) Remove(name string) error {
	if !m.Enabled() {
		return nil
	}

	if err := os.Remove(filepath.Join(m.directory, LBReadyFilePrefix+name)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove readiness file: %w", err)
	}
	return nil
}

// IsReady returns true if the directory contains any readiness files.
func (m *Manager) IsReady() bool {
	if !m.Enabled() {
		return false
	}
	matches, _ := filepath.Glob(filepath.Join(m.directory, LBReadyFilePrefix+"*"))
	return len(matches) > 0
}

// Watch watches the readiness directory for state transitions and sends
// a notification on the returned channel when readiness changes.
// Blocks until ctx is cancelled. Returns an error if the watcher cannot be established.
func (m *Manager) Watch(ctx context.Context) (<-chan struct{}, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("failed to create fsnotify watcher: %w", err)
	}

	if err := watcher.Add(m.directory); err != nil {
		watcher.Close() //nolint:errcheck
		return nil, fmt.Errorf("failed to watch readiness directory %q: %w", m.directory, err)
	}

	ch := make(chan struct{}, 1)

	go func() {
		defer watcher.Close() //nolint:errcheck
		defer close(ch)

		previousReadiness := m.IsReady()

		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-watcher.Events:
				if !ok {
					return
				}
				if !ev.Has(fsnotify.Create) && !ev.Has(fsnotify.Remove) {
					continue
				}
				isReady := m.IsReady()
				if isReady != previousReadiness {
					previousReadiness = isReady
					select {
					case ch <- struct{}{}:
					default:
					}
				}
			case _, ok := <-watcher.Errors:
				if !ok {
					return
				}
			}
		}
	}()

	return ch, nil
}
