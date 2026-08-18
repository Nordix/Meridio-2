# EndpointNetworkConfiguration Controller

## Overview

The EndpointNetworkConfiguration (ENC) controller runs inside the controller-manager. It reconciles application Pods to produce `EndpointNetworkConfiguration` custom resources — one per Pod — that declare the desired network state: which Gateways the Pod connects to, which VIPs to assign, and which next-hops to use for source-based routing.

The ENC is consumed by the sidecar controller (`internal/controller/sidecar/`) running inside each application Pod, which applies VIPs, policy routing rules, and ECMP routes to the Pod's network namespace.

## Background & Rationale

### The Networking Contract

The user is responsible for defining the baseline network connectivity between `Gateway` load balancers and application endpoints. This is established through infrastructure-specific configuration in the application Pods and the `Gateway` resource (e.g. via Multus Network Attachment Definitions).

For an application Pod to use a `Gateway` service it joined via label matching, it must fulfill a networking contract:

- **VIP local termination**: VIP addresses must be assigned as local IPs on a specific interface within the Pod so the application can accept traffic destined for the service.
- **Symmetric routing**: All return traffic (or traffic originating from a VIP source address) must be steered back through the load balancers of the hosting `Gateway`. This is achieved via source-based routing (policy routing), preventing "asymmetric routing" where traffic would otherwise attempt to exit via the default Pod interface (`eth0`).

The ENC declares the desired state needed to satisfy this contract; the sidecar controller applies it inside the Pod's network namespace.

### Why a Per-Pod Custom Resource?

The ENC is a **per-Pod** custom resource. This choice replaces the earlier proof-of-concept approach of writing Pod annotations, which was unsuitable for production due to RBAC limitations: it is not possible to restrict "patch pod" permissions without granting broad access to the core Pod specification. A dedicated CRD lets network-processing entities be restricted to reading and updating only `EndpointNetworkConfiguration` objects, eliminating the risk of granting write access to Pod specs.

A per-Pod declarative model provides several advantages over shared or service-centric configurations:

