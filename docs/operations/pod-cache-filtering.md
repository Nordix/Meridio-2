# Pod Cache Filtering

## Overview

The controller-manager can be configured to only cache Pods that carry a specific label. This reduces memory usage and event processing overhead in namespaces with many unrelated Pods.

When enabled, only Pods with the configured label are visible to the controller-manager's informer cache. All controllers (DG, ENC, Gateway) operate exclusively on labeled Pods. Unlabeled Pods are invisible — no events, no List/Get results.

## Configuration

| Flag | Env Var | Default | Description |
|---|---|---|---|
| `--pod-cache-label` | `MERIDIO_POD_CACHE_LABEL` | _(empty, disabled)_ | Label `key=value` for Pod cache filtering. Empty disables filtering (all Pods cached). |

Example:
```bash
--pod-cache-label=meridio-2.nordix.org/managed=true
```

When set, the controller-manager:
1. Configures the informer cache with the label selector for Pods
2. Adds the label to LB Deployment pod templates (LB Pods inherit it automatically via rolling update)

## Requirements

- **Application Pods** must have the label in their pod template. The label is a documented requirement alongside existing DG selector labels.
- **LB Pods** receive the label automatically via the Deployment template (managed by the Gateway controller).

## Upgrade Procedure

When enabling pod cache filtering on an existing deployment:

1. **Pre-label all relevant Pods** before upgrading the controller-manager:
   ```bash
   # Label application Pods
   kubectl label pods -l <app-selector> -n <namespace> meridio-2.nordix.org/managed=true

   # Label existing LB Pods (avoids transient invisibility during rollout)
   kubectl label pods -l gateway.networking.k8s.io/gateway-name -n <namespace> meridio-2.nordix.org/managed=true
   ```
   Direct Pod labeling does not trigger Deployment rollouts.

2. **Upgrade the controller-manager** with `--pod-cache-label` set.

3. **LB Pods** rolling update proceeds — the controller-manager updates the Deployment template with the label. Pre-labeled old Pods remain visible throughout; new Pods inherit the label from the template.

If pre-labeling is skipped:
- Application Pods without the label will not have ENCs created/updated. Existing ENCs become stale (ownerReference GC won't fire because the Pod still exists).
- LB Pods without the label are invisible during rollout (not included in ENC next-hop lists), causing brief degradation until replaced by new labeled Pods.
- The DG controller incidentally removes unlabeled application Pods from endpoint slices during the LB rollout (new LB Pods trigger the DG Pod mapper → reconciliation lists only labeled Pods).

## Disabling the Feature

Setting `--pod-cache-label` to empty (or removing it) restores the default behavior: all Pods in the namespace are cached.

Note: the label applied to the LB Deployment template is additive. The controller-manager does not remove previously-configured labels from the template when the feature is disabled, since both key and value are user-defined and the controller has no record of what was previously set. The lingering label is harmless but can be removed manually from the LB Deployment template if desired.

## Operational Constraints

The cache label is a **hard contract**:

- Do not remove the label from running application Pods. If removed:
  - The DG controller removes the Pod from endpoint slices (triggered by the synthetic Delete event from the filtered watch)
  - The ENC becomes orphaned (stale content, not garbage-collected because the Pod still exists)

- If runtime label removal is intentional, follow this order:
  1. Remove the DG selector label(s) first (while the Pod is still visible). This triggers proper ENC cleanup.
  2. Then remove the cache label.

- If the DG label removal was missed, delete the stale ENC manually:
  ```bash
  kubectl delete enc <pod-name> -n <namespace>
  ```

## When to Use

The optimization is beneficial when:
- The namespace has many Pods unrelated to Meridio (e.g., 500 Pods, only 10 managed by Meridio)
- Pod churn is high in the namespace (frequent creates/deletes of unrelated Pods)

When most Pods in the namespace are Meridio-managed, the benefit is minimal and the operational overhead of labeling may not be justified.
