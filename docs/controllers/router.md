# Router Controller

## Overview

The Router controller runs inside the LB Pod's router container. It reconciles GatewayRouter CRs to configure BIRD BGP sessions and source-based policy routing for VIP traffic attraction.

## Architecture

### Deployment Model

- Runs as a sidecar container alongside the `stateless-load-balancer` container in each LB Pod
- Each instance is scoped to a single Gateway (receives `--gateway-name` and `--gateway-namespace` at startup)
- Multiple LB Pod replicas each run their own independent Router controller instance, all reconciling the same GatewayRouter CRs

### Resource Relationships

```
Gateway
├── status.addresses → VIPs (set by Gateway controller from L34Routes)
└── referenced by → GatewayRouter.spec.gatewayRef

GatewayRouter (cluster-scoped per Gateway namespace)
├── spec.gatewayRef → Gateway
├── spec.address → external router IP
├── spec.interface → network interface name
└── spec.bgp → BGP session parameters (ASN, ports, hold time, BFD)
```

### Reconcile Flow

```
1. Gateway or GatewayRouter change triggers reconcile
2. Filter: skip if Gateway name/namespace doesn't match this instance
3. Skip if Gateway is being deleted (DeletionTimestamp check)
4. Fetch Gateway → extract VIPs from status.addresses (IPAddressType only)
5. List GatewayRouters → filter by gatewayRef matching this Gateway
6. Call Bird.Configure(vips, routers) which internally:
   a. Install policy routing rules first (VIP source → BIRD kernel table)
   b. Write config to file (atomic: write tmp + rename)
   c. If BIRD is running: birdc configure (hot reload)
```

Policy routes are applied before BIRD reconfiguration to minimize the misrouting window. The blackhole fallback table (4097) catches VIP-sourced traffic while BGP routes converge.

**Cross-controller race**: A race exists between the controller manager exposing VIPs to application Pods (via EndpointNetworkConfiguration) and router controllers processing the same VIP change. An app Pod could start sourcing traffic from a new VIP before any router has set up policy rules for it. A potential defense-in-depth improvement would be static blackhole safety rules tied to the internal network interface, catching VIP-sourced traffic that hasn't been fully plumbed yet regardless of reconcile timing across controllers.

### Watch Strategy

- **Primary**: `Gateway` (For trigger)
- **Secondary**: `GatewayRouter` via `EnqueueRequestsFromMapFunc` → enqueues the owning Gateway

The mapper filters GatewayRouters by `spec.gatewayRef` (not labels), ensuring only relevant changes trigger reconciliation. When `gatewayRef.Namespace` is nil, it defaults to the GatewayRouter's own namespace for comparison.

## BIRD Integration

### Process Lifecycle

BIRD runs as a child process of the router binary, started via `exec.CommandContext`.

**Startup sequence**:
1. `Bird.Run()` starts in a goroutine before the manager
2. If no config file exists, writes an empty default config
3. Starts `bird -d` (foreground/debug mode)
4. Sets `running = true` after `cmd.Start()` returns (fork succeeded)

**Graceful shutdown**:
- On context cancellation, `cmd.Cancel` sends SIGTERM to BIRD
- BIRD receives SIGTERM → sends BGP NOTIFICATION (Cease) to peers → cleans up → exits
- If BIRD doesn't exit within `WaitDelay` (3s) → Go sends SIGKILL
- This ensures peers immediately remove routes rather than waiting for hold timer expiry

**Config application**:
- `Bird.Configure()` first installs policy routes via `setPolicyRoutes()`, then writes config to disk (atomic: tmp file + rename), then calls `birdc configure` if `running == true`
- If BIRD hasn't started yet (`running == false`), only writes the file — BIRD picks it up on startup
- Mutex protects concurrent access to config writes and the `running` flag

**Logging**:
- BIRD logs to a configurable file path via `--bird-log-file` flag (default: `/var/log/bird/bird.log`)
- The config template conditionally emits `log "<path>" all;` when a log file is set
- `log stderr all;` is always present as baseline
- The `Bird` struct uses the options pattern: `bird.New(bird.WithLogFile(cfg.BirdLogFile))`

**Interface**: `BirdInterface` allows mocking in tests (config generation, monitoring) without filesystem or process dependencies.

### Config Generation

BIRD config is generated using `text/template` and assembled from three parts:

1. **Base config**: Static filters, kernel protocol, BFD protocol, BGP template
2. **VIP statics**: `protocol static VIP4/VIP6` with routes via loopback
3. **Router protocols**: One `protocol bgp 'NBR-<name>'` per GatewayRouter

VIPs arrive as plain IPs from `Gateway.status.addresses` (filtered to `IPAddressType` only) and are converted to CIDRs (`/32` or `/128`) inside the BIRD package via `vipsToCidr()`.

### Policy Routing

Source-based routing ensures return traffic from VIPs uses BIRD-learned routes:

| Table | Purpose | Priority |
|---|---|---|
| 4096 | BIRD-managed BGP routes (`merge paths on`) | 100 |
| 4097 | Blackhole fallback (safety net) | 101 |

For each VIP:
- `ip rule add from <VIP>/32 lookup 4096 priority 100`
- `ip rule add from <VIP>/32 lookup 4097 priority 101`

Blackhole routes (`0.0.0.0/0` and `::/0`) in table 4097 prevent traffic leaking via the default routing table when no BGP routes exist.

Rules are reconciled idempotently: stale rules removed, missing rules added. Errors are accumulated best-effort (partial progress over rollback); the next reconcile retries any failed operations.

### BGP Monitoring

A separate goroutine polls `birdc show protocols all "NBR-*"` at 1-second intervals:

- Parses protocol state (up/down/start/idle) and BGP session info (Established)
- Tracks connectivity: at least one `NBR-*` protocol in state `up` with info `Established`
- Currently log-only; status changes logged when protocol up-count changes
- Failed birdc queries are logged at V(1) level for debugging

## Known Limitations and Future Work

### BIRD Process Lifecycle (MVP gaps)

- **No error propagation**: `Bird.Run()` goroutine logs errors but doesn't signal the main process. If BIRD crashes, the controller keeps running with green health probes while doing nothing useful. Fix: use `errCh` pattern to trigger context cancellation on BIRD failure.

- **Startup readiness window**: `running` is set to `true` after `cmd.Start()` (fork), not after BIRD's control socket is ready. A reconcile during this window will attempt `birdc configure` and fail. Self-heals via controller-runtime requeue, but produces noisy error logs at startup.

### Connectivity Signaling (post-MVP)

The monitoring goroutine will evolve from log-only to a data source for the controller manager:

- **Pod readiness gates**: Router sets per-IP-family readiness conditions (e.g., `ipv4-ready`, `ipv6-ready`) based on BGP session state
- **Controller manager consumes**: Watches LB Pods, reads readiness gates, builds next-hop lists for `EndpointNetworkConfiguration` from only Pods with relevant gates set to `True`
- **Why readiness gates, not CR status**: GatewayRouter is a shared cluster resource — N LB Pods reconcile the same CRs. Per-Pod connectivity state belongs on the Pod, not the CR.

### Configuration Externalization (post-MVP)

Several values are currently hardcoded and should be exposed via CLI flags / environment variables:

- **Monitoring interval**: Currently `1 * time.Second` in `monitorConnectivity()`
- **Kernel table IDs**: `defaultKernelTableID` (4096), `blackholeKernelTableID` (4097)
- **Rule priorities**: `rulePriority` (100), `blackholeRulePriority` (101)
- **BIRD paths**: Socket path (`/var/run/bird/bird.ctl`), config file (`/etc/bird/bird.conf`)
- **BGP defaults**: Default ports (`defaultLocalPort`, `defaultRemotePort` = 179), default hold time (90s) — currently constants in `config.go`
- **Kernel scan time**: The `kernel` protocol blocks have no `scan time` set. Meridio v1 hit a bug (PR #607) where BGP-learned routes were delayed 30-50s before being pushed to the kernel table after upgrading to BIRD 3. The cause was BIRD 3's new multi-threaded architecture not reliably syncing its internal routing tables to the kernel without periodic forced scans. Fixed by adding `scan time 10`. The same issue likely affects this controller — BGP routes learned from external peers may not appear in kernel table 4096 promptly, causing VIP traffic to hit the blackhole fallback. Should be added to the generated config with a configurable default (10s).
- **BIRD log level**: Currently `log stderr all;` + optional file logging. Should support selectable log classes and size rotation (as Meridio v1 does via `LogFilePath`, `BackupLogFilePath`, `BirdLogFileSize`).

### Metrics and Route Monitoring (post-MVP)

Meridio v1 exposes per-GatewayRouter metrics and monitors BIRD route counts:

- **Route count monitoring**: Periodic `birdc show route count` to track total routes in BIRD tables. Meridio v1 logs route count changes and rate-limits the output (see `stats.go`).
- **GatewayRouter metrics**: Per-GatewayRouter up/down state exposed via OpenTelemetry. Includes per-IP-family connectivity status. (In Meridio v1 these were called "gateway metrics" — the resource was renamed to GatewayRouter in Meridio-2 to avoid ambiguity with Gateway API's Gateway.)
- **Memory monitoring**: `birdc show memory` for BIRD memory usage tracking.
- These should be exposed as Prometheus metrics via the controller-runtime metrics server (already wired but unused).

### LB Readiness Gating (post-MVP)

MVP assumes the collocated LB is always ready. Future work:

- LB container signals distribution readiness (e.g., via a file or readiness gate)
- Router controller gates VIP exposure on LB readiness
- Refer to `WatchesRawSource` builder option for integrating non-CR event sources

### Feature Parity with Meridio v1

| Feature | Meridio-1 | This Controller | Notes |
|---|---|---|---|
| BGP config generation | ✅ | ✅ | Dual-stack, BFD, custom ports |
| Static routing protocol | ✅ | ❌ | CRD only has BGP (by design) |
| BGP authentication | ✅ | ❌ | Out of MVP scope |
| BIRD process lifecycle | ✅ | ✅ | Graceful shutdown via SIGTERM, file-based logging |
| BIRD startup readiness | ✅ | ❌ | Meridio-1 polls `birdc show status`; this controller relies on retry |
| BGP monitoring | ✅ (rich) | ✅ (basic) | Meridio-1: per-gateway per-IP-family tracking; here: simple up-count |
| Policy routing (VIP→table) | ✅ | ✅ | With blackhole fallback |
| BIRD log monitoring | ✅ | ⚠️ | File-based logging added; no stderr parsing or structured re-emission |
| Connectivity → health signal | ✅ | ❌ | Meridio-1 signals NSP; this controller only logs |
| Error propagation | ✅ | ❌ | Meridio-1 uses `errCh` pattern |
| Metrics | ✅ | ❌ | Meridio-1 has gateway metrics |
| Config generation method | `fmt.Sprintf` | `text/template` | Migrated for maintainability and future auth support |

## Testing

### Unit Tests

- `internal/controller/router/controller_test.go`: Reconciler logic (gateway filtering, GatewayRouter matching, VIP extraction, address type filtering, enqueue mapper, configure error propagation)
- `internal/bird/bird_test.go`: Config generation (base, VIPs, routers, full reference config comparison)
- `internal/bird/monitor_test.go`: Protocol output parsing, connectivity detection, status formatting, monitor channel lifecycle
