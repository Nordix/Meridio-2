# LoadBalancer Controller

## Overview

The LoadBalancer controller manages NFQLB (nfqueue-loadbalancer) instances for traffic distribution within a Gateway. It watches **DistributionGroup** as its primary resource — mirroring the Kubernetes Service/kube-proxy pattern — and creates corresponding NFQLB shared-memory instances, configures policy routing, nftables rules, and readiness signaling.

The controller runs inside the `stateless-load-balancer` container in each LB Pod (one Pod per Gateway).

## Architecture

### Deployment Model

**One SLLB Pod per Gateway**, containing 2 containers:
- `stateless-load-balancer` — runs the LoadBalancer controller
- `router` — runs Bird3 for BGP/routing protocol advertisement

### NFQLB Architecture

**One NFQLB process per container**, started in `flowlb` mode:

```bash
nfqlb flowlb --queue=0:3 --qlength=1024 --promiscuous_ping
```

This single process manages **multiple shared-memory LB instances** (one per DistributionGroup). Each instance is a shared memory region, not a separate process:

```bash
nfqlb init --shm=<name> --M=<m> --N=<n> --ownfw=0
```

### Detailed Flow

```
                          ┌──────────────────────────────────────────────────────────┐
                          │ SLLB Pod (per Gateway)                                   │
                          │                                                          │
                          │  ┌────────────────────────────────────────────────────┐  │
  Incoming VIP traffic    │  │ stateless-load-balancer container                  │  │
  ──────────────────────► │  │                                                     │  │
                          │  │  nftables (prerouting)                             │  │
                          │  │  ├─ Match VIP → queue to nfqueue 0-3              │  │
                          │  │  │                                                 │  │
                          │  │  ▼                                                 │  │
                          │  │  nfqlb flowlb (single process)                    │  │
                          │  │  ├─ Reads shared memory instances                  │  │
                          │  │  ├─ Maglev hash → selects target                  │  │
                          │  │  └─ Sets fwmark on packet                         │  │
                          │  │                                                     │  │
                          │  │  Policy routing (ip rule / ip route)               │  │
                          │  │  ├─ fwmark 5000 → table 5000 → via target-1       │  │
                          │  │  ├─ fwmark 5001 → table 5001 → via target-2  ─────┼──┼──► net1 (macvlan)
                          │  │  └─ fwmark 5002 → table 5002 → via target-3       │  │    to targets
                          │  │                                                     │  │
                          │  │  LoadBalancer Controller                           │  │
                          │  │  ├─ Watches DistributionGroups, L34Routes,         │  │
                          │  │  │  LoadBalancerEndpointSlices, Gateways            │  │
                          │  │  ├─ Creates/deletes shared-memory instances        │  │
                          │  │  ├─ Configures nftables VIP rules                 │  │
                          │  │  ├─ Configures policy routing per target           │  │
                          │  │  └─ Writes readiness files                        │  │
                          │  └────────────────────────────────────────────────────┘  │
                          │                                                          │
                          │  ┌────────────────────────────────────────────────────┐  │
                          │  │ router container (Bird3)                           │  │
                          │  │  ├─ Reads readiness files from shared volume       │  │
                          │  │  └─ Advertises VIPs via BGP when ready             │  │
                          │  └────────────────────────────────────────────────────┘  │
                          └──────────────────────────────────────────────────────────┘
```

### Architectural Analogy

The controller mirrors the Kubernetes Service/kube-proxy architectural pattern:

```
┌─────────────────────────────────┬──────────────────────────────────────────────┐
│ Kubernetes                      │ Meridio-2                                    │
├─────────────────────────────────┼──────────────────────────────────────────────┤
│ Service (abstract LB)           │ DistributionGroup (abstract LB)              │
│ EndpointSlice (backends)        │ LoadBalancerEndpointSlice (backends)         │
│ kube-proxy (per-node agent)     │ LB controller (per-Gateway agent)            │
│ Watches: Service (primary)      │ Watches: DistributionGroup (primary)         │
│ Implements: iptables/ipvs       │ Implements: NFQLB (Maglev)                   │
└─────────────────────────────────┴──────────────────────────────────────────────┘
```

**Key insight**: kube-proxy watches **Service** (not Node), even though it runs per-node. Similarly, the LoadBalancer controller watches **DistributionGroup** (not Gateway), even though it runs per-Gateway.

## Design Principles

- **No finalizers**: In-memory state only (shared memory, policy routes, nftables). DistributionGroup deletion triggers cleanup via the NotFound path — no external resources require finalization.
- **Reconcile loop is authoritative**: Mappers enqueue broadly, reconcile decides via `belongsToGateway()`. Multiple Gateways coexist without interference.
- **Idempotent reconciliation**: Safe to run multiple times. `RouteReplace` and `ensureRule` are idempotent kernel operations.
- **DistributionGroup as primary resource**: Mirrors kube-proxy/Service pattern (see ADR-001). Each DG maps 1:1 with an NFQLB shared-memory instance.
- **Stateless on restart**: `CleanupStaleRules` at startup removes all fwmark rules >= `startingOffset`, then the first reconcile rebuilds state from LoadBalancerEndpointSlices. No persistent storage required.

## Resource Relationships

