# Gateway Controller

## Overview

The Gateway controller manages the lifecycle of Gateway API Gateway resources, creating and managing stateless load balancer (LB) Deployments for L3/L4 traffic handling on secondary networks.

## Architecture

### Core Concepts

**Gateway**: A Gateway API resource representing an L3/L4 load balancer instance. The controller creates a dedicated LB Deployment for each accepted Gateway.

**GatewayClass**: Determines which controller manages a Gateway. Only Gateways with a GatewayClass matching `spec.controllerName` are managed by this controller.

**GatewayConfiguration**: Implementation-specific parameters (replicas, resources, network attachments) referenced via `Gateway.spec.infrastructure.parametersRef`.

**LB Deployment**: A Kubernetes Deployment running load balancer and router containers. Named `sllb-<gateway-name>` and owned by the Gateway via ownerReference.

### Design Principles

- **No finalizers**: Uses ownerReferences only for automatic garbage collection
  - LB Deployments are pure Kubernetes resources (no external cleanup needed)
  - OwnerReferences ensure cleanup even if controller is unavailable
  - Simpler operational model: no manual intervention needed
- **Reconcile loop is authoritative**: Mappers enqueue broadly, reconcile decides
  - GatewayClass mapper filters by controllerName (avoid missing events)
  - L34Route mapper enqueues all Gateway parentRefs (no pre-filtering)
  - Reconcile checks `shouldManageGateway()` for final decision
- **Idempotent reconciliation**: Safe to run multiple times
- **Template-based deployment**: Load LB Deployment from YAML template, customize per Gateway

### Resource Relationships

```
GatewayClass
└── spec.controllerName → matches controller

Gateway
├── spec.gatewayClassName → GatewayClass
├── spec.infrastructure.parametersRef → GatewayConfiguration
└── (owns) → LB Deployment (via ownerReference)

GatewayConfiguration
├── spec.networkAttachments → NAD references
├── spec.networkSubnets → secondary network CIDRs
├── spec.horizontalScaling → replicas, enforceReplicas
└── spec.verticalScaling → container resources

L34Route
├── spec.parentRefs → Gateway
└── spec.destinationCIDRs → VIP addresses

LB Deployment (owned by Gateway)
├── metadata.ownerReferences → Gateway (controller=true)
├── metadata.labels[gateway.networking.k8s.io/gateway-name] → Gateway name
└── spec.template.spec.serviceAccountName → stateless-load-balancer (injected by Kustomize)
```

**Ownership model:**
- Gateway owns LB Deployment via `metadata.ownerReferences` (controller=true)
- Enables automatic garbage collection when Gateway is deleted
- No finalizers needed (Kubernetes handles cleanup)

## Reconciliation Flow

### 1. Fetch Gateway
- Return early if not found (deleted - ownerReferences handle cleanup)
- **Verify no finalizers** (design decision: use ownerReferences only)
  - Skip reconciliation if DeletionTimestamp is set
  - Log error if finalizers present (unexpected state)

### 2. Check GatewayClass
- Fetch GatewayClass referenced by `spec.gatewayClassName`
- Compare `spec.controllerName` with controller's configured name
- Skip if not managed by this controller

**Handle Ownership Transfer:**
- If Gateway's `gatewayClassName` changes to a different controller:
  - Reset `Accepted` to `Unknown` with reason `Pending` (best effort)
  - LB Deployment remains (new controller can delete/replace via ownerReference)
- Gateway API discourages changing `gatewayClassName` (edge case)

### 3. Validate Gateway Configuration
- Combined step via `validateGateway()`: fetch GatewayConfiguration, load template, validate
- Fetch GatewayConfiguration from `spec.infrastructure.parametersRef`
- Load LB Deployment template from `<template-path>/lb-deployment.yaml`
- Validate required fields (networkSubnets, networkAttachments, verticalScaling)
- Validate merged network attachments (template + GatewayConfiguration) for duplicate interfaces
- Three error types with distinct handling:
  - `validationError` → Set `Accepted=False`, don't requeue (wait for user fix)
  - `templateError` → Set `Accepted=False`, requeue with exponential backoff (allow hot-fixing template)
  - API errors → Retry immediately

**Validation rules:**
- parametersRef is required
- GatewayConfiguration must exist
- At least one networkSubnet required
- Only NAD attachment type supported (DRA not yet implemented)
- CIDRs must be valid and not 0.0.0.0/0 or ::/0
- CIDRs must not overlap (across all networkSubnet entries)
- IPv6 link-local addresses (fe80::/10) not allowed
- networkAttachments must be NAD type with valid NAD configuration
- No duplicate interface names within GatewayConfiguration NADs
- No duplicate interface names across merged template + GatewayConfiguration NADs
  - GatewayConfiguration NADs override template NADs with same namespace/name/interface triplet
  - Template NADs default to GatewayConfiguration's namespace when namespace is empty

### 4. Set Accepted Status
- Set `Accepted=True` with reason `Accepted`
- Message includes controller name for identification
- ObservedGeneration tracks which spec version was accepted

**Why check GatewayClass before setting Accepted:**
- Gateway API convention: `Accepted=True` means controller will manage this Gateway
- Prevents multiple controllers from accepting the same Gateway
- Allows users to transfer Gateway ownership by changing `gatewayClassName`

### 5. Update Addresses from L34Routes
- List all L34Routes in namespace (or cluster-wide if `--namespace` is empty)
- Filter routes referencing this Gateway in `spec.parentRefs`
- Extract unique destination CIDRs (VIP addresses)
- Update `status.addresses` with sorted IP addresses

