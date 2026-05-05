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
	"fmt"
	"path/filepath"

	"github.com/fsnotify/fsnotify"
	"github.com/nordix/meridio-2/internal/common/readiness"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/event"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// watchLBReadinessDir watches the LB readiness directory and sends a GenericEvent
// only when the state transitions (empty→non-empty or non-empty→empty).
// Returns an error if the watcher cannot be established (causes manager shutdown).
func (r *RouterReconciler) watchLBReadinessDir(ctx context.Context, ch chan<- event.GenericEvent) error {
	log := ctrl.Log.WithName("lbwatcher")

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("failed to create fsnotify watcher: %w", err)
	}
	defer watcher.Close() //nolint:errcheck

	if err := watcher.Add(r.LBReadinessPath); err != nil {
		return fmt.Errorf("failed to watch LB readiness directory %q: %w", r.LBReadinessPath, err)
	}

	previousReadiness := dirHasLBReadinessFiles(r.LBReadinessPath)
	log.Info("watching LB readiness directory", "path", r.LBReadinessPath, "ready", previousReadiness)

	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-watcher.Events:
			if !ok {
				return nil
			}
			if !ev.Has(fsnotify.Create) && !ev.Has(fsnotify.Remove) {
				continue
			}
			isReady := dirHasLBReadinessFiles(r.LBReadinessPath)
			if isReady != previousReadiness {
				previousReadiness = isReady
				log.Info("LB target availability changed", "status", map[bool]string{true: "ready", false: "unavailable"}[isReady])
				select {
				case ch <- event.GenericEvent{Object: &gatewayapiv1.Gateway{
					ObjectMeta: metav1.ObjectMeta{
						Name:      r.GatewayName,
						Namespace: r.GatewayNamespace,
					},
				}}:
				default:
				}
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
			log.Error(err, "fsnotify watcher error")
		}
	}
}

// dirHasLBReadinessFiles returns true if the directory contains any lb-ready-* files.
func dirHasLBReadinessFiles(dir string) bool {
	matches, _ := filepath.Glob(filepath.Join(dir, readiness.LBReadyFilePrefix+"*"))
	return len(matches) > 0
}