```
DistributionGroup
├── spec.parentRefs → Gateway (direct reference, checked first by belongsToGateway)
├── spec.selector → Pods (label matching, managed by DG controller)
├── spec.maglev.maxEndpoints → NFQLB N parameter
└── (indirect) ← L34Route.backendRefs → DistributionGroup

L34Route
├── spec.parentRefs → Gateway (determines which LB controller handles it)
├── spec.backendRefs → DistributionGroup (links to NFQLB instance)
├── spec.destinationCIDRs → VIP addresses (for nftables and flows)
├── spec.sourceCIDRs → flow match criteria (source filtering)
├── spec.protocols → flow match criteria
├── spec.priority → flow priority
├── spec.destinationPorts → flow match criteria (port filtering)
├── spec.sourcePorts → flow match criteria (port filtering)
└── spec.byteMatches → flow match criteria (L4 header byte patterns)

Gateway
└── status.addresses → VIP addresses (aggregated from L34Routes)

LoadBalancerEndpointSlice (owned by DistributionGroup)
├── spec.distributionGroupName → DG name
├── spec.gatewayRef → Gateway (name + namespace)
├── spec.endpoints[].target → Pod (name + UID)
├── spec.endpoints[].addresses[] → IP + family
├── spec.endpoints[].identifier → Maglev slot index
└── spec.endpoints[].ready → target readiness

NFQLB Instance (in-memory, per DistributionGroup)
├── shared memory: /dev/shm/<instance-name>
├── targets → activated endpoints with fwmarks
└── flows → traffic classification rules from L34Routes

Policy Routes (kernel state, per target)
├── ip rule: fwmark <fwmark> → table <fwmark>
└── ip route: default via <target-ip> table <fwmark>

nftables (shared across all DGs in this LB Pod)
├── VIP sets (IPv4 + IPv6)
├── prerouting chain: VIP traffic → nfqueue
└── output chain: local ICMP to VIPs → nfqueue
```

## Reconciliation Flow

### 1. Fetch DistributionGroup

- Return early if not found → `cleanupDistributionGroup()`:
  - Delete NFQLB shared-memory instance
  - Remove readiness file
  - Clean targets/flows from in-memory tracking state

### 2. Check Gateway Ownership

- Call `belongsToGateway()`:
  1. **Direct parentRefs**: Check `DistributionGroup.Spec.ParentRefs` — if any entry references this controller's Gateway (by group, kind, name, namespace), return true immediately
  2. **Indirect via L34Routes**: List L34Routes in GatewayNamespace, check if any L34Route has both:
     - `parentRefs` referencing this controller's Gateway (by name)
     - `backendRefs` referencing this DistributionGroup (by name, group, kind)
- Skip reconciliation if DG doesn't belong to this Gateway
- If previously managed (instance exists in memory) but no longer belongs → `cleanupDistributionGroup()`

**Why two ownership paths:** A DistributionGroup can reference a Gateway directly via `parentRefs`, or be connected indirectly through L34Routes that bind DG to Gateway. Both are valid.

### 3. Reconcile NFQLB Instance

