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
	"context"
	"errors"
	"fmt"
	"net"

	meridio2v1alpha1 "github.com/nordix/meridio-2/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// Controller reconciles EndpointNetworkConfiguration to configure VIPs and
// source-based policy routing for multi-Gateway connectivity.
//
// Runs as a sidecar container in each application Pod under a dedicated
// ServiceAccount (separate from controller-manager and stateless-load-balancer).
// Watches a single ENC CR (named after the Pod).
//
// Required RBAC (managed separately — no kubebuilder markers to avoid
// polluting the shared role.yaml via `make manifests`):
//   - meridio-2.nordix.org endpointnetworkconfigurations: get, list, watch
//   - meridio-2.nordix.org endpointnetworkconfigurations/status: get, update, patch
//   - Container capability: NET_ADMIN
type Controller struct {
	client.Client
	Scheme     *runtime.Scheme
	PodName    string
	PodUID     string
	MinTableID int
	MaxTableID int

	nl          netlinkOps
	tableIDs    *tableIDAllocator
	managedVIPs map[string]map[string]bool // interface name → VIP string → true
}

// domainState holds the desired network state for a single domain.
type domainState struct {
	gatewayName   string
	interfaceName string
	vips          []net.IP
	nextHops      []net.IP
	tableID       int
}

