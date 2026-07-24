# DistributionGroup Controller

## Overview

The DistributionGroup controller manages LoadBalancerEndpointSlices for endpoints on secondary networks, enabling L3/L4 load balancing across multi-network Pods. It bridges Gateway API resources with a custom endpoint discovery CRD, providing per-Gateway endpoint sets for load balancers.

## Architecture

### Core Concepts

**DistributionGroup**: A logical grouping of Pods on a secondary network with a specific distribution strategy (e.g., Maglev consistent hashing).

**Network Context**: The combination of:
- Subnet CIDR (e.g., `192.168.100.0/24`)
- Attachment type (currently only `NAD` for Multus is supported)

**LoadBalancerEndpointSlice**: A custom resource owned by the DG controller that carries per-Gateway endpoint information. Each endpoint bundles all its addresses (dual-stack) in a single entry, with an optional Maglev identifier.

**Maglev ID**: A stable integer (0 to maxEndpoints-1) assigned to each Pod for consistent hashing. Stored directly in the endpoint's `identifier` field. Scoped per DistributionGroup and per Gateway — the same Pod gets the same ID across all IP families within a Gateway, but may have different IDs in different DGs.

**Why shared allocation across families:** The LoadBalancer uses a single NFQLB hash table per DistributionGroup. This hash table maps identifiers to fwmarks, and fwmarks determine routing tables. If different families assigned different IDs to the same Pod, the single hash table would have conflicting entries — one Pod's route would overwrite another's, causing non-deterministic routing and cross-Pod identifier collisions. Shared allocation ensures each identifier maps to exactly one Pod across all IP families, and the LB creates per-family routes under the same fwmark (IPv4 and IPv6 routing tables are independent in Linux). See [#70](https://github.com/Nordix/Meridio-2/issues/70) for full problem description and [#106](https://github.com/Nordix/Meridio-2/issues/106) for related cross-component contract considerations.

### Design Principles

- **No finalizers**: Uses ownerReferences only for automatic garbage collection
  - LoadBalancerEndpointSlices are pure Kubernetes resources (no external cleanup needed)
  - OwnerReferences ensure cleanup even if controller is unavailable
  - Finalizers would risk stuck resources if controller crashes during DG deletion
  - Simpler operational model: no manual intervention needed
- **No empty slices**: Deleted when no endpoints (unlike core K8s EndpointSlice controller)
- **Idempotent reconciliation**: Safe to run multiple times, handles cleanup automatically
- **Configurable max endpoints per slice**: Controlled via `--max-endpoints-per-slice` flag (default 200)
- **CIDR normalization**: All network context CIDRs normalized to canonical form for consistency
- **Single controller per namespace**: No multi-controller conflict detection (deploy one instance per namespace)
- **No shared slices**: Each DG owns distinct slices (even if network context matches) to avoid write conflicts with multiple workers
- **Per-Gateway slices**: Each slice is scoped to a single Gateway via `spec.gatewayRef`
- **Dual-stack in one endpoint**: A Pod's IPv4 and IPv6 addresses are co-located in the same endpoint entry (no cross-object correlation needed by consumers)

### Resource Relationships

```
DistributionGroup
├── spec.selector → Pods (label matching)
├── spec.parentRefs → Gateway (direct reference)
└── (indirect) ← L34Route.backendRefs → DistributionGroup

Gateway
└── spec.infrastructure.parametersRef → GatewayConfiguration

GatewayConfiguration
└── spec.internalSubnets → Network contexts (CIDR + attachment type)

LoadBalancerEndpointSlice (owned by DistributionGroup via ownerReference)
├── metadata.ownerReferences → DistributionGroup (controller=true)
├── labels[app.kubernetes.io/managed-by] → "distributiongroup-controller.meridio-2.nordix.org"
├── labels[meridio-2.nordix.org/distribution-group] → DG name (convenience, truncated to 63 chars)
├── spec.distributionGroupName → DistributionGroup name
├── spec.gatewayRef → Gateway (name + namespace)
├── spec.endpoints[].target → Pod (name + UID)
├── spec.endpoints[].addresses[] → IP + family (dual-stack: up to 2 entries)
├── spec.endpoints[].identifier → Maglev slot index (optional)
└── spec.endpoints[].ready → Pod readiness state
```

**Ownership model:**
- DistributionGroup owns LoadBalancerEndpointSlices via `metadata.ownerReferences` (controller=true)
- Enables automatic garbage collection when DistributionGroup is deleted
- No finalizers needed (Kubernetes handles cleanup)

## Reconciliation Flow

### 1. Fetch DistributionGroup
- Return early if not found (deleted)
- Skip reconciliation if being deleted (ownerReferences handle cleanup)

### 2. List Matching Pods
- Apply `spec.selector` label matching
- Filter to `Running` phase only (excludes Pending/Succeeded/Failed)
- Early exit if no Pods → delete all owned slices
- Note: Pod Readiness is checked later when creating endpoints

### 3. Discover Referenced Gateways
**Direct references:**
- `DistributionGroup.spec.parentRefs` → Gateway

**Indirect references:**
- List L34Routes with `backendRefs` pointing to this DG
- Extract Gateways from L34Route's `parentRefs`

### 4. Filter Accepted Gateways
Only process Gateways with `Accepted=True` condition set by the Gateway controller. This avoids:
- Watching GatewayClass resources
- Resolving `gatewayClassName` references
- Checking `controllerName` matches

**Why check Accepted condition:**
- Gateway controller sets `Accepted=True` when Gateway is valid and managed by our controller
- Per GEP-1364: `Accepted=True` means Gateway is semantically/syntactically valid and will produce data plane config
- Filtering by `Accepted=True` ensures we only process Gateways that:
  - Have valid GatewayClass reference
  - Have valid GatewayConfiguration (mandatory parametersRef)
  - Are managed by our controller (not another implementation)
- Gateways without `Accepted=True` are ignored (no network context extracted, no slices created)

### 5. Enforce Single-Gateway Restriction
If more than one accepted Gateway references the DG (directly or via L34Routes):
- Set `Ready=False` with reason `MultipleGateways`
- Skip reconciliation (existing slices are preserved/frozen)
- The operator must resolve the conflict by removing one Gateway's reference

### 6. Extract Network Contexts (per Gateway)
For each accepted Gateway:
- Fetch referenced GatewayConfiguration
- Extract subnet CIDRs and attachment types from `spec.internalSubnets`
- Normalize CIDRs to canonical form (e.g., `192.168.1.5/24` → `192.168.1.0/24`)
- Group results per Gateway (namespaced name) to scope Maglev allocation per Gateway

### 7. Scrape Pods for Gateway
For each Pod, across all configured subnets within the Gateway:
- Extract the Pod's secondary IP in each subnet via Multus `k8s.v1.cni.cncf.io/network-status` annotation
- A Pod contributes at most one IPv4 and one IPv6 address (one per configured subnet/family)
- Determine IP family using `net.ParseIP().To4() == nil`
- Sort addresses by family (IPv4 before IPv6) for deterministic ordering
- Skip Pods without any matching IPs
- No gatekeeping: Pods are not required to have IPs in all configured subnets.
  A Pod with only an IPv4 address gets included with one address entry.

**Why one address per family per Pod:**
Each endpoint occupies a single slot in the distribution algorithm. Multiple addresses of the same
family on the same Pod would not increase weight or resilience (same slot, same failure domain),
and cannot be used for weighted load balancing. This constraint is reflected in the API
(`addresses` field: MaxItems=2, listMapKey=family).

### 8. Assign Maglev IDs (if Type=Maglev)
**Per Gateway, across all Pods with addresses:**
- Extract existing Pod UID→ID mappings from current LoadBalancerEndpointSlices for this Gateway
- Preserve existing assignments (stability)
- Assign new IDs from available pool (0 to `maxEndpoints-1`)
- Sort new Pods by CreationTimestamp (deterministic assignment)
- Enforce capacity limit: exclude Pods beyond `maxEndpoints`
- No gatekeeping: a Pod that was scraped with only one IP family (e.g., IPv4 only) still
  receives a Maglev ID. Dual-stack presence is not a prerequisite for ID assignment.

**Maglev ID Scoping:**

Maglev IDs are scoped **per DistributionGroup** and **per Gateway**. ID assignment operates at
the Pod level and is independent of IP families — a Pod gets an ID as long as it has at least
one address in any of the Gateway's configured subnets.

- **Same Pod, different DistributionGroups**: Pod-A might be ID `3` in DG-1 and ID `7` in DG-2

**Immutability enforcement:**

`maxEndpoints` is immutable (enforced via CEL validation). To change capacity, create a new DistributionGroup.

**Why this matters for the LoadBalancer controller:**

The LoadBalancer controller uses **dynamic ID offsets per DistributionGroup** to differentiate target IP routes:
- Each DistributionGroup gets a contiguous fwmark range of size `maxEndpoints`, allocated dynamically starting at offset 5000
- Routes are created with fwmarks: `fwmark = offset + maglev_id`
- When NFQLB marks a packet based on distribution decision, the fwmark determines which route (and thus which endpoint) receives the packet
- Changing `maxEndpoints` would:
  - Cause Maglev hash table reshuffle, potentially reassigning IDs for many endpoints
  - Risk fwmark collisions with other DistributionGroups if the range grows into a neighbor's allocation
  - Require NFQLB shared memory reinitialization (different M value)
  - Break active connections for this DG

### 9. Build LoadBalancerEndpointSlices
**Per Gateway:**
- Build endpoint entries: Pod target (name + UID), addresses (dual-stack), identifier (Maglev), ready state (based on Pod readiness)
- Preserve existing slice structure (endpoints stay in their original slice when possible)
- Fill remaining capacity in existing slices with new endpoints
- Compact: move endpoints from later slices into earlier slices with free capacity to avoid fragmentation after scale-in
- Remove empty slices after compaction
- Build new slices for overflow endpoints (split at `MaxEndpointsPerSlice` boundary)
- Set labels:
  - `app.kubernetes.io/managed-by: distributiongroup-controller.meridio-2.nordix.org`
  - `meridio-2.nordix.org/distribution-group: <dg-name>` (truncated to 63 chars if needed)
- Set spec fields: `distributionGroupName`, `gatewayRef` (name + namespace), `endpoints`

**Distribution-group label:**
The `meridio-2.nordix.org/distribution-group` label exists solely for `kubectl` convenience
(e.g., `kubectl get loadbalancerendpointslices -l meridio-2.nordix.org/distribution-group=test-dg`). It MUST NOT
be used for controller logic — the controller uses ownerReferences for slice discovery and
`spec.distributionGroupName` for the authoritative DG reference. The label value is truncated
to 63 characters (Kubernetes label value limit) when the DG name exceeds this length.

**Slice naming:**
New slices are named `<dg-name>-<hash>-<index>` where the hash is a 16-character FNV-64a hex
digest of the full identity (`dgName/gwNamespace/gwName`). This ensures:
- Deterministic names across reconciles (no randomness)
- Different Gateways (including same name in different namespaces) produce different slice names
- The DG name remains the human-readable prefix for `kubectl` output
- Names stay within the 253-char Kubernetes limit (DG name is truncated if necessary)
- The DG name is included in the hash input to avoid truncation-induced collisions when two DGs
  share a long name prefix and target the same Gateway

Example: `test-dg-a1b2c3d4e5f6g7h8-0`

**MaxEndpointsPerSlice enforcement:**
The limit is only enforced when filling remaining capacity into existing slices or creating new slices.
Existing slices that already exceed the limit (e.g., after `--max-endpoints-per-slice` is lowered)
retain all their endpoints — the controller never truncates or splits an oversized slice. Such slices
gradually shrink as Pods scale down naturally. This avoids unnecessary endpoint churn on a
configuration change.

**Pod readiness logic:**
- Checks `PodReady` condition (all containers ready + readiness probes pass)
- Returns false if Pod is being deleted (`DeletionTimestamp != nil`)
- Matches Kubernetes core EndpointSlice controller behavior
- Ensures traffic only goes to fully ready Pods

### 10. Reconcile LoadBalancerEndpointSlices
- Create new slices (with ownerReference set)
- Update existing slices if endpoints/labels changed (semantic equality check)
- Delete orphaned slices
- **Delete empty slices** (unlike Kubernetes core EndpointSlice controller)

**Why delete empty slices:**
- Kubernetes core controller keeps 1 empty slice per Service for faster endpoint addition
- Our controller manages secondary networks with dynamic attachment
- Empty slices provide no value (no "warm cache" benefit for secondary networks)
- Cleaner resource model: no slices = no endpoints = `Ready=False` status

**Why no strict managed-by filtering:**
- Always use ownerReference-based filtering (in-memory), never filter by `managed-by` label at API level
- API-level filtering might create orphans when controller name changes:
  - Old slices with different `managed-by` label become invisible to new controller
  - New controller might try to create slices with same names
  - Create fails with "already exists" error, orphaning the slices
- OwnerReference filtering allows controller to see all owned slices and update their `managed-by` label
- Trade-off: Slightly higher memory usage vs operational simplicity

### 11. Update DistributionGroup Status
**Ready condition:**
- `True` if LoadBalancerEndpointSlices with endpoints exist
- `False` if no endpoints available, with specific reason:
  - "No Pods match selector"
  - "No Gateways reference this DistributionGroup..."
  - "No accepted Gateways found (Gateways may not exist or lack Accepted=True status condition)"
  - "No network context available..."
  - "No endpoints available" (default - Pods have no secondary IPs)
  - "DistributionGroup is referenced by multiple Gateways..." (reason: `MultipleGateways`)

**CapacityExceeded condition (Maglev only):**
- `True` if Pods were excluded due to capacity limits
- Message includes statistics (e.g., `"5/37 pods excluded (32 capacity)"`)

**Conflict handling:**
- Status updates may conflict during concurrent reconciles (`.Owns()` watch)
- Conflicts trigger silent requeue (idiomatic Kubernetes pattern)

## Watch Triggers

The controller reconciles when:

| Resource | Trigger | Mapper Function |
|----------|---------|-----------------|
| DistributionGroup | Create/Update/Delete | Direct (`.For()`) |
| LoadBalancerEndpointSlice | Create/Update/Delete | Owned (`.Owns()`) |
| Pod | Create/Update/Delete | Label selector match |
| Gateway | Create/Update/Delete | Referenced in parentRefs or L34Routes |
| L34Route | Create/Update/Delete | BackendRef points to DG |
| GatewayConfiguration | Create/Update/Delete | Referenced by Gateway |

**Note:** Gateway watch includes early filtering - only Gateways with `Accepted=True` trigger reconciliation.

### Why Watch GatewayConfiguration Directly?

**The GatewayConfiguration watch is necessary for performance**, not redundant with the Gateway watch.

**Scenario 1: Valid GatewayConfiguration update (valid → valid)**
```yaml
# User adds IPv6 network to existing config
GatewayConfiguration:
  spec:
    internalSubnets:
    - cidr: "192.168.1.0/24"
    - cidr: "2001:db8::/64"  # IPv6 added
```

**Without GatewayConfiguration watch:**

The Gateway controller watches GatewayConfiguration and reconciles when it changes. However,
if the config remains valid, the Gateway stays `Accepted=True` with no status change — meaning
no Gateway object event is emitted.

From the DG controller's perspective:
1. GatewayConfiguration updated (valid → valid)
2. Gateway controller reconciles, confirms Gateway is still valid — no status write
3. No Gateway event reaches the DG controller
4. DG controller is never triggered
5. DG remains stale until an unrelated event (Pod change, periodic resync) causes reconciliation

**With GatewayConfiguration watch:**
1. GatewayConfiguration updated
2. DG controller's GatewayConfiguration mapper triggers directly
3. DG reconciles, fetches the updated config, discovers new network

**Scenario 2: Invalid GatewayConfiguration update (valid → invalid) - Race Condition**
```yaml
# User breaks config
GatewayConfiguration:
  spec:
    internalSubnets: []  # Empty - invalid!
```

**Race condition:**
1. GatewayConfiguration updated (becomes invalid)
2. **DG GatewayConfiguration mapper triggers** (sees Gateway still has `Accepted=True`)
3. **DG reconciles with invalid config** ❌
4. Gateway controller reconciles (later)
5. Gateway sets `Accepted=False`
6. DG Gateway mapper triggers, reconciles again

**Impact:**
- DG might process invalid GatewayConfiguration briefly
- `getNetworkContexts()` returns empty map (no valid CIDRs)
- DG deletes all LoadBalancerEndpointSlices (no network contexts)
- Gateway watch provides eventual consistency
- **Result: Temporary disruption, but eventually consistent** ⚠️

**Scenario 3: Fixed GatewayConfiguration (invalid → valid) - Race Condition**
```yaml
# User fixes config
GatewayConfiguration:
  spec:
    internalSubnets:
    - cidr: "192.168.1.0/24"  # Fixed!
```

**Race condition:**
1. GatewayConfiguration updated (becomes valid)
2. **DG GatewayConfiguration mapper triggers** (sees Gateway still has `Accepted=False`)
3. **DG skips reconciliation** (Gateway not accepted yet) ❌
4. Gateway controller reconciles (later)
5. Gateway sets `Accepted=True`
6. **DG Gateway mapper triggers** (Gateway status changed) ✅
7. DG processes valid config, creates LoadBalancerEndpointSlices

**Impact:**
- DG GatewayConfiguration mapper fires too early (before Gateway validates)
- DG skips processing (Gateway still shows `Accepted=False`)
- Gateway watch provides eventual consistency (triggers when `Accepted=True` is set)
- **Result: Slight delay, but eventually consistent** ✅

**Trade-off summary:**
- ✅ Fast response to valid config changes (Scenario 1)
- ✅ No missed events
- ⚠️ Brief disruption possible (Scenario 2: invalid config processed before Gateway marks it)
- ⚠️ Slight delay possible (Scenario 3: GatewayConfiguration mapper fires before Gateway validates)
- ✅ Eventual consistency guaranteed via Gateway watch

**Conclusion: GatewayConfiguration watch is mandatory** - Without it, valid config updates (Scenario 1) would be missed entirely since Gateway status doesn't change. The race conditions in Scenarios 2 and 3 are acceptable trade-offs for correctness and responsiveness.

## Maglev Implementation

### ID Assignment Algorithm

1. **Preserve existing assignments** from current LoadBalancerEndpointSlices (Pod UID → `identifier`)
2. **Build available ID pool** (0 to maxEndpoints-1, excluding used IDs)
3. **Sort new Pods** by CreationTimestamp (oldest first), tiebreak by namespace/name
4. **Assign sequentially** from available pool
5. **Enforce capacity**: Stop at maxEndpoints, exclude remaining Pods

### Capacity Enforcement

**Example:** maxEndpoints=32, 35 Pods exist
- 32 oldest Pods get IDs (0-31)
- 3 newest Pods excluded from LoadBalancerEndpointSlices
- Status condition reports: `CapacityExceeded=True`

**Why enforce capacity:**
- Maglev hash table size is fixed
- Exceeding capacity breaks consistent hashing guarantees
- Excluded Pods won't receive traffic (intentional)

### Stability Guarantees

- Pod keeps same Maglev ID across reconciliations (unless deleted or removed from DG)
- ID becomes available for reassignment when:
  - Pod is deleted
  - Pod no longer matches DG selector
  - Pod loses all its secondary network IPs
- New Pods receive IDs deterministically (CreationTimestamp order, oldest first)

## Edge Cases

### No Matching Pods
- Delete all owned LoadBalancerEndpointSlices
- Set `Ready=False` status

### Gateway Not Accepted
- Skip Gateway (no network context extracted)
- Effectively ignores the DG until Gateway is accepted

### Invalid CIDR in GatewayConfiguration
- Log warning and skip that CIDR
- Continue processing other valid CIDRs

### Pod Without Secondary IP
- Skip Pod (not included in LoadBalancerEndpointSlices)
- Common during Pod startup or network attachment failures

### Concurrent Reconciles
- Status update conflicts handled gracefully
- Automatic requeue with fresh resourceVersion

### LoadBalancerEndpointSlice Modified Externally
- Detected via semantic equality check
- Overwritten on next reconcile (controller owns the resource)

### Controller Lifecycle

**User deletes DistributionGroup:**
- OwnerReferences trigger automatic LoadBalancerEndpointSlice deletion via Kubernetes GC
- Works even if controller is unavailable (crashed, deleted, or scaled to zero)
- No risk of stuck resources in Terminating state

**Why no finalizers:**
- LoadBalancerEndpointSlices don't require external cleanup (cloud LBs, DNS, etc.)
- Finalizers create operational risk:
  - DG stuck in Terminating if controller unavailable during deletion
  - Requires manual finalizer removal if controller permanently gone
  - Adds complexity without benefit for pure Kubernetes resources
- OwnerReferences provide sufficient cleanup guarantees

## Performance Considerations

### Early Exits
- Skip reconciliation if DG is being deleted
- Return early if no Pods match selector
- Filter Gateways before fetching GatewayConfigurations

## Testing

### Test Categories
- Maglev ID assignment and capacity enforcement
- CIDR normalization
- Slice structure preservation and compaction
- Pod IP scraping (NAD annotations)
- Status condition building
- Dual-stack endpoint assembly
- Idempotency and ID stability across reconciles

### Manual Testing in Cluster

Deploy test resources (dual-stack):

```bash
cat <<'EOF' | kubectl apply -f -
---
apiVersion: v1
kind: Namespace
metadata:
  name: meridio-2
---
apiVersion: gateway.networking.k8s.io/v1
kind: GatewayClass
metadata:
  name: meridio-2
spec:
  controllerName: meridio-2.nordix.org/gateway-controller
---
apiVersion: k8s.cni.cncf.io/v1
kind: NetworkAttachmentDefinition
metadata:
  name: test-net
  namespace: meridio-2
spec:
  config: |
    {
      "cniVersion": "0.3.1",
      "name": "test-net",
      "type": "macvlan",
      "master": "eth0",
      "mode": "bridge",
      "ipam": {
        "type": "whereabouts",
        "ipRanges": [
          {
            "range": "192.168.100.0/24"
          },
          {
            "range": "2001:db8:100::/64"
          }
        ]
      }
    }
---
apiVersion: meridio-2.nordix.org/v1alpha1
kind: GatewayConfiguration
metadata:
  name: test-gwconfig
  namespace: meridio-2
spec:
  networkAttachments: []
  internalSubnets:
    - cidr: "192.168.100.0/24"
    - cidr: "2001:db8:100::/64"
  horizontalScaling:
    replicas: 1
    enforceReplicas: false  # Not needed for single-replica test setup
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
    - name: all
      protocol: ALL
      port: 0
---
apiVersion: meridio-2.nordix.org/v1alpha1
kind: DistributionGroup
metadata:
  name: test-dg
  namespace: meridio-2
spec:
  type: Maglev
  selector:
    matchLabels:
      app: test-backend
  maglev:
    maxEndpoints: 32
  parentRefs:
    - name: test-gateway
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: test-backend
  namespace: meridio-2
spec:
  replicas: 3
  selector:
    matchLabels:
      app: test-backend
  template:
    metadata:
      labels:
        app: test-backend
      annotations:
        k8s.v1.cni.cncf.io/networks: test-net
    spec:
      containers:
        - name: app
          image: nginx:alpine
          ports:
            - containerPort: 80
EOF
```

Mark Gateway as accepted (only needed when testing DG controller standalone, without the Gateway controller running):

```bash
kubectl patch gateway test-gateway -n meridio-2 --type=merge --subresource=status --patch '
{
  "status": {
    "conditions": [
      {
        "type": "Accepted",
        "status": "True",
        "reason": "Accepted",
        "message": "Gateway accepted by meridio-2.nordix.org/gateway-controller",
        "lastTransitionTime": "'$(date -u +"%Y-%m-%dT%H:%M:%SZ")'"
      }
    ]
  }
}'
```

Verify LoadBalancerEndpointSlices created:

```bash
# Check slices for a specific DG
kubectl get loadbalancerendpointslices -n meridio-2 -l meridio-2.nordix.org/distribution-group=test-dg

# Verify Maglev IDs and dual-stack addresses
kubectl get loadbalancerendpointslices -n meridio-2 -l meridio-2.nordix.org/distribution-group=test-dg -o json | \
  jq -r '.items[] | 
    "Slice: \(.metadata.name) (DG: \(.spec.distributionGroupName), GW: \(.spec.gatewayRef.name))",
    (.spec.endpoints[] | "  \(.target.name) [ID: \(.identifier // "none")] ready=\(.ready) addresses=\([.addresses[] | "\(.family):\(.ip)"] | join(", "))")'

# Check DistributionGroup status
kubectl get distributiongroup test-dg -n meridio-2 -o yaml
```

Test capacity enforcement (scale beyond maxEndpoints):

```bash
kubectl scale deployment test-backend -n meridio-2 --replicas=35

# Verify CapacityExceeded condition (once additional replicas are ready)
kubectl get distributiongroup test-dg -n meridio-2 -o jsonpath='{.status.conditions[?(@.type=="CapacityExceeded")]}'

# Count endpoints (should be 32, not 35)
kubectl get loadbalancerendpointslices -n meridio-2 -l meridio-2.nordix.org/distribution-group=test-dg -o json | \
  jq '[.items[].spec.endpoints | length] | add'
```

Test capacity recovery (scale back within limits):

```bash
kubectl scale deployment test-backend -n meridio-2 --replicas=4

# CapacityExceeded condition should be removed
kubectl get distributiongroup test-dg -n meridio-2 -o jsonpath='{.status.conditions[*].type}'
```

**Test indirect DG → Gateway relation via L34Route:**

```bash
cat <<'EOF' | kubectl apply -f -
---
apiVersion: meridio-2.nordix.org/v1alpha1
kind: DistributionGroup
metadata:
  name: test-dg-indirect
  namespace: meridio-2
spec:
  type: Maglev
  selector:
    matchLabels:
      app: test-backend
  maglev:
    maxEndpoints: 32
  # No parentRefs — indirect reference via L34Route
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
    - name: test-dg-indirect
      group: meridio-2.nordix.org
      kind: DistributionGroup
  destinationCIDRs:
    - "20.0.0.1/32"
  protocols:
    - TCP
  priority: 1
EOF
```

```bash
# Check LoadBalancerEndpointSlices for the indirect DG
kubectl get lbeslice -n meridio-2 -l meridio-2.nordix.org/distribution-group=test-dg-indirect

# Verify endpoints and Maglev IDs
kubectl get lbeslice -n meridio-2 -l meridio-2.nordix.org/distribution-group=test-dg-indirect -o json | \
  jq -r '.items[] |
    "Slice: \(.metadata.name) (DG: \(.spec.distributionGroupName), GW: \(.spec.gatewayRef.name))",
    (.spec.endpoints[] | "  \(.target.name) [ID: \(.identifier // "none")] ready=\(.ready) addresses=\([.addresses[] | "\(.family):\(.ip)"] | join(", "))")'

# Check DistributionGroup status
kubectl get distributiongroup test-dg-indirect -n meridio-2 -o jsonpath='{.status.conditions}'
```

### Integration Testing
Deploy to cluster and verify:
- LoadBalancerEndpointSlices created with correct gatewayRef and distributionGroupName
- Maglev IDs assigned and stable across reconciles
- Dual-stack addresses co-located in single endpoint entries
- Capacity enforcement with 33+ Pods
- Status conditions reflect actual state

## Configuration

### Controller Flags

**`--namespace`**: Limit to single namespace (empty = all namespaces)

**`--controller-name`**: 
- Used to identify Gateways accepted by this controller
- Should match the GatewayClass.spec.controllerName that the Gateway controller uses
- DG controller checks Gateway status conditions for this controller name (shortcut to avoid watching GatewayClass)

**`--max-endpoints-per-slice`**: Maximum number of endpoints per LoadBalancerEndpointSlice (default: 200)

### Environment Variables
All flags can be set via `MERIDIO_*` environment variables (e.g., `MERIDIO_NAMESPACE`, `MERIDIO_MAX_ENDPOINTS_PER_SLICE`).

## RBAC

The controller manager's RBAC is split into:
- **Role** (namespace-scoped): Pods, Deployments, Gateways, L34Routes, GatewayConfigurations, DistributionGroups, LoadBalancerEndpointSlices, EndpointNetworkConfigurations
- **ClusterRole** (cluster-scoped): GatewayClasses

See `config/rbac/manager-role.yaml` and `config/rbac/manager-clusterrole.yaml`.

The DG controller specifically requires:
- Read: Pods, Gateways, L34Routes, GatewayConfigurations, DistributionGroups
- Read/Write: LoadBalancerEndpointSlices (create, update, delete)
- Write: DistributionGroups/status

## Node Failure Detection

The DG controller does **not** watch Node events or independently detect node failure. When a Node becomes unreachable, the affected Pod's `PodReady` condition remains stale (`True`) because the dead kubelet cannot update it. Endpoint removal and Maglev ID reallocation are deferred until Kubernetes evicts and deletes the Pod.

This is intentional:

1. **Node unreachable ≠ node dead.** A control-plane network partition triggers NotReady, but the Pod may still be running and serving traffic on the data-plane path.
2. **Premature Maglev ID reallocation disrupts active connections.** Revoking an ID reshuffles the hash table, reassigning in-flight connections to a different endpoint — even if the original Pod is still alive.
3. **Kubernetes provides the control mechanism.** Applications set Pod `tolerationSeconds` for `node.kubernetes.io/not-ready` and `node.kubernetes.io/unreachable` taints to control the eviction window (default 300s, reducible to e.g. 30s for faster failover).

Once the Pod is deleted or transitions out of `Running` phase, the DG controller removes it from EndpointSlices and the Maglev ID is freed — this is the safe trigger, as Kubernetes has committed to terminating the Pod.

## Future Enhancements

### Additional Attachment Types
- Future attachment types (e.g., DRA) could be added if needed
- Would require IP scraping logic and attachment-specific handling in `pods.go`

### Additional Distribution Types
- Round-robin (no stable IDs)

### Capacity Management
- Make `maxEndpoints` mutable with controlled migration
- Add metrics for capacity utilization

### Concurrency Tuning
- Add `--distributiongroup-max-concurrent-reconciles` flag
- Default: 1 worker (controller-runtime default)
- Increase for high-churn environments (many DGs/Pods changing frequently)
- Safe: Work queue prevents concurrent reconciles of same DG
- Note: Cannot share LoadBalancerEndpointSlices between DGs (even with matching network context) due to write conflicts

## References

- [Gateway API Specification](https://gateway-api.sigs.k8s.io/)
- [Kubernetes EndpointSlice Controller](https://github.com/kubernetes/kubernetes/tree/master/pkg/controller/endpointslice) (design inspiration)
- [Multus CNI Network Status Annotation](https://github.com/k8snetworkplumbingwg/multus-cni/blob/master/docs/how-to-use.md#network-status-annotation)
- [LoadBalancerEndpointSlice CRD](../../api/v1alpha1/loadbalancerendpointslice_types.go)