**Why aggregate addresses from routes:**
- Gateway API pattern: Gateway status reflects actual VIPs served
- Enables clients to discover available VIPs without listing routes
- Sorted for deterministic output

### 6. Reconcile LB Deployment
- Use pre-fetched template and GatewayConfiguration from step 3
- Customize for this Gateway:
  - Name: `sllb-<gateway-name>`
  - Namespace: same as Gateway
  - Labels: `app=sllb-<gateway-name>`, `gateway.networking.k8s.io/gateway-name=<gateway-name>`
  - ServiceAccount: from `--lb-service-account` flag (injected by Kustomize)
  - Selector: `app=sllb-<gateway-name>` (immutable)
  - Anti-affinity: updated to match deployment-specific labels
  - Gateway name injection: `MERIDIO_GATEWAY_NAME` env var in containers
- Merge `spec.infrastructure.labels` and `spec.infrastructure.annotations`
- **Apply GatewayConfiguration** (if referenced):
  - Horizontal scaling: replicas (respects `enforceReplicas` flag)
  - Vertical scaling: container resources (respects `enforceResources` flag)
  - Network attachments: NAD annotations on pod template (template + GatewayConfiguration as authoritative sources, GatewayConfiguration overrides template)
- Set ownerReference to Gateway
- Create if not exists, update if changed (semantic equality check implemented)

**Implementation details:**
- Follows Meridio v1 "existing-as-base" pattern
- Single code path via `reconcileDeploymentSpec(base, template, ...)` where `base == nil` for create
- Uses `maps.Equal()` and `maps.Copy()` for map operations
- Preserves external labels/annotations while enforcing controller-managed fields
- Preserves existing container resources after template merge (external tools/VPA values not lost)
- Semantic equality check via `deploymentNeedsUpdate()` before calling `r.Update()`
- `SetControllerReference()` only called for new Deployments (prevents spurious updates)

**Conflict handling:**
- Transient errors (Conflict, AlreadyExists) → Requeue without setting `Programmed=False`
- Permanent errors (name collision, create failure) → Set `Programmed=False` + return error
- Follows Meridio v1 pattern: helper functions return wrapped errors, Reconcile decides requeue strategy

**Why template-based approach:**
- Flexibility: Users can customize LB container images, resources, etc.
- Separation of concerns: Controller logic vs deployment configuration
- Kustomize integration: Templates live in `config/templates/`, packaged as ConfigMap

**ServiceAccount name handling:**
- Template uses placeholder ServiceAccount name (`stateless-load-balancer`)
- Kustomize replacement injects actual name after `namePrefix` is applied
- Controller reads from `--lb-service-account` flag (bound to `MERIDIO_LB_SERVICE_ACCOUNT` env var)
- Works with any Kustomize configuration (no hardcoded prefixed names)

**Gateway name injection:**

The controller injects Gateway identity into LB pod containers via environment variables:
- `MERIDIO_GATEWAY_NAME` - Set to Gateway name by controller (template has placeholder comment)
- `MERIDIO_GATEWAY_NAMESPACE` - Set via Downward API (`metadata.namespace`)
- Applied to both `loadbalancer` and `router` containers
- LoadBalancer and Router controllers use these to identify which Gateway they serve

**Label and annotation management:**

The controller applies labels/annotations in three layers with specific precedence:

1. **Controller-managed labels** (always enforced, highest priority):
   - `app: sllb-<gateway-name>` - Used for selector (immutable) and pod anti-affinity
   - `gateway.networking.k8s.io/gateway-name: <gateway-name>` - Gateway API standard label

2. **Gateway infrastructure labels/annotations** (user-controlled, middle priority):
   - From `Gateway.spec.infrastructure.labels` → Deployment labels + Pod template labels
   - From `Gateway.spec.infrastructure.annotations` → Deployment annotations + Pod template annotations
   - Can override template labels/annotations
   - Cannot override controller-managed labels

3. **Template labels/annotations** (base layer, lowest priority):
   - From `lb-deployment.yaml` template
   - Merged with existing Deployment labels/annotations on updates
   - Preserves external labels/annotations added by other tools

**Merge behavior:**
- Create: Template → Infrastructure → Controller-managed
- Update: Existing → Template → Infrastructure → Controller-managed
- Uses `mergeMaps()` helper: later layers overwrite earlier layers
- External labels/annotations preserved unless explicitly overridden

**Example:**
```yaml
# Template has:
labels:
  template-label: value1

# Gateway.spec.infrastructure.labels:
infrastructure:
  labels:
    custom-label: value2
    template-label: overridden  # Overwrites template

# Result on Deployment:
labels:
  app: sllb-my-gateway                              # Controller-managed
  gateway.networking.k8s.io/gateway-name: my-gateway # Controller-managed
  custom-label: value2                               # Infrastructure
  template-label: overridden                         # Infrastructure (overwrote template)
```

**Anti-affinity label updates:**
- Template may contain pod anti-affinity rules with `app` label selector
- Controller updates anti-affinity `matchExpressions` to use deployment-specific `app` value
- Ensures pods from different Gateways don't anti-affine with each other

### 7. Set Programmed Status
- Set `Programmed=True` with reason `Programmed` after successful deployment reconciliation
- Set `Programmed=False` for permanent errors that prevent data plane configuration:
  - Name collision (Deployment owned by different Gateway)
  - Deployment creation failure
- Transient errors (API conflicts, network issues) don't alter `Programmed` status

## Watch Triggers

The controller reconciles when:

| Resource | Trigger | Mapper Function | Filtering Strategy |
|----------|---------|-----------------|-------------------|
| Gateway | Create/Update/Delete | Direct (`.For()`) | None (all events) |
| Deployment | Create/Update/Delete | Owned (`.Owns()`) | ownerReference |
| GatewayClass | Create/Update/Delete | Matches controllerName | GatewayClass-based |
| L34Route | Create/Update/Delete | References Gateway in parentRefs | None (enqueues all) |
| GatewayConfiguration | Create/Update/Delete | Referenced by Gateway | None (enqueues all) |

### Filtering Strategy Rationale

**GatewayClass mapper:**
- Uses GatewayClass-based filtering (checks `spec.controllerName`)
- Avoids missing events when GatewayClass changes
- Reconciles all Gateways using the affected GatewayClass

**L34Route mapper:**
- No pre-filtering (enqueues all Gateway parentRefs)
- Reconcile loop is authoritative for acceptance checks
- Simpler and more robust than status-based filtering in mapper
- Why not filter by Gateway `Accepted` status?
  - Would require fetching Gateway from cache in mapper (extra complexity)
  - Reconcile already has early exit for unmanaged Gateways
  - Extra reconciles are cheap (controller-runtime uses in-memory cache)
  - Work queue deduplicates requests (multiple route changes → less reconcile)
  - Simpler code with no edge cases to reason about

**GatewayConfiguration mapper:**
- No pre-filtering (enqueues all Gateways referencing the changed GatewayConfiguration)
- Reconcile loop decides whether to process (same pattern as L34Route mapper)
- parametersRef check already narrows to relevant Gateways

## Status Conditions

### Accepted Condition

**Type:** `Accepted`

**Status:** `True` | `False` | `Unknown`

**Reasons:**
- `Accepted`: Gateway is valid and will be managed by this controller
- `InvalidParameters`: GatewayConfiguration is missing/invalid, or template load failure
- `Pending`: Waiting for controller (default for Unknown status)

**Lifecycle:**
1. Gateway created → `Unknown` (Pending) - waiting for controller
2. Controller validates → `True` (Accepted) - will manage this Gateway
3. GatewayConfiguration invalid → `False` (InvalidParameters) - won't create LB Deployment, no requeue
4. Template load failure → `False` (InvalidParameters) - requeue with exponential backoff
5. Gateway deleted → ownerReferences clean up LB Deployment

**ObservedGeneration:** Tracks which Gateway.spec version was evaluated

### Programmed Condition

**Type:** `Programmed`

**Status:** `True` | `False` | `Unknown`

**Reasons:**
- `Programmed` (status=True): LB Deployment reconciled successfully
- `Invalid` (status=False): Deployment creation failure or name collision
- `Pending` (status=Unknown): Waiting for LB Deployment reconciliation (default)

**Lifecycle:**
1. Gateway created → `Unknown` (Pending) - waiting for LB Deployment reconciliation
2. LB Deployment created → `True` (Programmed) - config sent to data plane (pods may still be initializing)
3. Permanent error (name collision, create failure) → `False` (Invalid) - cannot send config to data plane
4. Transient error (API conflict, network issue) → status unchanged - controller will retry

**Note:** `Programmed=True` indicates the LB Deployment resource was successfully reconciled, but does not guarantee the data plane is ready to process traffic. The LB pods run their own controllers (loadbalancer and router) that configure the data plane asynchronously.

**ObservedGeneration:** Tracks which Gateway.spec version was programmed

## GatewayConfiguration Application

The controller applies GatewayConfiguration values to the LB Deployment during reconciliation (step 6). 
All features are implemented and production-ready.

**Note:** Resource changes trigger Pod recreation via RollingUpdate (brief downtime per Pod). 
In-place Pod resize is not implemented (see Future Enhancements section).

### Horizontal Scaling

Controls LB Deployment replica count with optional HPA integration:

```yaml
horizontalScaling:
  replicas: 2
  enforceReplicas: false  # Let HPA manage (default)
```

**Behavior:**
- `enforceReplicas=false`: Apply replicas on initial creation, skip updates (HPA manages)
- `enforceReplicas=true`: Always enforce replicas value (override HPA)

### Vertical Scaling

Controls container resource requests/limits with optional VPA integration:

```yaml
verticalScaling:
  containers:
  - name: loadbalancer
    enforceResources: true
    resources:
      requests:
        cpu: "200m"
        memory: "256Mi"
      limits:
        cpu: "500m"
        memory: "512Mi"
    resizePolicy:
    - resourceName: cpu
      restartPolicy: NotRequired
```

**Behavior:**
- `enforceResources=false`: Apply resources on initial creation, preserve existing deployment values on updates (VPA/external tool manages)
- `enforceResources=true`: Always enforce resources from GatewayConfiguration (updates Deployment template → triggers Pod recreation via RollingUpdate)
- Switching from `true` to `false`: Last enforced values are preserved, external tool takes over from there

**Current MVP Implementation:**
- Updates `Deployment.spec.template.spec.containers[].resources` directly
- Triggers Pod recreation via RollingUpdate when resources change
- Works on all Kubernetes versions (no feature gates required)
- Existing container resources are preserved across reconciliations (template does not overwrite them on updates)

**Partial management:**
- Omit `verticalScaling` → Controller doesn't touch any container resources (existing values preserved)
- List specific containers → Only those containers are managed (when `enforceResources=true`)
- Unlisted containers → Keep their existing deployment values (not reset to template defaults)

**Desired state semantics:**
- User must provide complete Resources (requests + limits if needed)
- Omitting fields means "no value desired" (sets to empty/nil)
- Allows QoS class changes (e.g., remove limits for Burstable)
- ResourceClaims not supported (validation rejects - DRA not implemented)