- **Scaling and noise reduction**: A change to one Pod's network state triggers a reconciliation event only for that Pod. A shared/global object would force every agent in the cluster to re-verify its state on any change, increasing blast radius, load, and the chance of optimistic-concurrency conflicts between multiple writers.
- **Deterministic lifecycle management**: An `ownerReference` from the ENC to its application Pod ties the network metadata's lifecycle to the Pod. When the Pod is deleted, Kubernetes garbage collection removes the ENC automatically, avoiding the "stale state" problem where a controller must manually track and prune terminated Pods from a shared list.
- **Architecture-agnostic processing**: The per-Pod resource is a simplified data contract. The consuming entity (sidecar/node agent) needs no "Gateway awareness" — it does not collect metadata from various infrastructure objects; it simply consumes the pre-calculated instructions (VIPs, routes, interface hints) in its own dedicated CR.
- **Observability and granular status**: Each endpoint has its own `.status` subresource, giving immediate visibility into whether a specific Pod failed to apply its routing rules, without cluttering a shared object or parsing logs to find the affected pool member. (See [No Status on ENC](#no-status-on-enc) for the current implementation state.)

### Why Declarative Kubernetes over a Custom Config Server?

A direct channel like gRPC or a centralized custom configuration server might seem appealing, but the Kubernetes-native declarative pattern was chosen for several reasons:

- **Persistence and state visibility**: A custom gRPC server creates hidden state invisible to standard Kubernetes tooling (`kubectl`). Using the API server as the persistence layer makes the configuration auditable, visible, and persistent — it survives controller restarts and provides a trail for troubleshooting network plumbing issues.
- **Decoupling from ephemeral failures**: Direct gRPC requires a synchronous handshake where producer and consumer must both be healthy and reachable simultaneously. The declarative approach is asynchronous: the controller persists the intended state to the API server independently of the consumer. If the network configuration agent is temporarily down, the configuration remains buffered, and the system converges once the agent recovers — without stalling the controller.
- **Built-in scalability and HA**: A custom, highly available, secure config database would duplicate functionality already provided by `etcd` and the Kubernetes API. Reusing cluster infrastructure avoids deploying, maintaining, and securing a separate HA service for networking metadata.
- **Self-healing and drift compensation**: Unlike an imperative push (which may be lost during a network partition or restart), the controller pattern continuously reconciles observed state against desired state, automatically correcting configuration drift without complex retry logic in the application.

## Architecture

### Deployment Model

- Runs as part of the centralized controller-manager (not per-Pod)
- Scoped to a single namespace via `--namespace` (or all namespaces if empty)
- Single active instance (leader election) reconciling all application Pods
- Produces ENC resources with the same name as the target Pod, linked via ownerReference

### Resource Relationships

```
Pod (application)
├── labels → matched by DistributionGroup.spec.selector
└── annotations["k8s.v1.cni.cncf.io/network-status"] → interface discovery

DistributionGroup
├── spec.selector → matches application Pods
├── spec.parentRefs[] → direct Gateway reference
└── referenced by L34Route.spec.backendRefs[] → indirect Gateway path

L34Route
├── spec.parentRefs[] → Gateway
├── spec.backendRefs[] → DistributionGroup
└── spec.destinationCIDRs[] → VIPs (per-route granularity)

Gateway
├── status.addresses[] → VIPs (when referenced via direct DG parentRef)
├── status.conditions[Accepted] → must be accepted by this controller
└── spec.infrastructure.parametersRef → GatewayConfiguration

GatewayConfiguration
└── spec.internalSubnets[] → network context (CIDR + attachment type)

Pod (SLLBR / LB)
├── labels[gateway.networking.k8s.io/gateway-name] → identifies which Gateway
├── annotations["k8s.v1.cni.cncf.io/network-status"] → secondary IPs (next-hops)
├── status.containerStatuses[].ready → Level 1 filter
└── status.conditions[ipv4/ipv6-connectivity] → Level 2 filter (readiness gates)

EndpointNetworkConfiguration (output, 1:1 with Pod)
├── metadata.name = Pod.metadata.name
├── metadata.ownerReferences[0] → Pod (GC on Pod deletion)
└── spec.gateways[] → GatewayConnections
    ├── name → Gateway name
    └── domains[] → NetworkDomains (max 2: IPv4 + IPv6)
        ├── name → "<gateway>-<IPFamily>"
        ├── ipFamily → "IPv4" or "IPv6"
        ├── network.subnet → from GatewayConfiguration.internalSubnets
        ├── network.interfaceHint → from Multus network-status annotation
        ├── vips[] → plain IPs (from Gateway status or L34Route destinationCIDRs)
        └── nextHops[] → plain IPs of ready SLLBR Pods on the internal network
```

### Reconcile Flow

```
1. Pod event triggers reconcile (create, update, delete)
2. Fetch Pod
   - NotFound → ENC garbage-collected via ownerReference, return
3. Skip non-Running Pods → delete ENC if one exists
4. Resolve Gateway connections:
   a. List all DistributionGroups in namespace
   b. Filter DGs whose spec.selector matches the Pod's labels
   c. For each matching DG, resolve Gateways:
      - Direct: DG.spec.parentRefs → Gateway
      - Indirect: L34Route.backendRefs=DG → L34Route.parentRefs → Gateway
   d. Filter: only Gateways with Accepted=True condition matching this controller name
   e. Collect VIPs per Gateway:
      - Direct path: Gateway.status.addresses (all)
      - Indirect path: L34Route.spec.destinationCIDRs (per-route)
   f. Deduplicate Gateways (merge VIPs when same Gateway reached via multiple paths)
5. If no gateway connections resolved → delete ENC if exists, return
6. For each Gateway, build GatewayConnection:
   a. Fetch GatewayConfiguration via Gateway.spec.infrastructure.parametersRef
   b. Extract network contexts: internalSubnets → CIDR→attachmentType map
   c. Split VIPs by IP family (IPv4/IPv6), deduplicate, sort
   d. Resolve SLLBR next-hops (see Next-Hop Resolution below)
   e. For each subnet:
      - Determine IP family from CIDR
      - Skip if no VIPs and no next-hops for this family
      - Resolve interface name from Pod's Multus network-status annotation
      - Skip if Pod has no interface in this subnet
      - Build NetworkDomain
   f. Sort domains by name for deterministic output
7. Sort GatewayConnections by Gateway name
8. Create or update ENC:
   - Create with ownerReference if ENC doesn't exist
   - Update only if spec changed (semantic DeepEqual comparison)
   - On conflict: requeue immediately (not exponential backoff)
```

### Next-Hop Resolution (Two-Level Filtering)

SLLBR Pod IPs serve as next-hops for source-based routing in application Pods. The controller applies two filtering levels to ensure only healthy, connected LB Pods are included:

**Level 1 — Container Readiness:**
- Pod must be in `Running` phase
- Pod must not be terminating (`DeletionTimestamp == nil`)
- All containers must report `Ready` in their status

**Level 2 — Per-IP-Family Connectivity Gate:**
- For each IP extracted from an SLLBR Pod, check the corresponding readiness gate:
  - IPv4 IP → requires `meridio-2.nordix.org/ipv4-connectivity = True`
  - IPv6 IP → requires `meridio-2.nordix.org/ipv6-connectivity = True`
- If the gate is **not declared** on the Pod (e.g., IPv4-only Gateway has no IPv6 gate), the Pod is **included** for that family (gate not applicable)
- If the gate is declared but the condition is not yet set, the Pod is **excluded** (not ready)

**IP Extraction:**
- SLLBR Pods are found via label `gateway.networking.k8s.io/gateway-name=<gatewayName>`
- Secondary IPs are extracted from the Multus `k8s.v1.cni.cncf.io/network-status` annotation
- For each internal subnet, the controller finds a non-default network interface whose IP falls within the subnet CIDR

**Determinism:**
- Next-hops are sorted lexicographically per IP family to avoid unnecessary ENC updates from non-deterministic Pod list ordering

### VIP Sources

VIPs reach the ENC through two distinct paths depending on how the DistributionGroup references the Gateway:

| Path | Source | Granularity |
|------|--------|-------------|
| Direct (DG.parentRefs → Gateway) | `Gateway.status.addresses[]` | All VIPs on the Gateway |
| Indirect (L34Route.backendRefs=DG → L34Route.parentRefs → Gateway) | `L34Route.spec.destinationCIDRs[]` | Per-route VIP subset |

When the same Gateway is reached via both paths, VIPs are merged and deduplicated. CIDRs from `destinationCIDRs` (e.g., `10.0.0.1/32`) are stripped to plain IPs before inclusion in the ENC.

### Interface Identification and VIP Assignment

The ENC exists in part because assigning VIPs correctly requires more than static IP configuration. Two problems must be solved: identifying the *right* interface, and setting low-level flags that standard CNI plugins do not expose.

#### The `nodad` Requirement for IPv6

Static IP assignment via Multus network annotations is insufficient for VIP management. Standard CNI plugins lack support for critical low-level interface flags such as `nodad` (disable Duplicate Address Detection).

In an IPv6 environment where multiple Pods host the same VIP, the absence of `nodad` causes assignment failures: the IPv6 stack detects the address on a peer Pod and disables it locally on all but the first Pod that claims it. Because the sidecar (a custom processing entity) applies these addresses itself, it can explicitly set `nodad`, ensuring VIPs are assigned and maintained across all participating endpoints without protocol-level conflicts.

This is why the sidecar contract exposes `Domain.IPFamily` (see [Sidecar Contract](#sidecar-contract)): protocol-specific behavior like IPv6 `nodad` depends on it.

#### Interface Identification (Subnet-to-Interface Mapping)

Identifying the correct secondary interface is a prerequisite for VIP assignment. Even in a single-Gateway configuration, VIPs must be attached exclusively to the interface connected to the load-balancing segment.

Name-based identification is fragile and prone to drift. Interface indices and names can vary based on:

- **Attachment order**: the sequence of `NetworkAttachmentDefinitions` in the Pod spec (especially when no explicit interface name is provided via Multus annotations).
- **Inconsistent naming conventions**: variations across different application Pods.

The reliable strategy is a **subnet-to-interface mapping**: the consuming entity matches the Gateway's network prefix (from `GatewayConfiguration.internalSubnets`) against the Pod's actual interface IPs (as reported in the `k8s.v1.cni.cncf.io/network-status` annotation). By selecting the interface holding a primary IP within the Gateway's subnet, it locates the correct attachment regardless of the interface's local name. This is why `Network.Subnet` is the authoritative field in the sidecar contract.

#### Optimization via Interface Hints ("Trust but Verify")

To accelerate lookup, the controller-manager pre-calculates a likely interface name and provides it as `Network.InterfaceHint`. It can do this because it has access to both the Pod's current network status and the Gateway's network prefix while preparing the ENC.

The hint follows a "Trust but Verify" protocol on the consumer side:

1. **Accelerated lookup**: the consumer first checks the hinted interface name.
2. **Validation**: it verifies the hinted interface actually holds a primary IP within the expected network prefix (`Network.Subnet`) before applying any configuration.
3. **Fallback**: if the hint is missing or fails verification, the consumer falls back to a full scan of all interfaces to find the correct subnet match.

The hint is an optimization only; `Network.Subnet` remains the source of truth for identification.

#### Multiple Gateways in the Same Subnet

A single application Pod may use services from multiple Gateways simultaneously, so the ENC associates VIPs and next-hops with their respective Gateway instance (via `spec.gateways[]`).

**Note:** Multiple Gateways may operate within the same subnet. In that case their configurations (VIPs and next-hops) target the *same* secondary network interface in the application Pod. The subnet-to-interface mapping above resolves all of them to that shared interface; the per-Gateway structure keeps their routing next-hops distinct.

### Gateway Acceptance Filter

The controller only produces ENC entries for Gateways that have been accepted by this specific controller instance. Acceptance is determined by:

```go
condition.Type == "Accepted" &&
condition.Status == "True" &&
strings.HasSuffix(condition.Message, r.ControllerName)
```

This ensures the ENC controller ignores Gateways managed by other implementations sharing the same cluster.

## Watch Strategy

| Resource | Watch Type | Mapper | Purpose |
|----------|-----------|--------|---------|
| `Pod` (application) | Primary (`.For()`) | Direct | Pod create/update/delete triggers ENC reconcile |
| `EndpointNetworkConfiguration` | Owned (`.Owns()`) | controller-runtime built-in | ENC changes reconcile owning Pod |
| `DistributionGroup` | Secondary (`.Watches()`) | `mapDGToPods` | DG selector/parentRef changes affect Pod membership |
| `Gateway` | Secondary (`.Watches()`) | `mapGatewayToPods` | VIP or status changes propagate to affected Pods |
| `L34Route` | Secondary (`.Watches()`) | `mapL34RouteToPods` | Route VIP/backend changes propagate to affected Pods |
| `GatewayConfiguration` | Secondary (`.Watches()`) | `mapGatewayConfigToPods` | Subnet changes propagate to affected Pods |
| `Pod` (SLLBR) | Secondary (`.Watches()`) | `mapSLLBRPodToPods` | LB Pod IP/readiness changes update next-hops |

### Mapper Details

**mapDGToPods**: DG changed → list Pods matching `DG.spec.selector` → enqueue those Pods.

**mapGatewayToPods**: Gateway changed → find DGs with direct `parentRefs` to this Gateway + find L34Routes with `parentRefs` to this Gateway → collect DGs from L34Route `backendRefs` → list Pods matching those DGs → enqueue.

**mapL34RouteToPods**: L34Route changed → extract Gateway keys from `parentRefs` → delegate to `podsForGatewayKeys` (same logic as mapGatewayToPods).

**mapGatewayConfigToPods**: GatewayConfiguration changed → list all Gateways in namespace → find those whose `infrastructure.parametersRef` points to this GatewayConfiguration → delegate to `podsForGatewayKeys`.

**mapSLLBRPodToPods**: SLLBR Pod changed → extract Gateway name from `gateway.networking.k8s.io/gateway-name` label → delegate to `podsForGatewayKeys`. Filtered by predicate: only Pods with the `gateway-name` label trigger this mapper.

### Pod Filtering

The primary Pod watch uses a predicate to exclude LB Pods (those with `gateway.networking.k8s.io/gateway-name` label). This ensures the ENC controller only reconciles application Pods, not the SLLBR Pods it reads as next-hop sources.

### Pod Cache Label Optimization

When `--pod-cache-label` is configured (e.g., `meridio-2.nordix.org/managed=true`), the controller-manager's informer cache only stores Pods with that label. This reduces memory usage in large clusters where most Pods are unrelated to Meridio.

**Edge case**: If the label is removed from a Pod at runtime, the Pod is evicted from the informer cache — triggering a delete event — but the Pod still exists in the API server. The ENC controller's reconciler sees `NotFound` and returns `nil`, relying on ownerReference GC. However, GC won't fire because the Pod still exists. The controller source notes this edge case and suggests calling `deleteENCIfExists` as a potential improvement.

## Error Handling

| Error Source | Example | Requeue? | Rationale |
|-------------|---------|----------|-----------|
| Pod fetch | API server error | Yes (error return) | Transient |
| Pod NotFound | Pod deleted | No | ENC garbage-collected via ownerReference |
| resolveGatewayConnections | List/Get failures | Yes (error return) | Transient API errors |
| Gateway not accepted | Missing condition | No | Silently excluded — not an error |
| GatewayConfig NotFound | Ref points to missing resource | No (silent) | `client.IgnoreNotFound` — skip this Gateway |
| No interface for subnet | Pod lacks Multus IP in CIDR | No | Domain skipped — Pod may not have this network |
| ENC create/update | API conflict (409) | Yes (`Requeue: true`) | Fast retry without backoff |
| ENC create/update | Other API error | Yes (error return) | Transient |
| ENC delete (non-running Pod) | API error | Yes (error return) | Transient |

### Conflict Handling

On `409 Conflict` errors during ENC create/update, the controller returns `{Requeue: true}, nil` instead of returning the error directly. This triggers an immediate requeue rather than exponential backoff, because conflicts are expected from stale informer cache reads or concurrent writers and should resolve quickly.

### Idempotent Updates

The controller uses `apiequality.Semantic.DeepEqual` to compare existing and desired ENC specs before issuing an update. If the spec hasn't changed, no API call is made. This prevents unnecessary writes and avoids triggering the sidecar controller on no-op updates.

### Deterministic Output

Multiple sources of non-determinism are explicitly handled to prevent spurious ENC updates:

- GatewayConnections are sorted by Gateway name
- Domains within a GatewayConnection are sorted by domain name
- VIPs are deduplicated and sorted per IP family
- Next-hops are sorted per IP family
- Map iteration is never relied upon for output ordering

## ENC Lifecycle

### Creation

An ENC is created when:
1. A Pod is in `Running` phase
2. At least one DistributionGroup's selector matches the Pod's labels
3. At least one accepted Gateway is reachable via DG parentRefs or L34Routes
4. The Gateway has a valid GatewayConfiguration with InternalSubnets
5. The Pod has at least one secondary interface matching an InternalSubnet

### Deletion

An ENC is deleted when:
- The Pod is deleted → ownerReference GC (automatic)
- The Pod transitions to a non-Running phase → explicit delete
- No gateway connections are resolved (e.g., all DGs deselected the Pod) → explicit delete

### Deterministic Naming for Discovery

The controller creates each ENC with a name identical to the application Pod it serves (`metadata.name = Pod.metadata.name`), maintaining a strict 1:1 relationship. Beyond simplicity, this enables **no-lookup discovery** for the sidecar agent: rather than performing expensive label queries or `list` operations, the sidecar retrieves its own Pod name (typically via the [Downward API](https://kubernetes.io/docs/concepts/workloads/pods/downward-api/)) and immediately knows the exact name of the ENC to watch or fetch.

### Ownership

The ENC's `metadata.ownerReferences` points to the Pod with `controller: true`. This ensures:
- Kubernetes garbage collection deletes the ENC when the Pod is deleted
- The controller-runtime `.Owns()` watch triggers reconciliation on ENC changes

## Configuration

The ENC controller shares the controller-manager's configuration. It uses:

| Parameter | Source | Description |
|-----------|--------|-------------|
| `ControllerName` | `--controller-name` / `MERIDIO_CONTROLLER_NAME` | Used to filter accepted Gateways |
| `Namespace` | `--namespace` / `MERIDIO_NAMESPACE` | Scopes resource listing |

The controller does not have its own dedicated flags — it inherits all behavior from the controller-manager's shared configuration.

### RBAC Requirements

The ENC controller requires these permissions (configured in `config/rbac/manager-role.yaml`):

```yaml
# Pods: list/watch for endpoint discovery and next-hop resolution
- apiGroups: [""]
  resources: [pods]
  verbs: [get, list, watch]

# Gateways: read for VIP extraction and acceptance checking
- apiGroups: [gateway.networking.k8s.io]
  resources: [gateways]
  verbs: [get, list, watch]

# L34Routes: read for VIP resolution and backend traversal
- apiGroups: [meridio-2.nordix.org]
  resources: [l34routes]
  verbs: [get, list, watch]

# GatewayConfigurations: read for network context (internalSubnets)
- apiGroups: [meridio-2.nordix.org]
  resources: [gatewayconfigurations]
  verbs: [get, list, watch]

# DistributionGroups: read for Pod-to-Gateway mapping
- apiGroups: [meridio-2.nordix.org]
  resources: [distributiongroups]
  verbs: [get, list, watch]

# EndpointNetworkConfigurations: full lifecycle management
- apiGroups: [meridio-2.nordix.org]
  resources: [endpointnetworkconfigurations]
  verbs: [create, delete, get, list, patch, update, watch]
```

## Sidecar Contract

The ENC controller produces resources consumed by the sidecar controller. The following invariants are maintained:

| Field | Contract | Sidecar Usage |
|-------|----------|---------------|
| `GatewayConnection.Name` | Equals Gateway name, stable | Used as table ID allocation key |
| `Domain.Name` | `"<gateway>-<IPFamily>"` | Local state key for routing table mapping |
| `Domain.IPFamily` | `"IPv4"` or `"IPv6"` | Protocol-specific settings (e.g., IPv6 nodad) |
| `Domain.VIPs[]` | Plain IPs (not CIDRs), `net.ParseIP()` valid | Assigned as addresses on interfaces |
| `Domain.NextHops[]` | Plain IPs (not CIDRs), `net.ParseIP()` valid | ECMP default routes in per-Gateway tables |
| `Network.Subnet` | Valid CIDR, `net.ParseCIDR()` valid | Interface identification/validation |
| `Network.InterfaceHint` | Non-empty for NAD attachments | Fast-path interface lookup |

## Known Limitations

### Scalability

- **O(DGs) per reconcile**: `listMatchingDGs` lists all DistributionGroups in the namespace and iterates to find selector matches. No index is used for reverse-lookup (Pod labels → matching DGs).
- **O(L34Routes) per mapper call**: `podsForGatewayKeys` lists all L34Routes in the namespace to find those referencing a given Gateway. No field index on `parentRefs`.
- **Fan-out on SLLBR changes**: A single SLLBR Pod readiness change triggers reconciliation of all application Pods connected to that Gateway.

### No Status on ENC

The ENC controller does not write status conditions to the ENC resource. Status is written exclusively by the sidecar controller (the consumer). The ENC controller has no feedback loop on whether the desired network state was successfully applied.

### Race: VIP Exposure vs. Router Readiness

A cross-controller race exists: the ENC controller may expose VIPs to application Pods (via ENC creation/update) before the router controller has set up policy routing rules for those VIPs on the LB Pods. An app Pod could start sourcing traffic from a new VIP before any router has plumbed it. The next-hop two-level filtering mitigates this for new LB Pods, but VIP additions to existing Gateways with already-ready LB Pods are exposed immediately. This is noted in the [Router controller documentation](router.md) as well.

Note: This is distinct from LB readiness gating ([constraints #10/#11](../operations/constraints-and-limitations.md), resolved via readiness files and connectivity gates). This race concerns the timing of policy-routing setup on LB Pods relative to VIP exposure to app Pods - see the cross-controller race note in [router.md](router.md).

### Pod Phase Gate

The controller uses `pod.Status.Phase == PodRunning` as a prerequisite. A Pod can be `Running` without all containers being ready (e.g., during init). However, the Multus network-status annotation is typically populated only after network setup completes, so the interface resolution step naturally gates on network readiness.

## Testing

### Unit Tests

- `internal/controller/endpointnetworkconfiguration/controller_test.go`:
  - Pod not found handling
  - Non-running Pod cleanup (ENC deletion)
  - No matching DGs → no ENC created
  - ENC creation with ownerReference
  - ENC update only when spec changes
  - No-op when spec unchanged (ResourceVersion stability)
  - ENC deletion when exists/not-exists

- `internal/controller/endpointnetworkconfiguration/mappers_test.go`:
  - `mapDGToPods`: selector matching, nil selector
  - `mapGatewayToPods`: direct parentRef path, indirect L34Route path, no DGs
  - `mapL34RouteToPods`: parentRef extraction
  - `mapGatewayConfigToPods`: parametersRef lookup, no matching Gateway
  - `mapSLLBRPodToPods`: label extraction, no label
  - `podsForGatewayKeys`: Pod deduplication across multiple DGs

- `internal/controller/endpointnetworkconfiguration/resolve_test.go`:
  - `listMatchingDGs`: selector match, no match, nil selector
  - `resolveGatewaysForDG`: direct parentRef, indirect via L34Route, deduplication, not-accepted filter
  - `extractVIPs`: dual-stack, IPv4-only, empty, CIDR stripping, deduplication, garbage input
  - `updateGwVipMap`: insert and merge (no aliasing)
  - `getNetworkContexts`: parametersRef resolution, no parametersRef
  - `getSLLBRNextHops`: IP extraction, deterministic ordering
  - `buildGatewayConnection`: domain skipped when no interface, naming convention, VIP sorting
  - `resolveGatewayConnections`: full chain integration, deterministic ordering across insertion orders
  - `isLBPodReady`: all ready, one not ready, deleting, no statuses
  - `hasConnectivityGate`: True, False, not declared, declared but missing
  - Two-level filtering integration: healthy pods, container not ready, partial gate, no gates, deleting
  - Sidecar contract verification: validates all output invariants consumed by sidecar
