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
	"errors"
	"fmt"
	"net"
	"time"

	meridio2v1alpha1 "github.com/nordix/meridio-2/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/nordix/meridio-2/internal/bird"
)

// ConnectivityGateManager manages Pod readiness gate conditions
type ConnectivityGateManager struct {
	client       client.Client
	podName      string
	podNamespace string
	podUID       types.UID
	holdTime     time.Duration
	// Current gate states (last written to API)
	ipv4Gate *bool // nil = not declared, true/false = current value
	ipv6Gate *bool
	// Dirty flags: force unconditional write on next tick after any failure
	ipv4Dirty bool
	ipv6Dirty bool
	// Damping: timestamp when connectivity first came up (zero = not up)
	ipv4UpSince time.Time
	ipv6UpSince time.Time
}

// NewConnectivityGateManager creates a new instance of ConnectivityGateManager
func NewConnectivityGateManager(c client.Client, podName, podNamespace string, podUID types.UID, holdTime time.Duration) *ConnectivityGateManager {
	cgm := &ConnectivityGateManager{
		client:       c,
		podName:      podName,
		podNamespace: podNamespace,
		podUID:       podUID,
		holdTime:     holdTime,
	}
	return cgm
}

// DiscoverGates checks the Pod's readiness gates and initializes internal state
func (cgm *ConnectivityGateManager) DiscoverGates(ctx context.Context) error {
	pod := &corev1.Pod{}
	if err := cgm.client.Get(ctx, types.NamespacedName{Name: cgm.podName, Namespace: cgm.podNamespace}, pod); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("pod %s/%s not found: %w", cgm.podNamespace, cgm.podName, err)
		}
		return fmt.Errorf("error fetching pod %s/%s: %w", cgm.podNamespace, cgm.podName, err)
	}

	for _, gate := range pod.Spec.ReadinessGates {
		switch gate.ConditionType {
		case ReadinessGateIPv4:
			cgm.ipv4Gate = new(bool)
			for _, c := range pod.Status.Conditions {
				if string(c.Type) == ReadinessGateIPv4 {
					*cgm.ipv4Gate = c.Status == corev1.ConditionTrue
				}
			}
		case ReadinessGateIPv6:
			cgm.ipv6Gate = new(bool)
			for _, c := range pod.Status.Conditions {
				if string(c.Type) == ReadinessGateIPv6 {
					*cgm.ipv6Gate = c.Status == corev1.ConditionTrue
				}
			}
		}
	}
	return nil
}

// HasIPv4Gate returns true if the Pod has the IPv4 connectivity gate declared
func (cgm *ConnectivityGateManager) HasIPv4Gate() bool {
	return cgm.ipv4Gate != nil
}

// HasIPv6Gate returns true if the Pod has the IPv6 connectivity gate declared
func (cgm *ConnectivityGateManager) HasIPv6Gate() bool {
	return cgm.ipv6Gate != nil
}

// classifyConnectivityByFamily determines per-IP-family connectivity from protocol statuses.
// Returns true for each family if at least one protocol of that family is established.
// Protocols not in familyMap are ignored.
func ClassifyConnectivityByFamily(protocols []bird.ProtocolStatus, familyMap map[string]string) (ipv4Connected, ipv6Connected bool) {
	for _, p := range protocols {
		if !p.IsEstablished() {
			continue
		}
		switch familyMap[p.Name] {
		case "IPv4":
			ipv4Connected = true
		case "IPv6":
			ipv6Connected = true
		}
	}
	return
}

// BuildFamilyMap creates a mapping from protocol names to IP families based on the provided GatewayRouters.
func BuildFamilyMap(gatewayRouters []*meridio2v1alpha1.GatewayRouter) map[string]string {
	familyMap := make(map[string]string)
	for _, gr := range gatewayRouters {
		family := "IPv4"
		if ip := net.ParseIP(gr.Spec.Address); ip != nil && ip.To4() == nil {
			family = "IPv6"
		}
		familyMap[fmt.Sprintf("NBR-%s", gr.Name)] = family
	}
	return familyMap
}