### Network Attachments

Applies NAD annotations to LB Deployment pod template:

```yaml
networkAttachments:
- type: NAD
  nad:
    namespace: meridio-2
    name: net1
    interface: net1
- type: NAD
  nad:
    namespace: meridio-2
    name: net2
    interface: net2
```

**Implementation:**
- Uses template + GatewayConfiguration as authoritative sources (not existing Deployment)
- Parses template NAD annotation and GatewayConfiguration NADs
- GatewayConfiguration NADs override template NADs (same namespace/name/interface)
- Template NADs not in GatewayConfiguration are preserved (including extra fields like ips, mac, bandwidth)
- Supports both JSON and shorthand formats when reading, always writes JSON
- Semantic (order-independent) comparison to avoid unnecessary rolling updates from NAD reordering
- Duplicate detection: `namespace/name:interface` (all 3 fields must match)
- Same NAD with different interfaces allowed (not duplicate)
- DRA attachments skipped (not yet implemented)

**Note:** NAD annotation handling differs from general label/annotation merging. NADs use template + GatewayConfiguration as authoritative sources (not existing Deployment), enabling proper replacement when GatewayConfiguration changes.

## Edge Cases

### Gateway Not Managed by This Controller
- Skip reconciliation (no status update)
- Avoids conflicts with other Gateway API implementations

### GatewayClass Not Found
- Skip reconciliation (treat as unmanaged)
- Allows users to delete GatewayClass without breaking Gateways

### GatewayConfiguration Missing
- Set `Accepted=False` with reason `InvalidParameters`
- Message indicates missing or invalid configuration
- Controller does not requeue (waits for user to fix configuration)

### Template Load Failure
- Set `Accepted=False` with reason `InvalidParameters`
- Controller requeues with exponential backoff (allows hot-fixing template without restart)

### Gateway Being Deleted
- Skip reconciliation (ownerReferences handle cleanup)
- LB Deployment deleted automatically by Kubernetes GC
- No finalizers needed

### Concurrent Reconciles
- Status update conflicts handled gracefully
- Automatic requeue with fresh resourceVersion
- Idiomatic Kubernetes pattern

### LB Deployment Modified Externally
- Detected via `.Owns()` watch
- Overwritten on next reconcile (controller owns the resource)
- Semantic equality check prevents unnecessary updates

### LB Deployment Name Collision
- If Deployment with desired name exists but owned by different Gateway → permanent error
- Error message includes actual owner for debugging
- Gateway status set to `Programmed=False` with reason `Invalid`
- User must resolve conflict (rename/delete conflicting Deployment or Gateway)

**Naming enforcement:**
- Controller uses strict name-based lookup: `sllb-<gateway-name>`
- No label-based fallback (ensures consistent naming for external tools like HPA)
- If Deployment is manually renamed, controller creates new Deployment with correct name
- Old Deployment remains orphaned (manual cleanup required)

### Ownership Transfer (gatewayClassName Changed)
- Old controller resets `Accepted` to `Unknown` (best effort)
- New controller can accept and manage Gateway
- LB Deployment remains (new controller decides whether to delete/replace)
- Gateway API discourages this scenario

## Configuration

### Controller Flags

**`--namespace`**: Limit to single namespace (empty = all namespaces)

**`--controller-name`**: 
- Used to match GatewayClass.spec.controllerName
- Default: `registry.nordix.org/cloud-native/meridio-2/gateway-controller`

**`--template-path`**:
- Path to template directory containing `lb-deployment.yaml`
- Default: `/templates`

**`--lb-service-account`**:
- ServiceAccount name for LB Deployment pods
- Default: `stateless-load-balancer`
- Injected by Kustomize replacement (handles namePrefix)

### Environment Variables
All flags can be set via `MERIDIO_*` environment variables (e.g., `MERIDIO_NAMESPACE`).

## Testing

### Manual Testing in Cluster

**Prerequisites:**
- Controller manager deployed
- Gateway API CRDs installed
- Multus + Whereabouts installed
- Namespace `meridio-2` exists

#### Setup: Deploy Base Resources

