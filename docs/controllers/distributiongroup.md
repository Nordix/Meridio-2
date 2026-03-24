# DistributionGroup Controller

## Overview

The DistributionGroup controller manages EndpointSlices for secondary network endpoints, enabling L3/L4 load balancing across multi-network Pods. It bridges Gateway API resources with Kubernetes EndpointSlices, providing network-aware endpoint discovery for load balancers.

## Architecture

### Core Concepts

**DistributionGroup**: A logical grouping of Pods on a secondary network with a specific distribution strategy (e.g., Maglev consistent hashing).

**Network Context**: The combination of:
- Subnet CIDR (e.g., `192.168.100.0/24`)
- Attachment type (`NAD` for Multus, `DRA` for Dynamic Resource Allocation)

**Maglev ID**: A stable integer (0-31 by default) assigned to each Pod for consistent hashing. Stored in EndpointSlice's `zone` field as `maglev:<id>`. Scoped per DistributionGroup and per network context (a Pod may have different IDs in different DGs or networks).

### Design Principles

- **No finalizers**: Uses ownerReferences only for automatic garbage collection
  - EndpointSlices are pure Kubernetes resources (no external cleanup needed)
  - OwnerReferences ensure cleanup even if controller is unavailable
  - Finalizers would risk stuck resources if controller crashes during DG deletion
  - Simpler operational model: no manual intervention needed
- **No empty EndpointSlices**: Deleted when no endpoints (unlike core K8s controller)
- **Idempotent reconciliation**: Safe to run multiple times, handles cleanup automatically
- **Max 100 endpoints per slice**: Follows Kubernetes EndpointSlice controller pattern
- **CIDR normalization**: All network context CIDRs normalized to canonical form for consistency
- **Single controller per namespace**: No multi-controller conflict detection (deploy one instance per namespace)
- **No shared EndpointSlices**: Each DG owns distinct slices (even if network context matches) to avoid write conflicts with multiple workers

### Resource Relationships

```
DistributionGroup
├── spec.selector → Pods (label matching)
├── spec.parentRefs → Gateway (direct reference)
└── (indirect) ← L34Route.backendRefs → DistributionGroup

Gateway
└── spec.infrastructure.parametersRef → GatewayConfiguration

GatewayConfiguration
└── spec.networkSubnets → Network contexts (CIDR + attachment type)

EndpointSlice (owned by DistributionGroup via ownerReference)
├── metadata.ownerReferences → DistributionGroup (controller=true)
├── labels[managed-by] → "distributiongroup-controller.meridio-2.nordix.org"
├── labels[distribution-group] → DistributionGroup name
├── labels[network-subnet] → CIDR (encoded: "192.168.1.0-24" or "2001_db8__-64")
├── endpoints[].addresses → Secondary network IPs
└── endpoints[].zone → Maglev ID (e.g., "maglev:5")
```

**Ownership model:**
- DistributionGroup owns EndpointSlices via `metadata.ownerReferences` (controller=true)
- Enables automatic garbage collection when DistributionGroup is deleted
- No finalizers needed (Kubernetes handles cleanup)

## Reconciliation Flow

### 1. Fetch DistributionGroup
- Return early if not found (deleted)
- Skip reconciliation if being deleted (ownerReferences handle cleanup)

### 2. List Matching Pods
- Apply `spec.selector` label matching
- Filter to `Running` phase only (excludes Pending/Succeeded/Failed)
- Early exit if no Pods → delete all EndpointSlices
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
- Gateways without `Accepted=True` are ignored (no network context extracted, no EndpointSlices created)

### 5. Extract Network Contexts
For each accepted Gateway:
- Fetch referenced GatewayConfiguration
- Extract subnet CIDRs and attachment types from `spec.networkSubnets`
- Normalize CIDRs to canonical form (e.g., `192.168.1.5/24` → `192.168.1.0/24`)

### 6. Filter Pods by Network
For each network context:
- Scrape secondary IP from Pod based on attachment type:
  - **NAD**: Parse Multus `k8s.v1.cni.cncf.io/network-status` annotation
  - **DRA**: (Future implementation)