// patchGateCondition patches a single Pod status condition using a strategic merge patch.
// No Get required — always writes unconditionally. The caller decides whether to call it.
func (cgm *ConnectivityGateManager) patchGateCondition(ctx context.Context, conditionType string, status bool) error {
	condStatus := corev1.ConditionFalse
	if status {
		condStatus = corev1.ConditionTrue
	}

	now := metav1.Now().Format(time.RFC3339)
	patchJSON := fmt.Sprintf(
		`{"status":{"conditions":[{"type":%q,"status":%q,"lastTransitionTime":%q}]}}`,
		conditionType, string(condStatus), now,
	)

	pod := &corev1.Pod{}
	pod.Name = cgm.podName
	pod.Namespace = cgm.podNamespace

	return cgm.client.Status().Patch(ctx, pod, client.RawPatch(
		types.StrategicMergePatchType, []byte(patchJSON),
	))
}

// SetAllGatesFalse(ctx) error — called on startup (defense-in-depth)
func (cgm *ConnectivityGateManager) SetAllGatesFalse(ctx context.Context) error {
	var errFinal error
	if cgm.ipv4Gate != nil {
		if err := cgm.patchGateCondition(ctx, ReadinessGateIPv4, false); err != nil {
			errFinal = errors.Join(errFinal, err)
		}
	}
	if cgm.ipv6Gate != nil {
		if err := cgm.patchGateCondition(ctx, ReadinessGateIPv6, false); err != nil {
			errFinal = errors.Join(errFinal, err)
		}
	}
	return errFinal
}

// OnStatusUpdate handles per-IP-family connectivity changes with damping.
// Down transitions are immediate. Up transitions require holdTime to elapse
// with continuous connectivity before the gate is set to True.
func (cgm *ConnectivityGateManager) OnStatusUpdate(ctx context.Context, ipv4Connected, ipv6Connected bool) error {
	var errFinal error
	if err := cgm.handleGate(ctx, cgm.ipv4Gate, ipv4Connected, &cgm.ipv4UpSince, ReadinessGateIPv4); err != nil {
		errFinal = errors.Join(errFinal, err)
	}
	if err := cgm.handleGate(ctx, cgm.ipv6Gate, ipv6Connected, &cgm.ipv6UpSince, ReadinessGateIPv6); err != nil {
		errFinal = errors.Join(errFinal, err)
	}
	return errFinal
}

func (cgm *ConnectivityGateManager) handleGate(ctx context.Context, gate *bool, connected bool, upSince *time.Time, conditionType string) error {
	if gate == nil {
		return nil // Gate not declared
	}

	dirty := cgm.getDirty(conditionType)

	if !connected {
		// Down: immediate
		*upSince = time.Time{} // Reset hold timer
		if *gate || *dirty {
			if err := cgm.patchGateCondition(ctx, conditionType, false); err != nil {
				*dirty = true
				return err
			}
			*gate = false
			*dirty = false
		}
		return nil
	}

	// Connected
	if *gate && !*dirty {
		return nil // Already True, no pending write
	}

	// Start hold timer if not already started
	now := time.Now()
	if upSince.IsZero() {
		*upSince = now
		return nil // Wait for hold time
	}

	// Check if hold time has elapsed
	if now.Sub(*upSince) >= cgm.holdTime {
		if err := cgm.patchGateCondition(ctx, conditionType, true); err != nil {
			*dirty = true
			return err
		}
		*gate = true
		*dirty = false
	}

	return nil
}

func (cgm *ConnectivityGateManager) getDirty(conditionType string) *bool {
	if conditionType == ReadinessGateIPv4 {
		return &cgm.ipv4Dirty
	}
	return &cgm.ipv6Dirty
}