```bash
cat <<'EOF' | kubectl apply -f -
---
# NAD: Bridge with Whereabouts (169.254.100.0/24 + 100:100::/64)
# Used by the router container for external gateway connectivity
apiVersion: k8s.cni.cncf.io/v1
kind: NetworkAttachmentDefinition
metadata:
  name: vlan-100
  namespace: meridio-2
spec:
  config: '{
      "cniVersion": "0.4.0",
      "name": "bridge-net1",
      "plugins": [
        {
          "type": "bridge",
          "name": "mybridge1",
          "bridge": "br-meridio",
          "vlan": 100,
          "ipam": {
            "type": "whereabouts",
            "enable_overlapping_ranges": false,
            "ipRanges": [{
              "range": "169.254.100.0/24",
              "exclude": [
                "169.254.100.1/32",
                "169.254.100.254/32"
              ]
            }, {
              "range": "100:100::/64",
              "exclude": [
                "100:100::1/128"
              ]
            }]
          }
        }
      ]
  }'
---
# NAD: MACVLAN with Whereabouts (192.168.100.0/24)
# Used by LB pods to reach application endpoints on the secondary network
apiVersion: k8s.cni.cncf.io/v1
kind: NetworkAttachmentDefinition
metadata:
  name: macvlan-net1
  namespace: meridio-2
spec:
  config: '{
      "cniVersion": "1.0.0",
      "name": "macvlan-nad-1",
      "plugins": [
          {
              "type": "macvlan",
              "master": "eth0",
              "mode": "bridge",
              "ipam": {
                  "log_file": "/tmp/whereabouts.log",
                  "type": "whereabouts",
                  "ipRanges": [
                      {
                          "range": "192.168.100.0/24"
                      }
                  ]
              }
          }
      ]
  }'
---
apiVersion: gateway.networking.k8s.io/v1
kind: GatewayClass
metadata:
  name: meridio-2
spec:
  controllerName: registry.nordix.org/cloud-native/meridio-2/gateway-controller
---
apiVersion: meridio-2.nordix.org/v1alpha1
kind: GatewayConfiguration
metadata:
  name: test-gwconfig
  namespace: meridio-2
spec:
  networkAttachments:
    - type: NAD
      description: "Router external connectivity"
      nad:
        name: vlan-100
        namespace: meridio-2
        interface: ext1
    - type: NAD
      description: "LB-to-endpoint secondary network"
      nad:
        name: macvlan-net1
        namespace: meridio-2
        interface: net1
  networkSubnets:
    - attachmentType: NAD
      cidrs:
        - "192.168.100.0/24"
  horizontalScaling:
    replicas: 2
    enforceReplicas: true
  verticalScaling:
    containers:
      - name: loadbalancer
        enforceResources: true
        resources:
          requests:
            cpu: 200m
            memory: 256Mi
          limits:
            cpu: "1"
            memory: 1Gi
---
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: test-gateway
  namespace: meridio-2
spec:
  gatewayClassName: meridio-2
  infrastructure:
    parametersRef:
      group: meridio-2.nordix.org
      kind: GatewayConfiguration
      name: test-gwconfig
  listeners:
    - name: default
      protocol: TCP
      port: 80
---
apiVersion: meridio-2.nordix.org/v1alpha1
kind: L34Route
metadata:
  name: test-route
  namespace: meridio-2
spec:
  parentRefs:
    - name: test-gateway
  backendRefs:
    - name: test-backend
      group: meridio-2.nordix.org
      kind: DistributionGroup
  destinationCIDRs:
    - "20.0.0.1/32"
    - "20.0.0.2/32"
    - "40.0.0.1/32"
    - "2000::1/128"
    - "3000::1/128"
  protocols:
    - TCP
  priority: 1
EOF
```

#### Test 1: Gateway Acceptance and LB Deployment Creation

```bash
# Accepted=True
kubectl -n meridio-2 get gateway test-gateway -o jsonpath='{.status.conditions[?(@.type=="Accepted")]}'
# Expected: status=True, reason=Accepted

# Programmed=True
kubectl -n meridio-2 get gateway test-gateway -o jsonpath='{.status.conditions[?(@.type=="Programmed")]}'
# Expected: status=True, reason=Programmed

# Deployment exists with ownerReference
kubectl -n meridio-2 get deployment sllb-test-gateway \
  -o jsonpath='{.metadata.ownerReferences[0].kind}/{.metadata.ownerReferences[0].name}'
# Expected: Gateway/test-gateway

# ServiceAccount from controller flag
kubectl -n meridio-2 get deployment sllb-test-gateway -o jsonpath='{.spec.template.spec.serviceAccountName}'
# Expected: stateless-load-balancer (or whatever --lb-service-account is set to)
```

#### Test 2: Network Attachments

```bash
# NAD annotation present on pod template with both interfaces
kubectl -n meridio-2 get deployment sllb-test-gateway \
  -o jsonpath='{.spec.template.metadata.annotations.k8s\.v1\.cni\.cncf\.io/networks}'
# Expected: JSON array containing vlan-100 (interface: ext1) and macvlan-net1 (interface: net1)

# Capture current pod names before NAD change
PODS_BEFORE=$(kubectl -n meridio-2 get pods -l app=sllb-test-gateway -o jsonpath='{.items[*].metadata.name}')

# Remove one NAD from GatewayConfiguration
kubectl -n meridio-2 patch gatewayconfiguration test-gwconfig --type=json \
  -p '[{"op":"replace","path":"/spec/networkAttachments","value":[{"type":"NAD","nad":{"name":"vlan-100","namespace":"meridio-2","interface":"ext1"}}]}]'
sleep 10
kubectl -n meridio-2 get deployment sllb-test-gateway \
  -o jsonpath='{.spec.template.metadata.annotations.k8s\.v1\.cni\.cncf\.io/networks}'
# Expected: JSON array containing only vlan-100 (macvlan-net1 removed)

# Verify pods were recreated (NAD change modifies pod template → rolling update)
PODS_AFTER=$(kubectl -n meridio-2 get pods -l app=sllb-test-gateway -o jsonpath='{.items[*].metadata.name}')
[ "$PODS_BEFORE" != "$PODS_AFTER" ] && echo "PASS: pods recreated" || echo "FAIL: pods unchanged"

# Restore both NADs
kubectl -n meridio-2 patch gatewayconfiguration test-gwconfig --type=json \
  -p '[{"op":"replace","path":"/spec/networkAttachments","value":[{"type":"NAD","nad":{"name":"vlan-100","namespace":"meridio-2","interface":"ext1"}},{"type":"NAD","nad":{"name":"macvlan-net1","namespace":"meridio-2","interface":"net1"}}]}]'
```

#### Test 3: Horizontal Scaling (enforceReplicas=true)

```bash
# Replicas match GatewayConfiguration
kubectl -n meridio-2 get deployment sllb-test-gateway -o jsonpath='{.spec.replicas}'
# Expected: 2

# Manually scale - controller should revert
kubectl -n meridio-2 scale deployment sllb-test-gateway --replicas=5
sleep 3
kubectl -n meridio-2 get deployment sllb-test-gateway -o jsonpath='{.spec.replicas}'
# Expected: 2 (controller enforces)
```

