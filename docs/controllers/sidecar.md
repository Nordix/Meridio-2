# Sidecar Controller

## Overview

The Sidecar controller runs as a container in each application Pod, configuring VIP addresses and source-based policy routing based on the Pod's `EndpointNetworkConfiguration` (ENC) custom resource. It enables multi-Gateway connectivity by ensuring return traffic from each VIP is routed through the correct Gateway.

## Architecture

### Core Concepts

**EndpointNetworkConfiguration (ENC)**: A per-Pod CR (named after the Pod) that declares the desired network state — which Gateways the Pod connects to, which VIPs to assign, and which next-hops to use for source-based routing.

**GatewayConnection**: A section within the ENC representing connectivity to a single Gateway. Each Gateway gets a dedicated routing table ID.

**NetworkDomain**: An IP-family-specific configuration within a GatewayConnection. A dual-stack Gateway has two domains (IPv4 and IPv6) sharing the same routing table ID (IPv4/IPv6 table namespaces are independent in the kernel).

**Table ID**: A Linux policy routing table identifier allocated per Gateway name. Sequential allocation from a configurable range with freed ID reuse. Keyed on `GatewayConnection.Name`, not hash-derived.

### Design Principles

- **Stateless**: All state is derived from the ENC spec on each reconcile. In-memory state (table ID allocations, managed VIP sets) is reconstructable.
- **Partial failure tracking**: `syncVIPs` always returns the actual managed set, even on error, so the controller stays in sync with kernel state.
- **Separate error semantics**: ENC content errors (invalid VIP, bad CIDR) don't requeue — wait for user fix. Interface-not-found is transient (interface may appear) — requeue with backoff. Netlink errors (transient) also requeue.
- **No finalizers**: ENC deletion triggers `cleanupAll` via the NotFound path. No external resources to clean up beyond the Pod's own network namespace.
- **OwnerReference validation**: The sidecar verifies the ENC's ownerReference points to its own Pod UID before acting. Prevents applying stale config from a previous Pod incarnation that shared the same name.
- **Separate ServiceAccount**: Uses its own ServiceAccount, distinct from controller-manager and stateless-load-balancer. No kubebuilder RBAC markers (would pollute shared `role.yaml` via `make manifests`).

### Resource Relationships

```
EndpointNetworkConfiguration (1:1 with Pod, same name)
├── spec.gateways[] → GatewayConnections
│   ├── name → Gateway name (used as table ID allocation key)
│   └── domains[] → NetworkDomains (max 2: IPv4 + IPv6)
│       ├── network.subnet → identifies target interface
│       ├── network.interfaceHint → optional fast-path interface name
│       ├── vips[] → VIP addresses to assign on the interface
│       └── nextHops[] → source-based routing next-hops
└── status.conditions[Ready] → Configured / ConfigurationFailed
```

**Kernel state managed per Pod:**
- VIP addresses on secondary interfaces (scoped per interface)
- Policy routing rules: `from <VIP> lookup <tableID>` (scoped per table)
- Default routes in per-Gateway tables: `default via <nextHops>` (ECMP)

**ECMP hashing:** Linux multipath routing uses Layer 3 hashing by default (`net.ipv4.fib_multipath_hash_policy=0`, `net.ipv6.fib_multipath_hash_policy=0`), so L4 port information is not included in the hash. This can result in poor distribution when many flows share the same source/destination IP pair. Setting the respective sysctl to `1` enables Layer 4 (5-tuple) hashing. The sidecar does not configure these sysctls — it is left to the cluster operator or Pod spec.

## Reconciliation Flow

### 1. Fetch ENC
- Return early if not found → `cleanupAll` (flush all tables, remove all VIPs)
- Verify ownerReference points to this Pod's UID — skip if not owned (stale ENC from previous Pod incarnation)
- Initialize `tableIDAllocator` and `managedVIPs` on first reconcile
- Default `netlinkOps` to real implementation if not injected (test hook)

### 2. Build Desired State (`buildDesiredState`)
- For each Gateway: allocate a table ID (stable per gateway name)
- For each Domain: resolve interface via `findInterfaceBySubnet`, parse VIPs and next-hops
- **On error**: update status to `ConfigurationFailed`. Content errors (invalid VIP, bad CIDR) return `nil` (no requeue — wait for ENC fix). `InterfaceNotFoundError` returns the error (requeue — interface may appear).

### 3. Apply State (`applyState`)