- Return first matching IP in the target subnet (NAD only)
- Skip Pods without IPs in the target subnet
- Skip primary interface IPs (only secondary networks)

### 7. Assign Maglev IDs (if Type=Maglev)
**Per network context (CIDR) within this DistributionGroup:**
- Extract existing Pod→ID mappings from current EndpointSlices for this network
- Preserve existing assignments (stability)
- Assign new IDs from available pool (0 to `maxEndpoints-1`)
- Sort new Pods by CreationTimestamp (deterministic assignment)
- Enforce capacity limit: exclude Pods beyond `maxEndpoints`

**Maglev ID Scoping:**

Maglev IDs are scoped **per DistributionGroup** and **per network context**. A Pod may have different IDs in different scenarios:

- **Same Pod, different networks**: Pod-A might be ID `5` in `192.168.1.0/24` (IPv4) and ID `12` in `2001:db8::/64` (IPv6)
- **Same Pod, different DistributionGroups**: Pod-A might be ID `3` in DG-1 and ID `7` in DG-2 (even for the same network)

**Why this matters for the LoadBalancer controller:**

The LoadBalancer controller uses **ID offsets per DistributionGroup** to differentiate target IP routes:
- Each DistributionGroup gets a unique ID offset (e.g., DG-1: offset 0, DG-2: offset 1024)
- Routes are created with fwmarks: `fwmark = offset + maglev_id`
- When NFQLB marks a packet based on distribution decision, the fwmark determines which route (and thus which endpoint) receives the packet
- Changing `maxEndpoints` would:
  - Cause Maglev hash table reshuffle, potentially reassigning IDs for many endpoints
  - Risk fwmark collisions with other DistributionGroups (e.g., if DG-1 grows from 32 to 128 endpoints, fwmarks 100-127 might collide with DG-2's range)
  - Require offset reallocation for all DGs (unless fixed spacing like 1024 is used)
  - Break active connections for this DG and potentially others

**Immutability enforcement:**

`maxEndpoints` is immutable (enforced via CEL validation). To change capacity, create a new DistributionGroup.

### 8. Create EndpointSlices
**Per network context:**
- Group endpoints by network (one or more slices per CIDR)
- Preserve existing slice structure (minimize churn)
- Split into multiple slices if > 100 endpoints
- Set labels:
  - `endpointslice.kubernetes.io/managed-by: distributiongroup-controller.meridio-2.nordix.org`
  - `meridio-2.nordix.org/distribution-group: <dg-name>`
  - `meridio-2.nordix.org/network-subnet: <encoded-cidr>` (IPv4: `192.168.1.0-24`, IPv6: `2001_db8__-64`)
- Set `addressType` based on CIDR (IPv4 vs IPv6)
- Set `zone` field for Maglev endpoints (e.g., `maglev:5`)
- Set endpoint `Ready` condition based on Pod readiness (not just Phase)

**Pod readiness logic:**
- Checks `PodReady` condition (all containers ready + readiness probes pass)
- Returns false if Pod is being deleted (`DeletionTimestamp != nil`)
- Matches Kubernetes core EndpointSlice controller behavior
- Ensures traffic only goes to fully ready Pods

### 9. Reconcile EndpointSlices
- Create new slices
- Update existing slices if endpoints/labels changed (semantic equality check)
- Delete orphaned slices
- **Delete empty slices** (unlike Kubernetes core controller)

**Why delete empty slices:**
- Kubernetes core controller keeps 1 empty slice per Service for faster endpoint addition
- Our controller manages secondary networks with dynamic attachment
- Empty slices provide no value (no "warm cache" benefit for secondary networks)
- Cleaner resource model: no slices = no endpoints = `Ready=False` status

**Why no strict managed-by filtering:**
- Always use ownerReference-based filtering (in-memory), never filter by `managed-by` label at API level
- API-level filtering might create name collisions when controller name changes:
  - Old slices with different `managed-by` label become invisible to new controller
  - New controller might try to create slices with same names (based on CIDR hash)
  - Create fails with "already exists" error, orphaning the slices
- OwnerReference filtering allows controller to see all owned slices and update their `managed-by` label
- **Multi-controller scenario:** Strict mode wouldn't prevent conflicts anyway:
  - Multiple controllers with different names would still create slices with identical names (CIDR-based)
  - Name collisions occur regardless of `managed-by` filtering
  - Solution: Deploy one controller per namespace (documented constraint)
- Trade-off: Slightly higher memory usage vs operational simplicity

### 10. Update DistributionGroup Status
**Ready condition:**
- `True` if EndpointSlices exist
- `False` if no endpoints available, with specific reason:
  - "No Pods match selector"
  - "No Gateways reference this DistributionGroup..."
  - "No accepted Gateways found (Gateways may not exist or lack Accepted=True status condition)"
  - "No network context available..."
  - "No endpoints available" (default - Pods have no secondary IPs)

**CapacityExceeded condition (Maglev only):**
- `True` if Pods were excluded due to capacity limits
- Message includes per-network statistics

**Conflict handling:**
- Status updates may conflict during concurrent reconciles (`.Owns()` watch)
- Conflicts trigger silent requeue (idiomatic Kubernetes pattern)

## Watch Triggers

The controller reconciles when:

| Resource | Trigger | Mapper Function |
|----------|---------|-----------------|
| DistributionGroup | Create/Update/Delete | Direct (`.For()`) |
| EndpointSlice | Create/Update/Delete | Owned (`.Owns()`) |
| Pod | Create/Update/Delete | Label selector match |
| Gateway | Create/Update/Delete | Referenced in parentRefs or L34Routes |
| L34Route | Create/Update/Delete | BackendRef points to DG |
| GatewayConfiguration | Create/Update/Delete | Referenced by Gateway |
| Node | Create/Update/Delete | Topology hints (optional) |

**Note:** Gateway watch includes early filtering - only Gateways with `Accepted=True` trigger reconciliation.

### Why Watch GatewayConfiguration Directly?

**The GatewayConfiguration watch is necessary for performance**, not redundant with the Gateway watch.

**Scenario 1: Valid GatewayConfiguration update (valid → valid)**
```yaml
# User adds IPv6 network to existing config
GatewayConfiguration:
  spec:
    networkSubnets:
    - cidrs: ["192.168.1.0/24", "2001:db8::/64"]  # IPv6 added
```

**Without GatewayConfiguration watch:**
1. GatewayConfiguration updated (valid → valid)
2. Gateway controller reconciles
3. Gateway stays `Accepted=True` (no status change!)
4. Gateway watch doesn't trigger (no event)
5. **DG never learns about new network** ❌

**With GatewayConfiguration watch:**
1. GatewayConfiguration updated
2. DG GatewayConfiguration mapper triggers immediately ✅
3. DG reconciles, discovers new network
4. Creates EndpointSlices for IPv6

**Scenario 2: Invalid GatewayConfiguration update (valid → invalid) - Race Condition**
```yaml
# User breaks config
GatewayConfiguration:
  spec:
    networkSubnets: []  # Empty - invalid!
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
- DG deletes all EndpointSlices (no network contexts)
- Gateway watch provides eventual consistency
- **Result: Temporary disruption, but eventually consistent** ⚠️

**Scenario 3: Fixed GatewayConfiguration (invalid → valid) - Race Condition**
```yaml
# User fixes config
GatewayConfiguration:
  spec:
    networkSubnets:
    - cidrs: ["192.168.1.0/24"]  # Fixed!
```

**Race condition:**
1. GatewayConfiguration updated (becomes valid)
2. **DG GatewayConfiguration mapper triggers** (sees Gateway still has `Accepted=False`)
3. **DG skips reconciliation** (Gateway not accepted yet) ❌
4. Gateway controller reconciles (later)
5. Gateway sets `Accepted=True`
6. **DG Gateway mapper triggers** (Gateway status changed) ✅
7. DG processes valid config, creates EndpointSlices

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

1. **Preserve existing assignments** from current EndpointSlices
2. **Build available ID pool** (0 to maxEndpoints-1, excluding used IDs)
3. **Sort new Pods** by CreationTimestamp (oldest first), tiebreak by namespace/name
4. **Assign sequentially** from available pool
5. **Enforce capacity**: Stop at maxEndpoints, exclude remaining Pods

### Capacity Enforcement

**Example:** maxEndpoints=32, 35 Pods exist
- 32 oldest Pods get IDs (0-31)
- 3 newest Pods excluded from EndpointSlices
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
  - Pod loses its secondary network IP
- New Pods receive IDs deterministically (CreationTimestamp order, oldest first)

## Edge Cases

### No Matching Pods
- Delete all owned EndpointSlices
- Set `Ready=False` status

### Gateway Not Accepted
- Skip Gateway (no network context extracted)
- Effectively ignores the DG until Gateway is accepted

### Invalid CIDR in GatewayConfiguration
- Log warning and skip that CIDR
- Continue processing other valid CIDRs

### Pod Without Secondary IP
- Skip Pod (not included in EndpointSlices)
- Common during Pod startup or network attachment failures

### Concurrent Reconciles
- Status update conflicts handled gracefully
- Automatic requeue with fresh resourceVersion

### EndpointSlice Modified Externally
- Detected via semantic equality check
- Overwritten on next reconcile (controller owns the resource)

### Controller Lifecycle

**User deletes DistributionGroup:**
- OwnerReferences trigger automatic EndpointSlice deletion via Kubernetes GC
- Works even if controller is unavailable (crashed, deleted, or scaled to zero)
- No risk of stuck resources in Terminating state

**Why no finalizers:**
- EndpointSlices don't require external cleanup (cloud LBs, DNS, etc.)
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
- CIDR encoding/normalization
- EndpointSlice structure preservation
- Pod IP scraping (NAD annotations)
- Status condition building
- BackendRef parsing

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
  networkSubnets:
    - attachmentType: NAD
      cidrs:
        - "192.168.100.0/24"
        - "2001:db8:100::/64"
  horizontalScaling:
    replicas: 1
    enforceReplicas: false
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

Mark Gateway as accepted (simulates Gateway controller):

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

Verify EndpointSlices created:

```bash
# Check EndpointSlices
kubectl get endpointslices -n meridio-2 -l meridio-2.nordix.org/distribution-group=test-dg

# Verify Maglev IDs assigned
kubectl get endpointslices -n meridio-2 -l meridio-2.nordix.org/distribution-group=test-dg -o json | \
  jq -r '.items | group_by(.metadata.labels."meridio-2.nordix.org/network-subnet") | 
    .[] | 
    "Network: \(.[0].metadata.labels."meridio-2.nordix.org/network-subnet") (AddressType: \(.[0].addressType))",
    (.[].endpoints[] | "  \(.addresses) -> \(.zone // "no-zone")")'


# Check DistributionGroup status
kubectl get distributiongroup test-dg -n meridio-2 -o yaml
```

Test capacity enforcement (scale beyond maxEndpoints):

```bash
kubectl scale deployment test-backend -n meridio-2 --replicas=35

# Verify CapacityExceeded condition (once additional replicas are ready)
kubectl get distributiongroup test-dg -n meridio-2 -o jsonpath='{.status.conditions[?(@.type=="CapacityExceeded")]}'

# Count endpoints in EndpointSlices (should be 32, not 35)
kubectl get endpointslices -n meridio-2 -l meridio-2.nordix.org/distribution-group=test-dg -o json | \
  jq -r '.items | group_by(.metadata.labels."meridio-2.nordix.org/network-subnet") | 
    .[] | 
    "\(.[0].metadata.labels."meridio-2.nordix.org/network-subnet"): \([.[].endpoints | length] | add) endpoints"'
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
  # No parentRefs - indirect reference via L34Route
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
# Check EndpointSlices for test-dg-indirect
kubectl get endpointslices -n meridio-2 -l meridio-2.nordix.org/distribution-group=test-dg-indirect

# Verify Maglev IDs assigned
kubectl get endpointslices -n meridio-2 -l meridio-2.nordix.org/distribution-group=test-dg-indirect -o json | \
  jq -r '.items | group_by(.metadata.labels."meridio-2.nordix.org/network-subnet") | 
    .[] | 
    "Network: \(.[0].metadata.labels."meridio-2.nordix.org/network-subnet") (AddressType: \(.[0].addressType))",
    (.[].endpoints[] | "  \(.addresses) -> \(.zone // "no-zone")")'

# Check DistributionGroup status
kubectl get distributiongroup test-dg-indirect -n meridio-2 -o yaml

# Count endpoints in EndpointSlices (should be 32, not 35)
kubectl get endpointslices -n meridio-2 -l meridio-2.nordix.org/distribution-group=test-dg-indirect -o json | \
  jq -r '.items | group_by(.metadata.labels."meridio-2.nordix.org/network-subnet") | 
    .[] | 
    "\(.[0].metadata.labels."meridio-2.nordix.org/network-subnet"): \([.[].endpoints | length] | add) endpoints"'
```

Verify test-dg and test-dg-indirect have separate EndpointSlices:

```bash
# List all EndpointSlices with their DG labels
kubectl get endpointslices -n meridio-2 -l meridio-2.nordix.org/distribution-group -o custom-columns=NAME:.metadata.name,DG:.metadata.labels.meridio-2\\.nordix\\.org/distribution-group

# Verify no overlap (each DG owns different slices)
kubectl get endpointslices -n meridio-2 -l meridio-2.nordix.org/distribution-group=test-dg -o name
kubectl get endpointslices -n meridio-2 -l meridio-2.nordix.org/distribution-group=test-dg-indirect -o name
```

Test capacity recovery (scale back within limits):

```bash
kubectl scale deployment test-backend -n meridio-2 --replicas=4

# Check all conditions (should only show: Ready)
kubectl get distributiongroup test-dg-indirect -n meridio-2 -o jsonpath='{.status.conditions[*].type}'

# Verify endpoint count matches scaling target (should return: 4)
kubectl get endpointslices -n meridio-2 -l meridio-2.nordix.org/distribution-group=test-dg-indirect -o json | \
  jq -r '.items | group_by(.metadata.labels."meridio-2.nordix.org/network-subnet") | 
    .[] | 
    "\(.[0].metadata.labels."meridio-2.nordix.org/network-subnet"): \([.[].endpoints | length] | add) endpoints"'
```

### Integration Testing
Deploy to cluster and verify:
- EndpointSlices created with correct labels
- Maglev IDs assigned and stable across reconciles
- Capacity enforcement with 33+ Pods
- Status conditions reflect actual state

## Configuration

### Controller Flags

**`--namespace`**: Limit to single namespace (empty = all namespaces)

**`--controller-name`**: 
- Used to identify Gateways accepted by this controller
- Should match the GatewayClass.spec.controllerName that the Gateway controller uses
- DG controller checks Gateway status conditions for this controller name (shortcut to avoid watching GatewayClass)

**`--enable-topology-hints`**: Enable Node watching for faster endpoint removal when Nodes fail

### Environment Variables
All flags can be set via `MERIDIO_*` environment variables (e.g., `MERIDIO_NAMESPACE`).

## Future Enhancements

### Node Availability Monitoring
- Watch Node resources for NotReady conditions
- Trigger immediate reconciliation when Nodes fail
- Remove endpoints faster than waiting for Pod deletion
- Requires `--enable-topology-hints` flag

### DRA Support
- Implement IP scraping for Dynamic Resource Allocation
- Add DRA-specific attachment logic in `pods.go`

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
- Note: Cannot share EndpointSlices between DGs (even with matching network context) due to write conflicts

### RBAC Refinement
- Split into namespace-scoped Role and cluster-scoped ClusterRole
- **Role** (namespace-scoped): distributiongroups, endpointslices, pods, l34routes, gatewayconfigurations
- **ClusterRole** (cluster-scoped): gateways, nodes
- Enables principle of least privilege for namespace-scoped deployments
- Current implementation uses ClusterRole for all resources (simplicity for alpha)

## References

- [Gateway API Specification](https://gateway-api.sigs.k8s.io/)
- [Kubernetes EndpointSlice Controller](https://github.com/kubernetes/kubernetes/tree/master/pkg/controller/endpointslice)
- [Multus CNI Network Status Annotation](https://github.com/k8snetworkplumbingwg/multus-cni/blob/master/docs/how-to-use.md#network-status-annotation)