#### Test 4: Horizontal Scaling (enforceReplicas=false / HPA deferral)

```bash
# Switch to non-enforcing mode
kubectl -n meridio-2 patch gatewayconfiguration test-gwconfig --type=merge \
  -p '{"spec":{"horizontalScaling":{"enforceReplicas":false}}}'
sleep 3

# Manually scale - controller should NOT revert
kubectl -n meridio-2 scale deployment sllb-test-gateway --replicas=5
sleep 3
kubectl -n meridio-2 get deployment sllb-test-gateway -o jsonpath='{.spec.replicas}'
# Expected: 5 (controller defers to external scaler)

# Restore enforcing mode
kubectl -n meridio-2 patch gatewayconfiguration test-gwconfig --type=merge \
  -p '{"spec":{"horizontalScaling":{"replicas":2,"enforceReplicas":true}}}'
```

#### Test 5: Vertical Scaling (enforceResources=true)

```bash
# Loadbalancer container resources match GatewayConfiguration
kubectl -n meridio-2 get deployment sllb-test-gateway \
  -o jsonpath='{.spec.template.spec.containers[?(@.name=="loadbalancer")].resources}'
# Expected: requests.cpu=200m, requests.memory=256Mi, limits.cpu=1, limits.memory=1Gi

# Router container keeps template defaults (not in verticalScaling)
kubectl -n meridio-2 get deployment sllb-test-gateway \
  -o jsonpath='{.spec.template.spec.containers[?(@.name=="router")].resources}'
# Expected: template defaults (requests.cpu=100m, requests.memory=128Mi, etc.)

# Update resources via GatewayConfiguration
kubectl -n meridio-2 patch gatewayconfiguration test-gwconfig --type=json \
  -p '[{"op":"replace","path":"/spec/verticalScaling/containers","value":[{"name":"loadbalancer","enforceResources":true,"resources":{"requests":{"cpu":"500m","memory":"512Mi"},"limits":{"cpu":"2","memory":"2Gi"}}}]}]'
sleep 3
kubectl -n meridio-2 get deployment sllb-test-gateway \
  -o jsonpath='{.spec.template.spec.containers[?(@.name=="loadbalancer")].resources.requests.cpu}'
# Expected: 500m
```

#### Test 6: Vertical Scaling (enforceResources=false / VPA deferral)

```bash
# Switch loadbalancer to non-enforcing
kubectl -n meridio-2 patch gatewayconfiguration test-gwconfig --type=json \
  -p '[{"op":"replace","path":"/spec/verticalScaling/containers","value":[{"name":"loadbalancer","enforceResources":false,"resources":{"requests":{"cpu":"999m"}}}]}]'
sleep 3

# Resources should remain at previous values (controller defers)
kubectl -n meridio-2 get deployment sllb-test-gateway \
  -o jsonpath='{.spec.template.spec.containers[?(@.name=="loadbalancer")].resources.requests.cpu}'
# Expected: 500m (unchanged, controller does not enforce)
```

#### Test 7: Status Addresses from L34Routes

```bash
kubectl -n meridio-2 get gateway test-gateway -o jsonpath='{.status.addresses[*].value}'
# Expected: 20.0.0.1 20.0.0.2 2000::1 3000::1 40.0.0.1 (sorted)
```

#### Test 8: Validation Errors (Accepted=False)