- Create if not exists: `nfqlb init --ownfw=0 --shm=<name> --M=<m> --N=<n>`
  - `--ownfw=0`: disabled (no fwmark reserved for LB's own traffic); hardcoded constant
  - **M** (Maglev table size): `maxEndpoints × 100`
  - **N** (max endpoints): from `DistributionGroup.Spec.Maglev.MaxEndpoints` (default: 102)
  - **Offset**: dynamically allocated contiguous range starting from `startingOffset` (default 5000)
- Skip if instance already exists (idempotent)
- Track in `controller.instances` map

**Why M = maxEndpoints × 100:** Maglev hashing requires a large lookup table relative to the endpoint count for uniform distribution. The 100× multiplier provides sufficient table slots for consistent hashing quality even with frequent endpoint changes.

### 4. Reconcile Targets

- List LoadBalancerEndpointSlices via field indexers (`spec.distributionGroupName` + `spec.gatewayRef.name`)
- Filter to slices owned by this DistributionGroup (ownerReference check — defends against manually-created slices and stale slices from a previous DG incarnation with the same name but different UID)
- Sort by name for deterministic processing
- Extract target identifiers from `endpoint.Identifier` (int pointer, Maglev slot index)
- Extract target IPs from `endpoint.Addresses[].IP`
- Filter by `endpoint.Ready == true`
- First occurrence of an identifier wins (duplicates across slices during transients are logged and skipped)

**Deactivate removed targets:**
- `Instance.DeleteTarget(identifier)` — deactivates in shared memory + deletes policy routes

**Activate/update desired targets:**
- `Instance.AddTarget(ips, identifier)` — creates policy routes (`RouteReplace` + `ensureRule`), then activates in shared memory
- For existing targets: re-applies routes idempotently (drift recovery)
- For IP changes (Pod reschedule): cleans old neighbor entries, removes old routes, creates new routes
- For broken targets: full retry — re-applies policy routes AND re-activates in nfqlb shared memory (see [Broken Target Semantics](#broken-target-semantics))

**Readiness signaling:**
- Create readiness file when at least one target successfully activated
- Remove readiness file when no targets remain
- Router controller reads these files to decide VIP advertisement via Bird3 (BGP)

**Track committed targets:** Stores desired ∪ failed-deletes for next reconcile retry.

### 5. Reconcile Flows

- List L34Routes matching this Gateway AND this DistributionGroup

**If no L34Routes found:** Delete ALL flows for this DG + clear nftables VIPs. This only happens when L34Routes are explicitly removed — flows are never deleted based on endpoint availability.
- Delete removed flows from NFQLB instance
- Add/update flows: maps L34Route → `nfqlb.Flow` via `l34RouteFlow` adapter
- Configure nftables VIP sets: fetch VIPs from `Gateway.status.addresses` (via `getGatewayVIPs()`), call `nftManager.SetVIPs()`

**Flow naming:** The flow name is the L34Route's metadata name (e.g., `my-http-route`). The flow is bound to its NFQLB instance via the `--target` flag which receives the DistributionGroup name.

**Track flows:** Stored in `controller.flows` map for next reconcile comparison.

### 6. Return Result

- Return error for requeue on any failure (target activation, flow config, nftables)
- Return nil on success

## Watch Triggers

| Resource | Trigger | Mapper Function | Filtering Strategy |
|----------|---------|-----------------|-------------------|
| DistributionGroup | Create/Update/Delete | Direct (`.For()`) | None (all events) |
| LoadBalancerEndpointSlice | Create/Update/Delete | `endpointSliceEnqueue` | OwnerReference to DistributionGroup + `spec.gatewayRef` matches this Gateway |
| Gateway | Create/Update/Delete | `gatewayEnqueue` | Name matches controller's Gateway + lists DGs with direct `parentRefs` to this Gateway |
| L34Route | Create/Update/Delete | `l34RouteEnqueue` | Checks parentRefs for this Gateway AND backendRefs for DG kind |

### Filtering Strategy Rationale

- **LoadBalancerEndpointSlice mapper**: Checks namespace matches this Gateway's namespace, `spec.gatewayRef` matches this Gateway, and ownerReference points to a DistributionGroup. Enqueues the owning DG.
- **Gateway mapper**: Only triggers if Gateway name matches this controller's Gateway. Lists all DistributionGroups and enqueues those with `spec.parentRefs` referencing this Gateway. Note: DGs linked only via L34Route (no direct `parentRefs`) are not re-reconciled by this mapper — they rely on the L34Route mapper instead.
- **L34Route mapper**: Checks both `parentRefs` (Gateway match) and `backendRefs` (DG kind). Enqueues each referenced DG.
- **All mappers enqueue broadly**: The reconcile loop makes the final decision via `belongsToGateway()`. This is simpler and more robust than pre-filtering in mappers.

## NFQLB Internals

The `internal/nfqlb` package manages the nfqlb process, shared memory instances, flow classification, target activation, and policy routing. Nftables VIP matching is managed externally via `internal/nftables.Manager`.

### Implementation Files

```
internal/nfqlb/
├── nfqlb.go       # NFQueueLoadBalancer struct, AddInstance, DeleteInstance, AddTarget, DeleteTarget, AddFlow, DeleteFlow
├── routing.go     # createPolicyRoute, deletePolicyRoute, ensureRule, CleanupStaleRules, cleanNeighbor
├── offset.go      # getOffset() dynamic allocation
├── validate.go    # Input validation for all CLI arguments
├── config.go      # nfqlbConfig, nfqlbInstanceConfig
├── option.go      # WithQueue, WithQLength, WithMaxTargets, WithNfqlbPath, WithStartingOffset
└── const.go       # Constants and defaults
```

## NFQLB Instance Lifecycle

### Instance Creation

Called by `reconcileNFQLBInstance()` when a new DistributionGroup is first seen. The nfqlb shared memory instance provides the Maglev consistent hashing lookup table.

**Command:** `nfqlb init --ownfw=0 --shm=<name> --M=<m> --N=<n>`

**Parameters:**
- Instance name = DistributionGroup name
- `M` = `maxEndpoints × 100` (Maglev hash table size, via `maglevMMultiplier`)
- `N` = `maxEndpoints` from DG spec (default: 102)
- `--ownfw=0`: disabled (no fwmark reserved for LB's own traffic); hardcoded constant
- Offset dynamically allocated via `getOffset()` — finds first non-overlapping fwmark range

**Behavior:**
- Returns existing instance if already created (idempotent — checked in both the controller's `c.instances` map and `NFQueueLoadBalancer.instances`)
- Fails with error if the nfqlb process is not running (`running.Load() == false`)
- Validates name (alphanumeric, dash, underscore, dot; no leading dash; non-empty)
- Instance tracked in `NFQueueLoadBalancer.instances` map (protected by mutex)

### Instance Deletion

Called from `cleanupDistributionGroup()` when a DG no longer belongs to this Gateway or is deleted.

**Sequence:**
1. Remove instance from `nfqlb.instances` map (under `nfqlb.mu`)
2. `nfqlb delete --shm=<name>` — unlinks shared memory file
3. Deactivate all targets (with policy route cleanup via `deleteTargetNoLock`)
4. List all flows (`flow-list`) and delete those matching `user_ref == name`
5. Uses `errors.Join` for error accumulation — all cleanup attempted regardless of individual failures

## Offset Allocation

The offset determines the fwmark range for a DistributionGroup's targets.

### Formula

```
fwmark = identifier + instance.offset
```

Where:
- `identifier` = Maglev slot index from LoadBalancerEndpointSlice `endpoint.Identifier` field (`0` to `maxTargets-1`)
- `instance.offset` = dynamically allocated starting fwmark for this DG

**Key property:** `fwmark == routing table ID` — the same value is used for both the fwmark and the ip rule table, simplifying kernel configuration.

### Allocation Algorithm (`getOffset()`)

1. Start searching from `startingOffset` (default: 5000)
2. For each existing instance, check if candidate range `[offset, offset+maxTargets-1]` overlaps with `[instance.offset, instance.offset+instance.maxTargets-1]`
3. If overlap found, advance past that instance's range (`offset = instance.offset + instance.maxTargets`) and restart the search
4. First non-overlapping position wins
5. Capped at `maxOffset = 100000` — returns error if exceeded

Contiguous packing: ranges are sized by actual `maxTargets` per DG (not a fixed block size).

**Known limitation:** `maxOffset` is hardcoded and not configurable by users. Should be exposed via a CLI flag or `WithMaxOffset` option for clusters with many DistributionGroups.

**Fragmentation:** The allocation algorithm does not compact or defragment freed ranges. In deployments with aggressive DG churn and varied `maxEndpoints` values, fragmentation can exhaust the offset range earlier than the raw `maxOffset` ceiling would suggest — small gaps between allocations may be too small for new DGs with larger `maxEndpoints`.

### Example

```
DG-1 (maxTargets=32):  offset=5000, fwmarks 5000–5031
DG-2 (maxTargets=100): offset=5032, fwmarks 5032–5131
DG-3 (maxTargets=32):  offset=5132, fwmarks 5132–5163

If DG-2 is deleted: offset range 5032–5131 becomes available
New DG-4 (maxTargets=50): offset=5032, fwmarks 5032–5081 (reuses freed space)
```

## Target Management

### AddTarget (Activation)

Called on every reconcile for each desired target. The nfqlb layer handles idempotency — the controller calls `AddTarget` unconditionally for all desired targets.

**Sequence:**

1. **Validate inputs** (returns error immediately, does NOT mark target as broken):
   - IPs non-empty
   - Identifier in `[0, maxTargets)`
   - All IPs parseable via `net.ParseIP`

2. **Check existing state:**
   - Target exists with same IPs and is not broken → re-apply routes only (drift recovery), skip nfqlb activate
   - Target exists with different IPs → IP change path (Pod reschedule)
   - Target is new or in `broken` set → full activation path

3. **IP change handling** (only when target IPs differ from stored):
   - `cleanNeighbor` for all old IPs (flush stale ARP/NDP entries)
   - `deletePolicyRoute` for IPs no longer in the set

4. **Store new IPs** (`s.targets[identifier] = ips`) — committed regardless of whether subsequent steps succeed

5. **Create policy routes** (for all target IPs):
   - `ensureRule`: add ip rule only if not already present (prevents duplicate accumulation)
   - `netlink.RouteReplace`: atomically create or replace route in table (idempotent)
   - On `RouteReplace` failure: cleanup the rule to avoid orphaned rule pointing to empty table

6. **Activate in nfqlb shared memory** (only for new/broken targets):
   - `nfqlb activate --index=<id> --shm=<name> <fwmark>`
   - Writes fwmark into Maglev lookup table slot

7. **Broken target tracking** (applies to errors from steps 3–6, not validation):
   - On any error after lock: mark target as `broken` (will be retried with full activation on next reconcile)
   - On success: remove from `broken` set

### DeleteTarget (Deactivation)

1. `nfqlb deactivate --index=<id> --shm=<name>` — removes fwmark from Maglev table
2. Delete policy routes for all stored IPs
3. On error: keeps target in `broken` set; on success: removes from `targets` map

### Policy Routing Details

**Route creation (`createPolicyRoute`):**
```
ip rule add fwmark <fwmark> table <fwmark>      (ensureRule — skip if exists)
ip route replace default via <target-ip> table <fwmark>   (RouteReplace — idempotent)
```

**Route deletion (`deletePolicyRoute`):**
```
ip rule del fwmark <fwmark> table <fwmark>      (ignore ENOENT — already gone)
ip route del default via <target-ip> table <fwmark>   (ignore ESRCH — already gone)
```

**Why `RouteReplace` instead of `RouteAdd`:**
- Container restart: kernel state (rules/routes) survives but in-memory state is lost
- After `CleanupStaleRules` at startup removes everything, `RouteReplace` handles both fresh creation and stale route overwrite
- Single atomic operation — no need to check if route exists first

**Why `ensureRule` instead of `RuleAdd`:**
- Linux allows duplicate ip rules (same mark + table)
- Without ensureRule, every reconcile would accumulate another duplicate rule
- ensureRule scans existing rules and only adds if no match found (mark + table comparison)

**ARP/NDP cleanup (`cleanNeighbor`):**
- Only called when target IPs actually change (not on every reconcile)
- Scans all neighbors, deletes entries matching the old IP
- Prevents traffic going to stale MAC address after Pod reschedule

## Startup Recovery

**Problem:** On container restart, kernel state (ip rules, routing tables) survives but controller in-memory state (`instances`, `targets`, `flows`) is lost.

**Solution: Clean slate approach**

1. `CleanupStaleRules(startingOffset)` called in `NFQueueLoadBalancer.Start()`
2. Scans all ip rules in kernel (both IPv4 and IPv6 via `FAMILY_ALL`)
3. Removes any rule with `Mark >= startingOffset`
4. Deletes the routing table entry for each removed rule
5. After cleanup, in-memory state and kernel state are both empty
6. First reconcile cycle rebuilds everything from LoadBalancerEndpointSlices (source of truth)

**Why clean slate instead of state recovery:**
- LoadBalancerEndpointSlices are the authoritative source of truth (managed by DG controller)
- Rebuilding from scratch is simpler and more reliable than parsing kernel state to recover `instance → offset → target` mappings
- `RouteReplace` and `ensureRule` make rebuild safe (no duplicate rules, atomic route creation)
- Brief period after startup where targets are being re-added is acceptable (BGP reconvergence happens anyway during container restart)

## Error Handling

| Error Source | Example | Action | Requeue? |
|---|---|---|---|
| `belongsToGateway` | API server error listing L34Routes | Return error | Yes |
| `reconcileNFQLBInstance` | NFQLB process not running | Return error | Yes |
| `reconcileNFQLBInstance` | Offset range exhausted (>100000) | Return error | Yes (bug: should not requeue) |
| `reconcileTargets` | Missing identifier on endpoint | Log, skip endpoint | No (continues) |
| `reconcileTargets` | Netlink EPERM (missing NET_ADMIN) | Return accumulated errors | Yes |
| `reconcileTargets` | `nfqlb activate` fails | Mark target as broken, accumulate error | Yes |
| `AddTarget` | Route creation fails | Mark broken, cleanup rule, return error | Yes (via caller) |
| `reconcileFlows` | `nfqlb flow-set` fails | Log error, accumulate | Yes |
| `reconcileFlows` | nftables SetVIPs fails | Return error | Yes |
| `cleanupDistributionGroup` | `nfqlb delete` fails | Log error, return error | Yes |
| `cleanupDistributionGroup` | Flow/target cleanup partial failure | `errors.Join`, return combined | Yes |

### Broken Target Semantics

- A target in the `broken` set means either route creation or nfqlb activation partially failed
- Next reconcile calls `AddTarget` again — the `broken` flag forces full activation (routes + nfqlb activate) instead of the route-only fast path
- Prevents targets stuck in inconsistent state (routes exist but not activated, or vice versa)
- On `DeleteTarget` failure, the target remains in `broken` — next reconcile retries deletion

### Error Accumulation Pattern

The controller uses `errors.Join` to accumulate errors during target/flow reconciliation rather than failing fast. This ensures:
- All targets are attempted even if some fail (partial progress)
- Failed deletions are tracked and retried
- The combined error is returned to trigger requeue

## Input Validation

All user-controlled inputs are validated before passing to `exec.Command`:

| Input | Validation Rule |
|---|---|
| Instance/flow names | Alphanumeric, dash, underscore, dot; no leading dash; non-empty |
| CIDRs | Must pass `net.ParseCIDR` |
| Port ranges | Numeric, 0–65535, start ≤ end |
| Protocols | Only `tcp`, `udp`, `sctp` |
| Target IPs | Must pass `net.ParseIP` |
| Target identifiers | Must be in `[0, maxTargets)` |
| Queue format | Validated via `getQueue` at construction |

While `exec.Command` passes args directly without a shell (no shell injection possible), validation prevents the nfqlb binary from receiving malformed input and provides clear error messages at the Go layer.

## nftables Rules

The nftables manager (`internal/nftables/manager.go`) creates and maintains packet filtering rules that steer VIP-destined traffic into the nfqueue for NFQLB processing.

### Table Structure

A single `inet` family table (`meridio-lb`) is shared across all DistributionGroups within one LB Pod:

```
table inet meridio-lb {
    set ipv4-vips { type ipv4_addr; flags interval; }
    set ipv6-vips { type ipv6_addr; flags interval; }

    chain prerouting { ... }
    chain output { ... }
    chain snat-local { ... }
}
```

| Component | Name | Purpose |
|-----------|------|---------|
| Table | `meridio-lb` | Shared inet-family table for all rules |
| Set | `ipv4-vips` | Interval set of IPv4 VIP CIDRs |
| Set | `ipv6-vips` | Interval set of IPv6 VIP CIDRs |
| Chain | `prerouting` | Queues incoming VIP traffic to nfqueue |
| Chain | `output` | Queues locally-originated ICMP/ICMPv6 replies to VIPs |
| Chain | `snat-local` | Rewrites PMTU ICMP source addresses |

### Prerouting Chain

Hooks at `filter` priority in the prerouting path. Matches packets whose destination address is in the VIP set and queues them to nfqueue for NFQLB processing:

```
chain prerouting {
    type filter hook prerouting priority filter;

    # IPv4: queue VIP-destined traffic
    meta nfproto ipv4 ip daddr @ipv4-vips counter queue num 0-3

    # IPv6: queue VIP-destined traffic
    meta nfproto ipv6 ip6 daddr @ipv6-vips counter queue num 0-3
}
```

The queue number range (e.g., `0-3`) is configured at manager creation time via `NewManager(queueNum, queueTotal)`.

### Output Chain

Hooks at `filter` priority in the output path. Matches locally-originated ICMP/ICMPv6 packets destined to VIP addresses and queues them back through nfqueue. This enables NFQLB to handle ping responses from VIPs correctly:

```
chain output {
    type filter hook output priority filter;

    # IPv4: ICMP replies to VIPs
    meta nfproto ipv4 meta l4proto icmp ip daddr @ipv4-vips counter queue num 0-3

    # IPv6: ICMPv6 replies to VIPs
    meta nfproto ipv6 meta l4proto icmpv6 ip6 daddr @ipv6-vips counter queue num 0-3
}
```

### PMTU SNAT Chain (snat-local)

Hooks at `raw` priority in the output path with chain type `route` (allows the kernel to re-evaluate routing after source address rewrite). This chain rewrites the source address of locally generated ICMP "Fragmentation Needed" (type 3, code 4) and ICMPv6 "Packet Too Big" (type 2) messages so that external clients see the VIP as the source instead of the LB pod IP.

```
chain snat-local {
    type route hook output priority raw;

    # IPv4: Rewrite src of ICMP Frag Needed to VIP from encapsulated packet
    meta nfproto ipv4 meta l4proto icmp \
        meta mark != 0 \
        ip daddr != @ipv4-vips \
        ip saddr != @ipv4-vips \
        @th,0,8 == 3 @th,8,8 == 4 \
        @th,192,32 @ipv4-vips \
        counter \
        ip saddr set @th,192,32 \
        meta mark set 0

    # IPv6: Rewrite src of ICMPv6 Packet Too Big to VIP from encapsulated packet
    meta nfproto ipv6 meta l4proto icmpv6 \
        meta mark != 0 \
        ip6 daddr != @ipv6-vips \
        ip6 saddr != @ipv6-vips \
        @th,0,8 == 2 \
        @th,256,128 @ipv6-vips \
        counter \
        ip6 saddr set @th,256,128 \
        meta mark set 0
}
```

**Matching logic** (both IPv4 and IPv6):

1. Protocol must be ICMP/ICMPv6
2. Packet mark must be non-zero (confirms the original packet was processed by NFQLB — requires `net.ipv4.fwmark_reflect=1` and `net.ipv6.fwmark_reflect=1` sysctls)
3. Destination is NOT a VIP (avoids mangling ICMP destined to VIPs, which are handled by the output chain)
4. Source is NOT already a VIP (skip if already rewritten)
5. ICMP type/code matches Frag Needed (IPv4) or Packet Too Big (IPv6)
6. Encapsulated destination address (the original packet's destination) IS a VIP

**Action**: Overwrites the IP source address with the encapsulated destination (the VIP), then resets the packet mark to 0 to prevent policy-routing interference from target fwmark rules.

### VIP Set Management

VIPs are extracted from `Gateway.status.addresses` (only entries with `type: IPAddress`). The `SetVIPs()` method:

1. Deduplicates the input CIDRs
2. Splits into IPv4 and IPv6 lists
3. Flushes the existing set elements
4. Adds new elements atomically (flush + add in a single nftables transaction)

IPs without a prefix length are normalized to `/32` (IPv4) or `/128` (IPv6). The sets use interval encoding to support CIDR ranges.

### Lifecycle

| Operation | Effect |
|-----------|--------|
| `Setup()` | Creates table, sets, and all three chains with rules |
| `SetVIPs(cidrs)` | Atomically replaces VIP set elements |
| `Cleanup()` | Deletes the entire table (removes all rules/sets) |

## Flow Configuration

Flows map L34Route resources to `nfqlb flow-set` commands, defining traffic classification rules that determine which shared-memory LB instance handles each packet.

### Flow Naming

Flow names use the L34Route resource name directly:

```
flowName = <l34route-name>
```

The flow is associated to its target NFQLB instance (one per DistributionGroup) via the `--target` flag, which receives the DistributionGroup name.

### L34Route → nfqlb Flow Mapping

The `l34RouteFlow` adapter (`internal/controller/loadbalancer/flow_adapter.go`) maps L34Route spec fields to `nfqlb flow-set` CLI arguments:

| L34Route Spec Field | nfqlb Flag | Notes |
|---------------------|------------|-------|
| (route name) | `--name` | Flow identifier |
| (DistributionGroup name) | `--target` | Shared-memory instance to use |
| `spec.priority` | `--prio` | Flow matching priority |
| `spec.protocols` | `--protocols` | Comma-separated (e.g., `tcp,udp`) |
| `spec.destinationCIDRs` | `--dsts` | Omitted if nil |
| `spec.sourceCIDRs` | `--srcs` | Omitted if all CIDRs have `/0` mask (any-IP) |
| `spec.destinationPorts` | `--dports` | Omitted if contains `0-65535` (any-port) |
| `spec.sourcePorts` | `--sports` | Omitted if contains `0-65535` (any-port) |
| `spec.byteMatches` | `--match` | L4 header byte matching patterns |

**Example resulting command:**

```bash
nfqlb flow-set \
    --name=my-http-route \
    --target=my-distribution-group \
    --prio=1 \
    --protocols=tcp \
    --dsts=10.0.0.1/32,fd00::1/128 \
    --dports=80,443
```

### VIP Aggregation

The `getGatewayVIPs()` function fetches VIP addresses to populate the nftables sets:

1. Reads the Gateway resource (`c.GatewayName` / `c.GatewayNamespace`)
2. Iterates `gateway.Status.Addresses`
3. Includes only entries where `type == IPAddressType`
4. Normalizes bare IPs to CIDR format:
   - IPv4 addresses → `/32` suffix
   - IPv6 addresses → `/128` suffix
5. Returns the list of CIDR strings for `SetVIPs()`

### configureNftables Optimization

The `configureNftables()` function avoids redundant nftables updates:

```go
func (c *Controller) configureNftables(ctx context.Context, distGroupName string, vips []string) error {
    // Skip if VIPs unchanged (order-independent comparison)
    if vipsEqual(c.currentVIPs, vips) {
        return nil
    }
    // Atomic update
    c.nftManager.SetVIPs(vips)
    c.currentVIPs = vips  // cache for next comparison
}
```

The `vipsEqual()` comparison is order-independent (uses map-based set comparison), so reordering of Gateway status addresses does not trigger unnecessary nftables flushes.

## Configuration

### Controller Flags

The `stateless-load-balancer` binary accepts the following flags:

| Flag | Env Var | Default | Description |
|------|---------|---------|-------------|
| `--gateway-name` | `MERIDIO_GATEWAY_NAME` | *(required)* | Name of the Gateway this load balancer belongs to |
| `--gateway-namespace` | `MERIDIO_GATEWAY_NAMESPACE` | *(required)* | Namespace of the Gateway |
| `--nfqueue` | `MERIDIO_NFQUEUE` | `"0:3"` | Netfilter queue range used by NFQLB |
| `--readiness-dir` | `MERIDIO_READINESS_DIR` | `/var/run/meridio` | Directory where LB readiness files are written. Empty string disables readiness signaling. |
| `--health-probe-bind-address` | `MERIDIO_PROBE_ADDR` | `:8081` | Address the health/ready probe endpoint binds to |
| `--log-level` | `MERIDIO_LOG_LEVEL` | `info` | Log level (`debug`, `info`, `error`) |
| `--metrics-bind-address` | `MERIDIO_METRICS_ADDR` | `0` (disabled) | Address the metrics endpoint binds to. Use `:8443` for HTTPS or `:8080` for HTTP. |
| `--metrics-secure` | `MERIDIO_METRICS_SECURE` | `true` | Serve metrics endpoint via HTTPS |
| `--metrics-cert-path` | `MERIDIO_METRICS_CERT_PATH` | `""` | Directory containing the metrics server TLS certificate |
| `--metrics-cert-name` | `MERIDIO_METRICS_CERT_NAME` | `tls.crt` | Filename of the metrics server certificate |
| `--metrics-cert-key` | `MERIDIO_METRICS_CERT_KEY` | `tls.key` | Filename of the metrics server private key |
| `--enable-http2` | `MERIDIO_ENABLE_HTTP2` | `false` | Enable HTTP/2 for metrics and webhook servers |

### NFQLB Internal Defaults

The following NFQLB parameters are compiled-in defaults (not currently exposed as CLI flags):

| Parameter | Default | Description |
|-----------|---------|-------------|
| Queue length | `1024` | Netfilter queue packet buffer length |
| Starting offset | `5000` | Starting fwmark offset to avoid collisions with existing routing tables |
| Max targets (nfqlb internal) | `100` | nfqlb package default; overridden by `DefaultMaglevMaxEndpoints` (102) from DG API |
| Maglev M multiplier | `100` | Maglev hash table size = maxTargets × 100 |
| Max offset | `100000` | Upper bound for fwmark allocation range |
| nfqlb binary | `nfqlb` | Path/name of the nfqlb binary (resolved via `$PATH`) |

### Environment Variables

All flags can be set via `MERIDIO_*` environment variables as shown in the table above.

**Precedence:** CLI flags > Environment variables > Defaults

Environment variables are only applied when the corresponding flag is not explicitly set on the command line.

### Container Requirements

The stateless-load-balancer container requires:

- **`NET_ADMIN` capability** — required for netlink operations, nftables rule management, and NFQLB queue attachment
- **Access to `/dev/shm`** — NFQLB uses shared memory for the Maglev lookup tables
- **`nfqlb` binary in `$PATH`** — the NFQLB userspace load balancer binary must be available in the container image
- **Shared volume at `/var/run/meridio/`** — readiness files are written here to signal the router container that a DistributionGroup has ready targets (files prefixed with `lb-ready-`)
- **No leader election** — each Pod runs its own independent load-balancer instance (leader election is disabled)

## Readiness Signaling

The LoadBalancer controller signals target availability to the router container via the filesystem. This gates VIP advertisement — BIRD only announces VIPs over BGP when at least one DistributionGroup has ready targets.

### Mechanism

- **Package**: `internal/common/readiness`
- **Directory**: `/var/run/meridio` (configurable via `--readiness-dir`)
- **File format**: `lb-ready-<distgroup-name>`
- **Shared volume**: emptyDir mounted in both `stateless-load-balancer` and `router` containers

### Lifecycle

| Event | Action | Effect |
|-------|--------|--------|
| At least one target successfully activated for a DG | `Readiness.Set(dgName)` | Creates `lb-ready-<dgName>` |
| All targets removed or DG has no ready endpoints | `Readiness.Remove(dgName)` | Removes the readiness file |
| DG deleted or no longer belongs to this Gateway | `cleanupDistributionGroup()` → `Readiness.Remove()` | Removes the readiness file |
| Container startup | `Readiness.Cleanup()` | Removes all `lb-ready-*` files (clean slate) |

### Router Side (Consumer)

The router controller calls `Readiness.Watch(ctx)` which uses `fsnotify` to detect Create/Remove events on the readiness directory. When readiness state transitions, a notification is sent on a channel. The router uses `Readiness.IsReady()` (glob for `lb-ready-*` files) to decide whether to include VIP static routes in the BIRD configuration.

## Testing

### Unit Tests

**File:** `internal/controller/loadbalancer/controller_test.go`

**Framework:** Ginkgo v2 + Gomega (BDD style), using `controller-runtime/pkg/client/fake` for Kubernetes API simulation.

**Mock Implementations:**

| Mock | Location | Purpose |
|------|----------|---------|
| `mockNFQLB` | `controller_test.go` (inline) | Implements `nfqlbManager` — tracks instances in a map |
| `mockNFQLBInstance` | `controller_test.go` (inline) | Implements `nfqlbInstance` — records AddFlow/DeleteFlow/AddTarget/DeleteTarget calls |
| `mockNftablesManager` | `nftables_mock.go` | Implements `nftablesManager` — tracks Setup/SetVIPs/Cleanup calls |
| `readiness.NewManager("")` | (disabled mode) | No-op readiness signaling for tests |

**Test Categories:**

- **`belongsToGateway`** — Direct parentRef matching, indirect L34Route matching, different-Gateway rejection
- **`reconcileNFQLBInstance`** — Instance creation, idempotency, sequential ID assignment, freed ID reuse
- **`reconcileTargets`** — Target activation/deactivation, non-ready endpoint filtering, missing identifier handling
- **`reconcileFlows`** — Flow creation from L34Route, route filtering by Gateway/DG, flow deletion
- **`endpointSliceEnqueue`** — GatewayRef filtering, ownerReference-based DG lookup
- **`l34RouteEnqueue`** — Gateway+DG matching, wrong Gateway rejection
- **Cleanup on deletion** — Full state cleanup when DG NotFound

**Running tests:**

```bash
make test
# Or directly:
cd internal/controller/loadbalancer && go test ./...
```

### Manual Testing in Cluster

Refer to the [Troubleshooting Guide](../operations/troubleshooting.md#load-balancer-nfqlb) for full command explanations.

#### 1. Check NFQLB shared memory instances

```bash
kubectl exec -n <ns> <sllbr-pod> -c loadbalancer -- nfqlb show --shm=<dg-name>
```

Expected: `Active` line lists fwmarks for each ready target. `M` and `N` match the DG's Maglev config.

#### 2. Check flows

```bash
kubectl exec -n <ns> <sllbr-pod> -c loadbalancer -- nfqlb flow-list
```

Expected: One flow per L34Route referencing this DG. Each flow shows `dests` (VIPs), `protocols`, `priority`, and `user_ref` (NFQLB instance name).

#### 3. Check policy routing

```bash
# List fwmark rules
kubectl exec -n <ns> <sllbr-pod> -c loadbalancer -- ip rule

# Verify route for a specific fwmark
kubectl exec -n <ns> <sllbr-pod> -c loadbalancer -- ip route show table <fwmark>
```

Expected: One `fwmark <N> lookup <N>` rule per active target. Each table contains `default via <target-ip> dev <interface>`.

#### 4. Check nftables rules

```bash
kubectl exec -n <ns> <sllbr-pod> -c loadbalancer -- nft list ruleset
```

Expected: `ipv4-vips` and `ipv6-vips` sets contain VIPs from all L34Routes. Prerouting chain queues VIP traffic to nfqueue.

#### 5. Check readiness files

```bash
kubectl exec -n <ns> <sllbr-pod> -c loadbalancer -- ls /var/run/meridio/
```

Expected: `lb-ready-<dg-name>` file present for each DG with active targets.

## Future Enhancements

### Expose `maxOffset` as Configurable (MEDIUM PRIORITY)

The fwmark offset ceiling is hardcoded at 100,000. Clusters with many DistributionGroups (each consuming `maxEndpoints` fwmark slots) could exhaust this range. Expose via CLI flag (`--max-offset`) or `WithMaxOffset` option.

### Fix Offset Exhaustion Error Handling (BUG)

**Current behavior:** When `getOffset()` exceeds `maxOffset`, it returns an error that propagates to the reconcile return, triggering infinite requeue.

**Problem:** Offset exhaustion is a persistent configuration issue. Infinite requeue wastes resources and floods logs.

**Fix:** Log a warning and return `ctrl.Result{}` (no requeue). Only retry when DG count changes.

### Add Observability Metrics (LOW PRIORITY)

No metrics are currently exposed. An ongoing study is determining which counters should be collected and exposed.

### DeletionTimestamp Guard in Reconcile (LOW PRIORITY)

The `Reconcile` method does not check `DeletionTimestamp` on the fetched DistributionGroup. Currently harmless because the controller uses no finalizers and DG deletion results in NotFound on the next reconcile. Adding the check would be defense-in-depth.

## Implementation Files

```
internal/controller/loadbalancer/
├── controller.go       # Reconcile loop, belongsToGateway, SetupWithManager, watch mappers
├── instance.go         # reconcileNFQLBInstance (NFQLB service creation)
├── targets.go          # reconcileTargets (endpoint activation/deactivation)
├── flows.go            # reconcileFlows, configureNftables, getGatewayVIPs
├── flow_adapter.go     # l34RouteFlow + nameOnlyFlow adapters (L34Route → nfqlb.Flow)
├── nfqlb_iface.go      # nfqlbManager/nfqlbInstance interfaces + NFQLBManagerAdapter
├── nftables_mock.go    # Mock nftablesManager for tests
├── controller_test.go  # Unit tests
└── README.md           # Quick reference (subset of this document)

internal/nfqlb/
├── nfqlb.go            # NFQueueLoadBalancer, Instance, AddInstance, DeleteInstance, AddTarget, DeleteTarget, AddFlow, DeleteFlow
├── routing.go          # createPolicyRoute, deletePolicyRoute, ensureRule, CleanupStaleRules, cleanNeighbor
├── offset.go           # getOffset() dynamic fwmark allocation
├── validate.go         # Input validation for CLI arguments
├── config.go           # nfqlbConfig, nfqlbInstanceConfig
├── option.go           # Functional options (WithQueue, WithQLength, etc.)
├── const.go            # Constants and defaults
├── utils.go            # Helper functions (getQueue, anyIPRange, anyPortRange, parseFlows)
├── doc.go              # Package documentation
├── nfqlb_test.go       # Core unit tests
├── target_test.go      # Target management tests
├── offset_test.go      # Offset allocation tests
├── utils_test.go       # Utility function tests
└── validate_test.go    # Validation tests

internal/nftables/
├── manager.go              # nftables table/chain/set management
├── manager_test.go         # Unit tests
└── manager_integration_test.go  # Integration tests (require nft binary)

internal/common/readiness/
└── readiness.go            # Readiness file manager (Set/Remove/IsReady/Watch/Cleanup)

cmd/stateless-load-balancer/
├── main.go                 # Binary entrypoint
└── cmd/
    ├── cmd.go              # Root command (run + version)
    ├── run.go              # Manager setup, NFQLB process start, controller registration
    └── version.go          # Version subcommand

internal/common/config/
└── loadbalancer.go         # LoadBalancerConfig with CLI flags + env binding
```

## References

- [ADR-001: DistributionGroup as Primary Resource](../architecture/adr-001-distributiongroup-primary-resource.md)
- [NFQLB (nfqueue-loadbalancer)](https://github.com/Nordix/nfqueue-loadbalancer) — upstream documentation for the Maglev load balancer binary
- [Gateway Controller](gateway.md) — manages LB Deployment lifecycle and Gateway status
- [DistributionGroup Controller](distributiongroup.md) — manages LoadBalancerEndpointSlices consumed by this controller
- [Router Controller](router.md) — consumes readiness files to gate VIP advertisement
- [Troubleshooting Guide — Load Balancer](../operations/troubleshooting.md#load-balancer-nfqlb) — debugging commands for nfqlb, nftables, and policy routing
- [Constraints and Limitations](../operations/constraints-and-limitations.md) — known limitations affecting LB behavior