**VIPs (grouped by interface):**
- Aggregate all VIPs across domains per interface name
- `syncVIPs`: add missing, remove stale (within managed set only)
- Clean up VIPs on interfaces no longer in desired state

**Rules and routes (aggregated by table ID):**
- Aggregate all VIPs and next-hops across domains sharing a table ID
- This ensures removed domains within a gateway get stale rules cleaned up
- `syncRules`: add missing source-based policy rules, remove stale ones in managed range
- `syncRoutes`: replace default route with ECMP next-hops per IP family

**On error**: update status to `ConfigurationFailed`, return error (requeue for transient netlink errors)

### 4. Clean Stale Gateways
- Compare active gateway allocations against current ENC spec
- For removed gateways: `flushTable` (delete all routes and rules) + release table ID
- Handles the case where an entire gateway is removed from the ENC

### 5. Update Status
- Set `Ready=True` with reason `Configured` on success
- Set `Ready=False` with reason `ConfigurationFailed` on any error
- `ObservedGeneration` tracks which ENC spec version was processed
- Status update conflicts always requeue (`{Requeue: true}, nil`)

## Watch Triggers

| Resource | Trigger | Mapper Function | Filtering Strategy |
|----------|---------|-----------------|-------------------|
| EndpointNetworkConfiguration | Create/Update/Delete | Direct (`.For()`) | Cache scoped to single ENC via field selector on `metadata.name` |

The manager's cache is configured to watch only the single ENC matching the Pod name, minimizing API server load in clusters with many Pods.

## Status Conditions

### Ready Condition

**Type:** `Ready`

**Status:** `True` | `False`

**Reasons:**
- `Configured`: All VIPs, rules, and routes applied successfully
- `ConfigurationFailed`: Error in ENC content or netlink operation

**Lifecycle:**
1. ENC created → Reconcile triggered
2. Valid ENC, netlink succeeds → `True` (Configured)
3. Invalid ENC content (bad VIP, bad CIDR) → `False` (ConfigurationFailed), no requeue
4. Interface not found → `False` (ConfigurationFailed), requeue with backoff (transient)
5. Transient netlink error → `False` (ConfigurationFailed), requeue with backoff
6. ENC deleted → `cleanupAll`, no status update (resource gone)

**ObservedGeneration:** Tracks which ENC generation was processed

## Known Limitations: Restart Recovery

The controller keeps `tableIDs`, `managedVIPs`, and `nl` in memory. On restart (Pod restart, OOM kill, node reboot), all in-memory state is lost. The first reconcile after restart re-initializes these as empty, which causes several issues:

### Issues

| # | Problem | Severity |
|---|---------|----------|
| 1 | **VIP leak** | High |
| 2 | **Orphaned routes on table ID shift** | Medium |
| 3 | **Orphaned rules on table ID shift** | Medium |