```bash
# 8a: Missing GatewayConfiguration reference
kubectl apply -f - <<EOF
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: test-no-config
  namespace: meridio-2
spec:
  gatewayClassName: meridio-2
  listeners:
    - name: default
      protocol: TCP
      port: 80
EOF
sleep 2
kubectl -n meridio-2 get gateway test-no-config -o jsonpath='{.status.conditions[?(@.type=="Accepted")]}'
# Expected: status=False, reason=InvalidParameters, message mentions "reference is required"
kubectl delete gateway test-no-config -n meridio-2

# 8b: Nonexistent GatewayConfiguration
kubectl apply -f - <<EOF
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: test-bad-ref
  namespace: meridio-2
spec:
  gatewayClassName: meridio-2
  infrastructure:
    parametersRef:
      group: meridio-2.nordix.org
      kind: GatewayConfiguration
      name: does-not-exist
  listeners:
    - name: default
      protocol: TCP
      port: 80
EOF
sleep 2
kubectl -n meridio-2 get gateway test-bad-ref -o jsonpath='{.status.conditions[?(@.type=="Accepted")]}'
# Expected: status=False, reason=InvalidParameters, message mentions "not found"
kubectl delete gateway test-bad-ref -n meridio-2

# 8c: Invalid CIDR in networkSubnets
kubectl apply -f - <<EOF
apiVersion: meridio-2.nordix.org/v1alpha1
kind: GatewayConfiguration
metadata:
  name: bad-cidr
  namespace: meridio-2
spec:
  networkAttachments: []
  networkSubnets:
    - attachmentType: NAD
      cidrs:
        - "fe80:1::/45"
  horizontalScaling:
    replicas: 1
    enforceReplicas: false
EOF
kubectl -n meridio-2 patch gateway test-gateway --type=merge \
  -p '{"spec":{"infrastructure":{"parametersRef":{"group":"meridio-2.nordix.org","kind":"GatewayConfiguration","name":"bad-cidr"}}}}'
sleep 2
kubectl -n meridio-2 get gateway test-gateway -o jsonpath='{.status.conditions[?(@.type=="Accepted")]}'
# Expected: status=False, reason=InvalidParameters, message mentions "IPv6 link-local addresses"

# Restore valid config
kubectl -n meridio-2 patch gateway test-gateway --type=merge \
  -p '{"spec":{"infrastructure":{"parametersRef":{"group":"meridio-2.nordix.org","kind":"GatewayConfiguration","name":"test-gwconfig"}}}}'
kubectl delete gatewayconfiguration bad-cidr -n meridio-2

# 8d: Duplicate interface names in networkAttachments
kubectl apply -f - <<EOF
apiVersion: meridio-2.nordix.org/v1alpha1
kind: GatewayConfiguration
metadata:
  name: dup-iface
  namespace: meridio-2
spec:
  networkAttachments:
    - type: NAD
      nad: { name: vlan-100, namespace: meridio-2, interface: ext1 }
    - type: NAD
      nad: { name: macvlan-net1, namespace: meridio-2, interface: ext1 }
  networkSubnets:
    - attachmentType: NAD
      cidrs: ["192.168.100.0/24"]
  horizontalScaling:
    replicas: 1
    enforceReplicas: false
EOF
kubectl -n meridio-2 patch gateway test-gateway --type=merge \
  -p '{"spec":{"infrastructure":{"parametersRef":{"group":"meridio-2.nordix.org","kind":"GatewayConfiguration","name":"dup-iface"}}}}'
sleep 2
kubectl -n meridio-2 get gateway test-gateway -o jsonpath='{.status.conditions[?(@.type=="Accepted")]}'
# Expected: status=False, reason=InvalidParameters, message mentions "duplicate interface"

# Restore valid config
kubectl -n meridio-2 patch gateway test-gateway --type=merge \
  -p '{"spec":{"infrastructure":{"parametersRef":{"group":"meridio-2.nordix.org","kind":"GatewayConfiguration","name":"test-gwconfig"}}}}'
kubectl delete gatewayconfiguration dup-iface -n meridio-2

# 8e: Overlapping CIDRs across networkSubnets
kubectl apply -f - <<EOF
apiVersion: meridio-2.nordix.org/v1alpha1
kind: GatewayConfiguration
metadata:
  name: overlap-cidr
  namespace: meridio-2
spec:
  networkAttachments:
    - type: NAD
      nad: { name: macvlan-net1, namespace: meridio-2, interface: net1 }
  networkSubnets:
    - attachmentType: NAD
      cidrs: ["192.168.0.0/16"]
    - attachmentType: NAD
      cidrs: ["192.168.100.0/24"]
  horizontalScaling:
    replicas: 1
    enforceReplicas: false
EOF
kubectl -n meridio-2 patch gateway test-gateway --type=merge \
  -p '{"spec":{"infrastructure":{"parametersRef":{"group":"meridio-2.nordix.org","kind":"GatewayConfiguration","name":"overlap-cidr"}}}}'
sleep 2
kubectl -n meridio-2 get gateway test-gateway -o jsonpath='{.status.conditions[?(@.type=="Accepted")]}'
# Expected: status=False, reason=InvalidParameters, message mentions "overlapping"

# Restore valid config
kubectl -n meridio-2 patch gateway test-gateway --type=merge \
  -p '{"spec":{"infrastructure":{"parametersRef":{"group":"meridio-2.nordix.org","kind":"GatewayConfiguration","name":"test-gwconfig"}}}}'
kubectl delete gatewayconfiguration overlap-cidr -n meridio-2
```

#### Test 9: Automatic Cleanup (No Finalizers)

```bash
kubectl delete gateway test-gateway -n meridio-2
kubectl -n meridio-2 get gateway test-gateway 2>&1 | grep "NotFound"
# Expected: deleted immediately (no Terminating state)

sleep 5
kubectl -n meridio-2 get deployment sllb-test-gateway 2>&1 | grep "NotFound"
# Expected: Deployment auto-deleted via ownerReference
```

#### Test 10: Ownership Transfer

```bash
# Re-create Gateway
kubectl apply -f - <<EOF
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: test-gateway
  namespace: meridio-2
spec:
  gatewayClassName: meridio-2
  infrastructure:
    parametersRef:
      group: meridio-2.nordix.org
      kind: GatewayConfiguration
      name: test-gwconfig
  listeners:
    - name: default
      protocol: TCP
      port: 80
EOF
sleep 2

# Create another GatewayClass
kubectl apply -f - <<EOF
apiVersion: gateway.networking.k8s.io/v1
kind: GatewayClass
metadata:
  name: other-controller
spec:
  controllerName: example.com/other-controller
EOF

# Change Gateway's gatewayClassName
kubectl -n meridio-2 patch gateway test-gateway --type=merge -p '{"spec":{"gatewayClassName":"other-controller"}}'
sleep 2
kubectl -n meridio-2 get gateway test-gateway -o jsonpath='{.status.conditions[?(@.type=="Accepted")]}'
# Expected: status=Unknown, reason=Pending
```

#### Cleanup

```bash
kubectl delete gateway test-gateway -n meridio-2 --ignore-not-found
kubectl delete l34route test-route -n meridio-2 --ignore-not-found
kubectl delete gatewayconfiguration test-gwconfig -n meridio-2 --ignore-not-found
kubectl delete gatewayclass meridio-2 other-controller --ignore-not-found
kubectl delete net-attach-def vlan-100 macvlan-net1 -n meridio-2 --ignore-not-found
```

## Future Enhancements

### Gateway API Standard Labels

**Current state:** LB Deployment and Pod template include:
```yaml
metadata:
  labels:
    gateway.networking.k8s.io/gateway-name: <gateway-name>  # Gateway API standard
    app: sllb-<gateway-name>                                # Deployment selector
    app.kubernetes.io/managed-by: meridio-2-controller-manager  # Operator identity
```