// TODO: Improve and double check error handling in the case of the netlink related operations.
func (c *Controller) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	if c.tableIDs == nil {
		c.tableIDs = newTableIDAllocator(c.MinTableID, c.MaxTableID)
		c.managedVIPs = make(map[string]map[string]bool)
	}
	if c.nl == nil {
		c.nl = defaultNetlinkOps{}
	}

	var enc meridio2v1alpha1.EndpointNetworkConfiguration
	if err := c.Get(ctx, req.NamespacedName, &enc); err != nil {
		if apierrors.IsNotFound(err) {
			c.cleanupAll(ctx)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Verify the ENC is owned by this Pod. Prevents acting on an ENC that
	// was created for a different Pod that happened to share the same name
	// (e.g. after a Pod restart with UID change before the old ENC was GC'd).
	if !c.isOwnedByPod(&enc) {
		log.Info("ENC not owned by this pod, skipping", "podUID", c.PodUID)
		return ctrl.Result{}, nil
	}

	// Build desired state. Content errors (invalid VIP, bad CIDR) don't requeue —
	// wait for ENC fix. Interface-not-found is deemed transient — requeue.
	domains, err := c.buildDesiredState(&enc)
	if err != nil {
		log.Error(err, "failed to build desired state")
		if statusErr := c.updateStatus(ctx, &enc, err); statusErr != nil {
			if apierrors.IsConflict(statusErr) {
				return ctrl.Result{Requeue: true}, nil
			}
			return ctrl.Result{}, statusErr
		}
		var infErr *InterfaceNotFoundError
		if errors.As(err, &infErr) {
			return ctrl.Result{}, err // requeue; interface may appear
		}
		return ctrl.Result{}, nil // don't requeue; wait for ENC fix
	}

	// Apply to kernel. Failures here may be transient netlink errors — requeue.
	if err := c.applyState(ctx, domains); err != nil {
		log.Error(err, "failed to apply network configuration")
		if statusErr := c.updateStatus(ctx, &enc, err); statusErr != nil {
			if apierrors.IsConflict(statusErr) {
				return ctrl.Result{Requeue: true}, nil
			}
			return ctrl.Result{}, statusErr
		}
		return ctrl.Result{}, err // requeue for transient netlink errors
	}

	// Clean stale gateways (entire gateway removed from ENC).
	// activeGateways() returns a copy, safe to release during iteration.
	desiredGateways := make(map[string]bool, len(enc.Spec.Gateways))
	for _, gw := range enc.Spec.Gateways {
		desiredGateways[gw.Name] = true
	}
	for gwName, tableID := range c.tableIDs.activeGateways() {
		if !desiredGateways[gwName] {
			flushTable(ctx, c.nl, tableID, c.MinTableID, c.MaxTableID)
			c.tableIDs.release(gwName)
			log.Info("cleaned up stale gateway", "gateway", gwName, "tableID", tableID)
		}
	}

	// Success
	if err := c.updateStatus(ctx, &enc, nil); err != nil {
		if apierrors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (c *Controller) buildDesiredState(enc *meridio2v1alpha1.EndpointNetworkConfiguration) ([]domainState, error) {
	var domains []domainState
	for _, gw := range enc.Spec.Gateways {
		tableID, err := c.tableIDs.allocate(gw.Name)
		if err != nil {
			return nil, fmt.Errorf("gateway %s: %w", gw.Name, err)
		}
		for _, domain := range gw.Domains {
			link, err := findInterfaceBySubnet(c.nl, domain.Network.InterfaceHint, domain.Network.Subnet)
			if err != nil {
				return nil, fmt.Errorf("gateway %s domain %s: %w", gw.Name, domain.Name, err)
			}

			vips := make([]net.IP, 0, len(domain.VIPs))
			for _, s := range domain.VIPs {
				ip := net.ParseIP(s)
				if ip == nil {
					return nil, fmt.Errorf("gateway %s domain %s: invalid VIP %q", gw.Name, domain.Name, s)
				}
				vips = append(vips, ip)
			}

			nextHops := make([]net.IP, 0, len(domain.NextHops))
			for _, s := range domain.NextHops {
				ip := net.ParseIP(s)
				if ip == nil {
					return nil, fmt.Errorf("gateway %s domain %s: invalid next-hop %q", gw.Name, domain.Name, s)
				}
				nextHops = append(nextHops, ip)
			}

			domains = append(domains, domainState{
				gatewayName:   gw.Name,
				interfaceName: link.Attrs().Name,
				vips:          vips,
				nextHops:      nextHops,
				tableID:       tableID,
			})
		}
	}
	return domains, nil
}

// tableState aggregates VIPs and next-hops across all domains sharing a table ID.
type tableState struct {
	vips     []net.IP
	nextHops []net.IP
}

func (c *Controller) applyState(ctx context.Context, domains []domainState) error {
	// --- VIPs: group by interface ---
	byIface := make(map[string][]net.IP)
	for _, d := range domains {
		byIface[d.interfaceName] = append(byIface[d.interfaceName], d.vips...)
	}

	for ifaceName, vips := range byIface {
		link, err := c.nl.LinkByName(ifaceName)
		if err != nil {
			return fmt.Errorf("interface %s: %w", ifaceName, err)
		}
		newManaged, err := syncVIPs(c.nl, link, vips, c.managedVIPs[ifaceName])
		c.managedVIPs[ifaceName] = newManaged
		if err != nil {
			return fmt.Errorf("interface %s VIP sync: %w", ifaceName, err)
		}
	}

	// Remove VIPs from interfaces no longer in desired state
	for ifaceName, managed := range c.managedVIPs {
		if _, exists := byIface[ifaceName]; exists {
			continue
		}
		link, err := c.nl.LinkByName(ifaceName)
		if err != nil {
			continue
		}
		newManaged, err := syncVIPs(c.nl, link, nil, managed)
		c.managedVIPs[ifaceName] = newManaged
		if err != nil {
			return fmt.Errorf("interface %s VIP cleanup: %w", ifaceName, err)
		}
		delete(c.managedVIPs, ifaceName)
	}

	// --- Rules & routes: aggregate by tableID ---
	// Aggregating across domains ensures that when a domain is removed from a
	// gateway, its VIPs disappear from the table's desired set and syncRules
	// removes the stale source-based rules.
	byTable := make(map[int]*tableState)
	for _, d := range domains {
		ts := byTable[d.tableID]
		if ts == nil {
			ts = &tableState{}
			byTable[d.tableID] = ts
		}
		ts.vips = append(ts.vips, d.vips...)
		ts.nextHops = append(ts.nextHops, d.nextHops...)
	}

	for tableID, ts := range byTable {
		if err := syncRules(ctx, c.nl, ts.vips, tableID, c.MinTableID, c.MaxTableID); err != nil {
			return fmt.Errorf("table %d rules: %w", tableID, err)
		}
		if err := syncRoutes(ctx, c.nl, ts.nextHops, tableID); err != nil {
			return fmt.Errorf("table %d routes: %w", tableID, err)
		}
	}

	return nil
}

func (c *Controller) cleanupAll(ctx context.Context) {
	if c.tableIDs == nil {
		return
	}
	// activeGateways() returns a copy, safe to release during iteration.
	for gwName, tableID := range c.tableIDs.activeGateways() {
		flushTable(ctx, c.nl, tableID, c.MinTableID, c.MaxTableID)
		c.tableIDs.release(gwName)
	}
	for ifaceName, managed := range c.managedVIPs {
		link, err := c.nl.LinkByName(ifaceName)
		if err != nil {
			continue
		}
		// TODO: return errors from cleanupAll (and flushTable) so Reconcile can
		// requeue on partial cleanup failure instead of silently succeeding.
		_, _ = syncVIPs(c.nl, link, nil, managed)
	}
	c.managedVIPs = make(map[string]map[string]bool)
}

func (c *Controller) updateStatus(ctx context.Context, enc *meridio2v1alpha1.EndpointNetworkConfiguration, reconcileErr error) error {
	condition := metav1.Condition{
		Type:               "Ready",
		ObservedGeneration: enc.Generation,
	}
	if reconcileErr != nil {
		condition.Status = metav1.ConditionFalse
		condition.Reason = "ConfigurationFailed"
		condition.Message = reconcileErr.Error()
	} else {
		condition.Status = metav1.ConditionTrue
		condition.Reason = "Configured"
		condition.Message = "Network configuration applied"
	}
	if meta.SetStatusCondition(&enc.Status.Conditions, condition) {
		return c.Status().Update(ctx, enc)
	}
	return nil
}

// isOwnedByPod checks if the ENC has an ownerReference pointing to this Pod's UID.
func (c *Controller) isOwnedByPod(enc *meridio2v1alpha1.EndpointNetworkConfiguration) bool {
	for _, ref := range enc.OwnerReferences {
		if ref.Kind == "Pod" && string(ref.UID) == c.PodUID {
			return true
		}
	}
	return false
}

// SetupWithManager sets up the controller with the Manager.
func (c *Controller) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&meridio2v1alpha1.EndpointNetworkConfiguration{}).
		Named("sidecar").
		Complete(c)
}