Note: VIP re-add after restart is not an issue — `syncVIPs` tolerates `EEXIST` on add and `EADDRNOTAVAIL` on delete (see [Netlink Errno Tolerance](#netlink-errno-tolerance)).

#### Issue 1: VIP leak

`managedVIPs` is empty after restart, so VIPs from a previous run that are no longer desired are never removed — `syncVIPs` only removes VIPs within its managed set.

#### Issues 2 & 3: Orphaned routes and rules on table ID shift

Both share the same root cause: after restart the `tableIDAllocator` has no memory of previously-used table IDs, so tables from a previous run that aren't re-allocated become invisible orphans.

**Concrete scenario:**

Before restart:
- ENC has gateways `gw-a` and `gw-b`
- Allocator assigned: `gw-a → 50000`, `gw-b → 50001`
- Kernel: table 50000 has gw-a routes/rules, table 50001 has gw-b routes/rules

After restart, `gw-a` is removed from the ENC:
- Allocator starts fresh: `gw-b → 50000` (first and only allocation)
- Controller writes gw-b's routes into table 50000 via `RouteReplace`, overwriting gw-a's old routes — this is fine
- But table 50001 (gw-b's old table) is never allocated to any gateway
- `flushTable` only runs for gateways removed from the *current* ENC spec, and the allocator doesn't know 50001 was ever used
- `syncRules` only scans rules matching its own `tableID`, so rules pointing to 50001 are never cleaned
- Result: table 50001 retains stale routes and rules indefinitely

Table ID shift is a latent condition that builds up during normal operation through gateway add/remove cycles, but only manifests after a restart. For example: `gw-c → 50000`, `gw-d → 50001` are added first, then `gw-a → 50002`, `gw-b → 50003`. Later `gw-c` and `gw-d` are removed (properly cleaned, IDs 50000-50001 freed). Running state is now `gw-a → 50002`, `gw-b → 50003` — correct while the process is alive. After restart, the fresh allocator assigns `gw-a → 50000`, `gw-b → 50001`. Tables 50002 and 50003 are now orphaned with stale routes and rules.

A simpler variant: the gateway set shrinks while the sidecar is down (e.g. `gw-b` removed from the ENC during a restart). The running sidecar would have cleaned table 50001 via the stale gateway path, but since it wasn't running, the cleanup never happened. After restart, only `gw-a → 50000` is allocated — table 50001 retains gw-b's old routes and rules with no code path to reach it.

Note: a pure ID swap (same gateways, different assignment) is self-healing — `RouteReplace` overwrites old routes and `syncRules` cleans stale rules in all actively-allocated tables. The problem is only with *unallocated* tables that no gateway claims.

### Fix Strategy

The core challenge is restoring the in-memory `gatewayName → tableID` mapping and `managedVIPs` set after a sidecar container restart, without disrupting the collocated application container's traffic (VIPs remain on interfaces, routing rules intact in the kernel).

#### Approaches Considered

**1. Kernel scan (sweep everything on startup):**
Seed `managedVIPs` from kernel state (/32 and /128 addresses on secondary interfaces), scan rules in the managed table ID range, flush orphaned tables. Simple and correct, but causes a brief traffic disruption (VIPs removed and re-added in a single reconcile cycle) since the `gatewayName → tableID` mapping cannot be recovered from kernel state alone.

**2. nftables maps (`VIP → tableID`):**
Persist `VIP → tableID` in nftables maps (`ipv4_addr : mark`, `ipv6_addr : mark`). On restart, cross-reference with the current ENC to recover gateway-to-table mappings. The `google/nftables` Go library (already a dependency) supports this. However, VIPs are not stable gateway identifiers — if VIPs are reshuffled between gateways while the sidecar is down, the recovered mapping is incorrect.

**3. `emptyDir` volume (persist `gatewayName → tableID` file):**
Write a mapping file to an `emptyDir` mount on each reconcile. `emptyDir` survives container restarts within the same Pod (matching network namespace lifetime). On restart, read the file to seed the allocator. Simple and reliable, but the file is easier to tamper with than kernel-resident state.

**4. nftables maps with gateway name hashing:**
Store `hash(gatewayName) → tableID` in a `mark : mark` nftables map. Avoids the VIP instability problem, but adds hash collision risk and complexity.

No approach has been selected yet. The kernel scan approach (1) is simplest but causes brief disruption. The `emptyDir` approach (3) preserves traffic but relies on filesystem state. A hybrid (persist mapping + surgical diff) would be ideal but adds complexity.

**Status: Not yet implemented.** Required before production use.

## Error Handling

| Error Source | Example | Status Update | Requeue? | Rationale |
|-------------|---------|---------------|----------|-----------|
| `buildDesiredState` | Invalid VIP, bad CIDR, table ID exhausted | `ConfigurationFailed` | No | User must fix ENC |
| `buildDesiredState` | Interface not found | `ConfigurationFailed` | Yes | Transient, interface may appear |
| `applyState` | Netlink EPERM, ENOMEM, device not found | `ConfigurationFailed` | Yes | Transient, may resolve |
| Status update conflict | Concurrent ENC update | — | Yes | Standard optimistic concurrency |

### Netlink Errno Tolerance

`syncVIPs` tolerates specific kernel errors where the desired outcome is already achieved:

| Operation | Tolerated Errno | Kernel Constant | Rationale |
|-----------|----------------|-----------------|-----------|
| `AddrAdd` | `EEXIST` (errno 17) | `EEXIST` | Address already present on the interface — desired state achieved. |
| `AddrDel` | `EADDRNOTAVAIL` (errno 99) | `EADDRNOTAVAIL` | Address not found on the interface — desired state achieved. |

Both errnos are returned by the kernel's netlink address management (`inet_rtm_newaddr` / `inet_rtm_deladdr` in `net/ipv4/devinet.c`, `inet6_addr_add` / `inet6_addr_del` in `net/ipv6/addrconf.c`). The behavior is consistent across IPv4 and IPv6.

Note: the kernel uses `EADDRNOTAVAIL` ("Cannot assign requested address") rather than `ENOENT` for "address not on this interface" in the delete path. The `ip` CLI surfaces this as `RTNETLINK answers: Cannot assign requested address`.

This tolerance is essential for idempotent reconciliation and makes the controller restart-safe for VIP operations (see [Restart Recovery](#known-limitations-restart-recovery)).

## Configuration

### CLI Flags

| Flag | Env Var | Default | Description |
|------|---------|---------|-------------|
| `--pod-name` | `POD_NAME` | (required) | Pod name (Downward API) |
| `--pod-namespace` | `POD_NAMESPACE` | (required) | Pod namespace (Downward API) |
| `--pod-uid` | `POD_UID` | (required) | Pod UID (Downward API) |
| `--min-table-id` | `MERIDIO_MIN_TABLE_ID` | `50000` | Minimum routing table ID |
| `--max-table-id` | `MERIDIO_MAX_TABLE_ID` | `55000` | Maximum routing table ID |
| `--health-probe-bind-address` | `MERIDIO_PROBE_ADDR` | `:8082` | Health probe address |
| `--log-level` | `MERIDIO_LOG_LEVEL` | `info` | Log level |
| `--metrics-bind-address` | `MERIDIO_METRICS_ADDR` | `0` | Metrics endpoint (0 = disabled) |
| `--metrics-secure` | `MERIDIO_METRICS_SECURE` | `true` | HTTPS metrics |
| `--metrics-cert-path` | `MERIDIO_METRICS_CERT_PATH` | (empty) | Metrics TLS cert directory |
| `--enable-http2` | `MERIDIO_ENABLE_HTTP2` | `false` | HTTP/2 for metrics |

Precedence: CLI flags > Environment variables > Defaults

### RBAC Requirements

Managed separately from controller-manager (dedicated ServiceAccount per Pod):

```yaml
# Required ClusterRole/Role rules:
- apiGroups: ["meridio-2.nordix.org"]
  resources: ["endpointnetworkconfigurations"]
  verbs: ["get", "list", "watch"]
- apiGroups: ["meridio-2.nordix.org"]
  resources: ["endpointnetworkconfigurations/status"]
  verbs: ["get", "update", "patch"]
```

Container requires `NET_ADMIN` capability for netlink operations.

## Testing

### Manual Testing in Cluster

Two test scenarios exercise the sidecar controller. Part 1 tests the sidecar in isolation
with hand-crafted ENCs (no controller-manager or Gateway infrastructure needed). Part 2
tests the sidecar integrated with the full Gateway API stack.

---

### Part 1: Standalone Sidecar — Multi-Gateway via Hand-Crafted ENC

Tests the sidecar controller in isolation by simulating connectivity to two Gateways
through manually crafted ENC resources. No controller-manager, Gateway, or LB
infrastructure is required — only the CRDs, Multus, and the target application.

**Prerequisites:**
- Meridio-2 CRDs installed
- Multus + Whereabouts installed
- Namespace `meridio-2` exists
- Example target and network-sidecar images built and available

#### Setup: Deploy NADs and Target Application

```bash
cat <<'EOF' | kubectl apply -f -
---
apiVersion: v1
kind: Namespace
metadata:
  name: meridio-2
---
# NAD: first internal network (LB ↔ application)
apiVersion: k8s.cni.cncf.io/v1
kind: NetworkAttachmentDefinition
metadata:
  name: macvlan-internal
  namespace: meridio-2
spec:
  config: '{
      "cniVersion": "1.0.0",
      "name": "macvlan-internal",
      "plugins": [{
        "type": "macvlan",
        "master": "eth0",
        "mode": "bridge",
        "ipam": {
          "type": "whereabouts",
          "ipRanges": [
            {"range": "192.168.100.0/24"},
            {"range": "2001:db8:100::/64"}
          ]
        }
      }]
  }'
---
# NAD: second internal network
apiVersion: k8s.cni.cncf.io/v1
kind: NetworkAttachmentDefinition
metadata:
  name: macvlan-internal-2
  namespace: meridio-2
spec:
  config: '{
      "cniVersion": "1.0.0",
      "name": "macvlan-internal-2",
      "plugins": [{
        "type": "macvlan",
        "master": "eth0",
        "mode": "bridge",
        "ipam": {
          "type": "whereabouts",
          "ipRanges": [
            {"range": "192.168.200.0/24"},
            {"range": "2001:db8:200::/64"}
          ]
        }
      }]
  }'
EOF
```

```bash
helm install target-a examples/target/deployment/helm \
  -n meridio-2 \
  --set replicas=1 \
  --set-json 'networks=[{"name":"macvlan-internal","namespace":"meridio-2","interface":"net1"},{"name":"macvlan-internal-2","namespace":"meridio-2","interface":"net2"}]'
```

#### Test 1: Apply ENC with Two Gateways

```bash
TARGET_POD=$(kubectl get pods -n meridio-2 -l app=target-a \
  -o jsonpath='{.items[0].metadata.name}')
TARGET_POD_UID=$(kubectl get pod -n meridio-2 "$TARGET_POD" \
  -o jsonpath='{.metadata.uid}')

cat <<EOF | kubectl apply -f -
apiVersion: meridio-2.nordix.org/v1alpha1
kind: EndpointNetworkConfiguration
metadata:
  name: ${TARGET_POD}
  namespace: meridio-2
  ownerReferences:
    - apiVersion: v1
      kind: Pod
      name: ${TARGET_POD}
      uid: ${TARGET_POD_UID}
spec:
  gateways:
    - name: gw-a
      domains:
        - name: gw-a-v4
          ipFamily: IPv4
          network:
            subnet: "192.168.100.0/24"
            interfaceHint: "net1"
          vips: ["20.0.0.1", "20.0.0.2"]
          nextHops: ["192.168.100.253", "192.168.100.254"]
        - name: gw-a-v6
          ipFamily: IPv6
          network:
            subnet: "2001:db8:100::/64"
            interfaceHint: "net1"
          vips: ["2000::1", "2000::2"]
          nextHops: ["2001:db8:100::fffd", "2001:db8:100::fffe"]
    - name: gw-b
      domains:
        - name: gw-b-v4
          ipFamily: IPv4
          network:
            subnet: "192.168.200.0/24"
            interfaceHint: "net2"
          vips: ["30.0.0.1"]
          nextHops: ["192.168.200.254"]
        - name: gw-b-v6
          ipFamily: IPv6
          network:
            subnet: "2001:db8:200::/64"
            interfaceHint: "net2"
          vips: ["3000::1"]
          nextHops: ["2001:db8:200::fffe"]
EOF
sleep 3
```

#### Test 2: Verify Both Gateways Configured

```bash
# gw-a: VIPs on net1
kubectl exec -n meridio-2 "$TARGET_POD" -c example-target -- ip addr show net1
# Expected: 20.0.0.1/32, 20.0.0.2/32, 2000::1/128, 2000::2/128

# gw-b: VIPs on net2
kubectl exec -n meridio-2 "$TARGET_POD" -c example-target -- ip addr show net2
# Expected: 30.0.0.1/32, 3000::1/128

# Policy rules — each gateway gets its own table
kubectl exec -n meridio-2 "$TARGET_POD" -c example-target -- ip rule show
kubectl exec -n meridio-2 "$TARGET_POD" -c example-target -- ip -6 rule show
# Expected: "from 20.0.0.1 lookup 50000", "from 20.0.0.2 lookup 50000"
#           "from 30.0.0.1 lookup 50001"
#           (and IPv6 equivalents)

# Routes per table
kubectl exec -n meridio-2 "$TARGET_POD" -c example-target -- ip route show table 50000
kubectl exec -n meridio-2 "$TARGET_POD" -c example-target -- ip -6 route show table 50000
# Expected: "default nexthop via 192.168.100.253 ... nexthop via 192.168.100.254 ..." (ECMP)
#           "default nexthop via 2001:db8:100::fffd ... nexthop via 2001:db8:100::fffe ..." (ECMP)

kubectl exec -n meridio-2 "$TARGET_POD" -c example-target -- ip route show table 50001
kubectl exec -n meridio-2 "$TARGET_POD" -c example-target -- ip -6 route show table 50001
# Expected: "default via 192.168.200.254" / "default via 2001:db8:200::fffe"

# ENC status
kubectl get enc "$TARGET_POD" -n meridio-2 -o jsonpath='{.status.conditions[?(@.type=="Ready")]}'
# Expected: status=True, reason=Configured
```

#### Test 3: Remove a VIP

```bash
kubectl patch enc "$TARGET_POD" -n meridio-2 --type=json -p '[
  {"op":"replace","path":"/spec/gateways/0/domains/0/vips","value":["20.0.0.1"]},
  {"op":"replace","path":"/spec/gateways/0/domains/1/vips","value":["2000::1"]}
]'
sleep 3

kubectl exec -n meridio-2 "$TARGET_POD" -c example-target -- ip addr show net1
# Expected: only 20.0.0.1/32 and 2000::1/128 remain (20.0.0.2 and 2000::2 gone)

kubectl exec -n meridio-2 "$TARGET_POD" -c example-target -- ip rule show
# Expected: only "from 20.0.0.1 lookup 50000" (no 20.0.0.2 rule)

# gw-b on net2 unaffected
kubectl exec -n meridio-2 "$TARGET_POD" -c example-target -- ip addr show net2
# Expected: 30.0.0.1/32 and 3000::1/128 still present
```

#### Test 4: Remove Next-Hops from gw-b

```bash
kubectl patch enc "$TARGET_POD" -n meridio-2 --type=json -p '[
  {"op":"replace","path":"/spec/gateways/1/domains/0/nextHops","value":[]},
  {"op":"replace","path":"/spec/gateways/1/domains/1/nextHops","value":[]}
]'
sleep 3

# VIPs still present on net2
kubectl exec -n meridio-2 "$TARGET_POD" -c example-target -- ip addr show net2
# Expected: 30.0.0.1/32 and 3000::1/128 still assigned

# Rules for gw-b VIPs still present (table allocated, just no routes)
kubectl exec -n meridio-2 "$TARGET_POD" -c example-target -- ip rule show
kubectl exec -n meridio-2 "$TARGET_POD" -c example-target -- ip -6 rule show
# Expected: "from 30.0.0.1 lookup 50001" and "from 3000::1 lookup 50001" still present

# gw-b table has no routes (next-hops removed)
kubectl exec -n meridio-2 "$TARGET_POD" -c example-target -- ip route show table 50001
kubectl exec -n meridio-2 "$TARGET_POD" -c example-target -- ip -6 route show table 50001
# Expected: empty

# gw-a unaffected
kubectl exec -n meridio-2 "$TARGET_POD" -c example-target -- ip route show table 50000
# Expected: ECMP routes still intact
```

#### Test 5: Delete ENC — Verify Full Cleanup

```bash
kubectl delete enc "$TARGET_POD" -n meridio-2

# All VIPs removed from both interfaces
kubectl exec -n meridio-2 "$TARGET_POD" -c example-target -- ip addr show net1
kubectl exec -n meridio-2 "$TARGET_POD" -c example-target -- ip addr show net2
# Expected: only Whereabouts-assigned IPs remain on each

# All policy rules removed
kubectl exec -n meridio-2 "$TARGET_POD" -c example-target -- ip rule show
kubectl exec -n meridio-2 "$TARGET_POD" -c example-target -- ip -6 rule show
# Expected: no rules referencing table 50000 or 50001

# Both routing tables flushed
kubectl exec -n meridio-2 "$TARGET_POD" -c example-target -- ip route show table 50000
kubectl exec -n meridio-2 "$TARGET_POD" -c example-target -- ip route show table 50001
# Expected: empty or "Error: ipv4: FIB table does not exist"
```

#### Cleanup

```bash
helm uninstall target-a -n meridio-2
kubectl delete network-attachment-definitions macvlan-internal macvlan-internal-2 -n meridio-2
```

---

### Part 2: Integrated Test — Sidecar with Full Gateway API Stack

Tests the sidecar alongside the controller-manager, Gateway, DistributionGroup, and
L34Route resources. Single gateway, hand-crafted ENC (EndpointNetworkConfiguration
controller not yet implemented).

**Prerequisites:**
- Controller manager deployed
- Gateway API CRDs + Meridio-2 CRDs installed
- Multus + Whereabouts installed
- Namespace `meridio-2` exists
- Example target and network-sidecar images built and available

#### Setup: Deploy Infrastructure

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
  controllerName: registry.nordix.org/cloud-native/meridio-2/gateway-controller
---
apiVersion: k8s.cni.cncf.io/v1
kind: NetworkAttachmentDefinition
metadata:
  name: vlan-100
  namespace: meridio-2
spec:
  config: '{
      "cniVersion": "0.4.0",
      "name": "bridge-ext",
      "plugins": [{
        "type": "bridge",
        "bridge": "br-meridio",
        "vlan": 100,
        "ipam": {
          "type": "whereabouts",
          "ipRanges": [
            {"range": "169.254.100.0/24", "exclude": ["169.254.100.1/32"]},
            {"range": "100:100::/64", "exclude": ["100:100::1/128"]}
          ]
        }
      }]
  }'
---
apiVersion: k8s.cni.cncf.io/v1
kind: NetworkAttachmentDefinition
metadata:
  name: macvlan-internal
  namespace: meridio-2
spec:
  config: '{
      "cniVersion": "1.0.0",
      "name": "macvlan-internal",
      "plugins": [{
        "type": "macvlan",
        "master": "eth0",
        "mode": "bridge",
        "ipam": {
          "type": "whereabouts",
          "ipRanges": [
            {"range": "192.168.100.0/24"},
            {"range": "2001:db8:100::/64"}
          ]
        }
      }]
  }'
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
      description: "LB-to-endpoint internal network"
      nad:
        name: macvlan-internal
        namespace: meridio-2
        interface: net1
  networkSubnets:
    - attachmentType: NAD
      cidrs:
        - "192.168.100.0/24"
        - "2001:db8:100::/64"
  horizontalScaling:
    replicas: 2
    enforceReplicas: true
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
      app: target-a
  maglev:
    maxEndpoints: 32
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
    - name: test-dg
      group: meridio-2.nordix.org
      kind: DistributionGroup
  destinationCIDRs:
    - "20.0.0.1/32"
    - "20.0.0.2/32"
    - "2000::1/128"
    - "2000::2/128"
  protocols:
    - TCP
  priority: 1
EOF
```

```bash
helm install target-a examples/target/deployment/helm \
  -n meridio-2 \
  --set replicas=4 \
  --set-json 'networks=[{"name":"macvlan-internal","namespace":"meridio-2","interface":"net1"}]'
```

#### Test 1: Verify Gateway and LB Deployment

```bash
kubectl -n meridio-2 get gateway test-gateway \
  -o jsonpath='{.status.conditions[?(@.type=="Accepted")].status}'
# Expected: True

kubectl -n meridio-2 get deployment sllb-test-gateway
```

#### Test 2: Verify EndpointSlices

```bash
# Overview: name, address type, endpoints, network subnet
kubectl get endpointslices -n meridio-2 \
  -l meridio-2.nordix.org/distribution-group=test-dg \
  -o custom-columns=\
NAME:.metadata.name,\
ADDRESSTYPE:.addressType,\
ENDPOINTS:.endpoints[*].addresses[*],\
SUBNET:.metadata.labels.meridio-2\\.nordix\\.org/network-subnet

# Detailed: Maglev IDs per network subnet
kubectl get endpointslices -n meridio-2 \
  -l meridio-2.nordix.org/distribution-group=test-dg -o json | \
  jq -r '.items | group_by(.metadata.labels."meridio-2.nordix.org/network-subnet") |
    .[] |
    "Network: \(.[0].metadata.labels."meridio-2.nordix.org/network-subnet") (AddressType: \(.[0].addressType))",
    (.[].endpoints[] | "  \(.addresses) -> \(.zone // "no-zone")")'
```

#### Test 3: Apply ENC with Real LB Next-Hops

```bash
TARGET_POD=$(kubectl get pods -n meridio-2 -l app=target-a \
  -o jsonpath='{.items[0].metadata.name}')
TARGET_POD_UID=$(kubectl get pod -n meridio-2 "$TARGET_POD" \
  -o jsonpath='{.metadata.uid}')

# Collect LB pod net1 IPs for ECMP next-hops
LB_NET1_IPv4=()
LB_NET1_IPv6=()
for LB_POD in $(kubectl get pods -n meridio-2 -l app=sllb-test-gateway \
  -o jsonpath='{.items[*].metadata.name}'); do
  NET_STATUS=$(kubectl get pod -n meridio-2 "$LB_POD" \
    -o jsonpath='{.metadata.annotations.k8s\.v1\.cni\.cncf\.io/network-status}')
  LB_NET1_IPv4+=($(echo "$NET_STATUS" | jq -r '.[] | select(.interface=="net1") | .ips[] | select(contains(":") | not)'))
  LB_NET1_IPv6+=($(echo "$NET_STATUS" | jq -r '.[] | select(.interface=="net1") | .ips[] | select(contains(":"))'))
done

V4_HOPS=$(printf '            - "%s"\n' "${LB_NET1_IPv4[@]}")
V6_HOPS=$(printf '            - "%s"\n' "${LB_NET1_IPv6[@]}")

cat <<EOF | kubectl apply -f -
apiVersion: meridio-2.nordix.org/v1alpha1
kind: EndpointNetworkConfiguration
metadata:
  name: ${TARGET_POD}
  namespace: meridio-2
  ownerReferences:
    - apiVersion: v1
      kind: Pod
      name: ${TARGET_POD}
      uid: ${TARGET_POD_UID}
spec:
  gateways:
    - name: test-gateway
      domains:
        - name: test-gateway-v4
          ipFamily: IPv4
          network:
            subnet: "192.168.100.0/24"
            interfaceHint: "net1"
          vips: ["20.0.0.1", "20.0.0.2"]
          nextHops:
${V4_HOPS}
        - name: test-gateway-v6
          ipFamily: IPv6
          network:
            subnet: "2001:db8:100::/64"
            interfaceHint: "net1"
          vips: ["2000::1", "2000::2"]
          nextHops:
${V6_HOPS}
EOF
sleep 3
```

#### Test 4: Verify Network Configuration

```bash
kubectl exec -n meridio-2 "$TARGET_POD" -c example-target -- ip addr show net1
# Expected: 20.0.0.1/32, 20.0.0.2/32, 2000::1/128, 2000::2/128

kubectl exec -n meridio-2 "$TARGET_POD" -c example-target -- ip rule show
kubectl exec -n meridio-2 "$TARGET_POD" -c example-target -- ip -6 rule show
# Expected: "from 20.0.0.1 lookup 50000", "from 20.0.0.2 lookup 50000", etc.

# ECMP routes via both LB pods
kubectl exec -n meridio-2 "$TARGET_POD" -c example-target -- ip route show table 50000
kubectl exec -n meridio-2 "$TARGET_POD" -c example-target -- ip -6 route show table 50000
# Expected: "default nexthop via <LB1_IPv4> ... nexthop via <LB2_IPv4> ..."

kubectl get enc "$TARGET_POD" -n meridio-2 -o jsonpath='{.status.conditions[?(@.type=="Ready")]}'
# Expected: status=True, reason=Configured
```

#### Test 5: Delete ENC — Verify Cleanup

```bash
kubectl delete enc "$TARGET_POD" -n meridio-2

kubectl exec -n meridio-2 "$TARGET_POD" -c example-target -- ip addr show net1
# Expected: only Whereabouts-assigned IPs

kubectl exec -n meridio-2 "$TARGET_POD" -c example-target -- ip rule show
# Expected: no rules referencing table 50000

kubectl exec -n meridio-2 "$TARGET_POD" -c example-target -- ip route show table 50000
# Expected: empty or "Error: ipv4: FIB table does not exist"
```

#### Cleanup

```bash
helm uninstall target-a -n meridio-2
kubectl delete distributiongroup test-dg -n meridio-2
kubectl delete l34route test-route -n meridio-2
kubectl delete gateway test-gateway -n meridio-2
kubectl delete gatewayconfiguration test-gwconfig -n meridio-2
kubectl delete gatewayclass meridio-2
kubectl delete network-attachment-definitions vlan-100 macvlan-internal -n meridio-2
```

### Unit Tests

Tests use a `mockNetlink` implementation of the `netlinkOps` interface, enabling full controller testing without a real network namespace.

**Test coverage:**
- Reconcile: ENC not found, empty gateways, single gateway dual-stack, multiple gateways, multiple VIPs per interface, shared interface across gateways, ECMP multi-nexthop, next-hops removed (routes deleted, rules kept), ownerRef rejection (wrong UID/Kind/missing), invalid VIP/next-hop/interface, netlink errors, stale gateway cleanup, domain removal cleanup
- Status: success/error conditions, observedGeneration
- Network functions: interface discovery (hint/scan), VIP sync (add/remove/partial failure), rule sync, route sync, table flush
- Table ID allocator: sequential allocation, stable mapping, range exhaustion, freed ID reuse

## Implementation Files

```
internal/controller/sidecar/
├── controller.go          # Reconcile loop, buildDesiredState, applyState, cleanupAll, updateStatus
├── network.go             # findInterfaceBySubnet, syncVIPs, syncRules, syncRoutes, flushTable
├── netlink.go             # netlinkOps interface + defaultNetlinkOps (real netlink delegation)
├── tableid.go             # tableIDAllocator (gateway name → table ID mapping)
├── controller_test.go     # Unit tests (Reconcile, status, network functions)
├── mock_netlink_test.go   # mockNetlink implementation for testing
└── tableid_test.go        # Table ID allocator tests

cmd/network-sidecar/
├── main.go                # Binary entrypoint
└── cmd/
    ├── cmd.go             # Root command (run + version)
    ├── run.go             # Manager setup, cache scoped to single ENC
    └── version.go         # Version subcommand

internal/common/config/
└── sidecar.go             # SidecarConfig with CLI flags + env binding
```