The `app.kubernetes.io/managed-by` label identifies all LB Pods as infrastructure managed by the
Meridio-2 controller manager, regardless of which Gateway they belong to. This enables:
- **Pod watchers** (e.g., Endpoint Network Configurator) to select all LB Pods via a single label selector
- **Operational tooling** (`kubectl get pods -l app.kubernetes.io/managed-by=meridio-2-controller-manager`)
- **Lifecycle clarity**: `managed-by: meridio-2-controller-manager` indicates the higher-level controller responsible for the Deployment lifecycle (even though Kubernetes' Deployment controller manages the Pods)

The value is a static constant (`meridio-2-controller-manager`), not derived from `--controller-name`.
The controller-name flag identifies which GatewayClass this controller manages (Gateway API protocol concern),
while managed-by identifies which operator created the resource (operational concern).

**Enhancement:** Add remaining recommended labels per [GEP-2659 (Gateway API Label Conventions)](https://gateway-api.sigs.k8s.io/geps/gep-2659/):
```yaml
metadata:
  labels:
    # Existing (implemented)
    gateway.networking.k8s.io/gateway-name: <gateway-name>
    app: sllb-<gateway-name>
    app.kubernetes.io/managed-by: meridio-2-controller-manager
    
    # Add for better compliance
    app.kubernetes.io/name: meridio-2
    app.kubernetes.io/instance: <gateway-name>
    app.kubernetes.io/component: gateway
```

**Why add remaining labels:**
- **Discoverability**: Standard labels enable filtering (`kubectl get deployments -l app.kubernetes.io/component=gateway`)
- **Observability**: Monitoring tools recognize standard labels for resource attribution
- **Interoperability**: Other Gateway API tools expect these labels
- Follows pattern used by Istio, Kong, and other Gateway API implementations

### Vertical Scaling: In-place Pod Resize

**Current limitation:** Resource changes trigger Pod recreation via RollingUpdate (brief downtime per Pod).

**Enhancement goal:** Update Pod resources in-place without recreation (zero downtime).

**Research findings:**
- Requires `InPlacePodVerticalScaling` feature gate (alpha in K8s 1.27+, beta in 1.29+)
- API server blocks direct Pod resource patches unless feature gate enabled
- **VPA does NOT use in-place resize**: VPA evicts Pods → Deployment recreates → VPA mutating webhook modifies resources during creation
- pod-template-hash is NOT recomputed when resources change in-place
- Deployment controller does NOT detect drift after in-place Pod resize

**Cluster testing results (K8s v1.31.0 with feature gate enabled):**
- ✅ Direct Pod resource patching works (no API server rejection)
- ✅ Deployment controller does not recreate Pods after in-place resize
- ✅ pod-template-hash remains unchanged after Pod patch
- ⚠️ Deployment template and Pod resources diverge (template shows old values)
- ⚠️ Next Deployment update (any reason) reverts Pod resources to template values
- ⚠️ New Pods (scale-up, node failure) use template values, not patched values

**Implementation approach:**

The only viable approach is **Pod-only patching** (do NOT update Deployment template):

```
EnforceResources = true (with feature gate enabled):
  1. Patch existing Pods in-place (if ResizePolicy allows)
  2. Do NOT update Deployment template (would trigger rollout)
  3. Store desired resources in annotation (e.g., meridio-2.nordix.org/desired-resources)
  4. Watch LB Deployment Pods (.Owns()):
     - New Pods: Patch to match annotation
     - Existing Pods: Patch if resources mismatch annotation
```

**Why not use a mutating webhook like VPA?**
- Mutating webhooks only intercept CREATE operations (not updates)
- In-place resize requires patching existing Pods
- Webhooks cannot help with in-place patching

**Trade-offs:**

✅ **Benefits:**
- Avoids Pod recreation (zero downtime for resource changes)
- Works with HPA/VPA (they update Deployment, we patch Pods)

❌ **Drawbacks:**
- **RBAC security concern**: Requires `pods/patch` permission (cannot restrict to resources-only)
- Deployment template shows stale values (confusing for users)
- Annotation-based state management (adds complexity)
- Only works when feature gate enabled (cluster-wide requirement)
- Marginal benefit (resource changes are rare, rollout is usually acceptable)

**RBAC requirements:**
```yaml
# Current (Deployment-only MVP)
- apiGroups: ["apps"]
  resources: ["deployments"]
  verbs: ["get", "list", "watch", "create", "update", "patch"]

# Additional for in-place resize
- apiGroups: [""]
  resources: ["pods"]
  verbs: ["get", "list", "watch", "patch"]
```

Note: Kubernetes RBAC does not support field-level restrictions. The `pods/patch` verb grants permission to modify **any** Pod field, not just resources. This is a security concern.

**Decision:** Defer to future PR after MVP is stable and tested. The RBAC security concern and added complexity outweigh the benefit of avoiding Pod recreation for resource changes.

### Ready Condition Based on Deployment State (LOW PRIORITY)
- Implement optional `Ready` condition (Extended conformance)
- Set `Ready=True` only when Deployment is fully ready (all replicas available)
- Include replica counts in status message
- Note: `Programmed` already indicates config was sent to data plane

## References

- [Gateway API Specification](https://gateway-api.sigs.k8s.io/)
- [Gateway API GEP-1364: Status Conditions](https://gateway-api.sigs.k8s.io/geps/gep-1364/)
- [Kubernetes OwnerReferences](https://kubernetes.io/docs/concepts/overview/working-with-objects/owners-dependents/)
