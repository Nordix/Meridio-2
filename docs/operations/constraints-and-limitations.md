# Meridio-2 MVP Constraints and Limitations

This document covers the known constraints and limitations of the Meridio-2 MVP (Minimum Viable Product) release.

Items marked *(architectural constraint)* reflect deliberate design decisions that are unlikely to change. All other items are known limitations within the MVP scope, intended to be addressed in future releases.

## Architecture / Cross-Controller

**1. ~~Dual-stack internal networking partially supported (IPv4 + IPv6 simultaneously)~~ (Resolved)**

The DistributionGroup controller assigns Maglev IDs per DG per Gateway, shared across all networks (IPv4/IPv6). Initially the LB controller accumulated IPs from both IPv4 and IPv6 EndpointSlices per identifier and created policy routes for both families ([#70](https://github.com/Nordix/Meridio-2/issues/70), [#144](https://github.com/Nordix/Meridio-2/issues/144)). With the LoadBalancerEndpointSlice CRD ([#178](https://github.com/Nordix/Meridio-2/issues/178)), dual-stack addresses are co-located in a single endpoint entry — no cross-slice correlation needed.

**2. ~~RBAC uses ClusterRole instead of namespace-scoped Roles (controller-manager)~~ (Resolved)**

The controller-manager's RBAC is now split into a namespace-scoped Role (`manager-role.yaml`) for namespace-scoped resources and a minimal ClusterRole (`manager-clusterrole.yaml`) for cluster-scoped resources (GatewayClass).

**3. IP scraping relies on Multus network-status annotation, not kernel state** *(architectural constraint)*

The controller-manager determines Pod secondary network IPs from the `k8s.v1.cni.cncf.io/network-status` annotation written by Multus, not by inspecting the Pod's network namespace. This requires only API client access (no access to Pod network namespaces). As a consequence, manual changes to interface or address configuration within the kernel are not detected by the controller-manager.

**4. No metrics exposed**

No metrics are exposed by any component. Future metrics should include traffic-related metrics from LB Pods (NFQLB hit counts, target distribution) as well as operational metrics (BGP session state, route counts).

**5. Network subnet CIDRs must uniquely identify a single interface IP per Pod** *(architectural constraint)*

Each CIDR in `GatewayConfiguration.spec.internalSubnets` must match exactly one secondary interface IP within application Pods. The DistributionGroup controller uses these subnets to select the correct IP from Multus network-status annotations when building endpoint slices — if multiple interfaces match, the selected IP is ambiguous. Similarly, the network sidecar discovers the target interface by matching the subnet against interface addresses, and would apply VIPs and routing rules to the wrong interface if multiple interfaces match.

To avoid ambiguity, the default network (`0.0.0.0/0`, `::/0`) and `fe80::/10` link-local addresses are explicitly not accepted.

**6. VIPs cannot be shared across Gateways** *(architectural constraint)*

Each VIP (defined in `L34Route.spec.destinationCIDRs`) must belong to exactly one Gateway. The L34Route API enforces a single `parentRef`, so a given L34Route — and its VIPs — is bound to one Gateway. Reusing the same VIP address in L34Routes attached to different Gateways is not supported and leads to undefined behavior, as multiple LB Deployments would attract and load-balance the same traffic independently. Additionally, if an application Pod joins both Gateways, the network sidecar would assign the same VIP on different interfaces with conflicting source-based routing rules, making return path selection ambiguous.

**7. Only flat (L2) secondary networks are supported** *(architectural constraint)*

LB Pods and application Pods must share the same L2 broadcast domain on the internal secondary network. The LB controller forwards traffic to application Pods by routing directly to their secondary network IPs via next-hop, which requires L2 adjacency. Routed (L3) secondary networks between LB and application Pods are not supported.

## Gateway Controller

**8. In-place Pod vertical scaling not implemented**

Resource changes in `GatewayConfiguration.spec.verticalScaling` trigger Pod recreation via RollingUpdate. In-place resize (zero downtime) requires the `InPlacePodVerticalScaling` feature gate and has RBAC security concerns.

## LB Controller

**9. LB uses dynamic routing table/fwmark ranges starting at 5000** *(architectural constraint)*

The LB controller assigns fwmarks and routing table IDs dynamically per DistributionGroup. Each DG gets a contiguous range of size `maxEndpoints` (from the DG's Maglev configuration, default 100). Ranges are allocated sequentially starting at offset 5000 and packed without gaps — the allocator finds the first non-overlapping range based on actual `maxEndpoints` values of existing instances. The formula is `offset + endpoint_identifier`, where `fwmark == tableID`. This range must not overlap with other fwmark or routing table usage in the LB Pod's network namespace. At startup, the LB cleans up all stale rules with `mark >= 5000` from a previous instance. DG offset assignment is in-memory — different LB Pods may assign different offsets to the same DistributionGroup. This is acceptable as routing tables are local to each Pod.

**10. ~~DistributionGroups with only direct parentRefs not processed by LB~~ (Resolved)**

Fixed in PR #110. The `belongsToGateway` check now also inspects `DistributionGroup.spec.parentRefs` in addition to L34Route references.

## Router Controller

**10. ~~VIPs advertised regardless of LB distribution readiness~~ (Resolved)**

The router now gates VIP advertisement on LB readiness. The collocated LB controller writes `lb-ready-<distGroupName>` files to a shared directory when a DistributionGroup has ready targets. The router watches this directory via fsnotify and suppresses VIPs until at least one readiness file exists. Configurable via `--readiness-dir` / `MERIDIO_READINESS_DIR` (default: `/var/run/meridio`).

**11. ~~No connectivity-based readiness signaling to the controller-manager~~ (Resolved)**

~~The router now sets per-IP-family Pod readiness gates (`meridio-2.nordix.org/ipv4-connectivity`, `meridio-2.nordix.org/ipv6-connectivity`) based on BGP session state (PR #142). However, the ENC controller does not yet consume these gates to filter next-hop lists (gh-123). Until step 3 is implemented, application Pods may still route return traffic through an LB that has lost external connectivity.~~

The full three-step solution is now implemented: the gateway controller declares readiness gates (PR #125), the router sets gate conditions based on BGP state with damped transitions (PR #142), and the ENC controller filters next-hops using two-level checks — container readiness plus per-IP-family gate conditions (PR #155).

**12. ~~BIRD error propagation missing~~ (Resolved)**

The router controller uses an errgroup to run BIRD and the monitor goroutine. If either fails, the errgroup cancels the context and the router process exits, triggering Kubernetes CrashLoopBackOff restart.

**13. ~~BGP-learned routes may be delayed up to 60 seconds in kernel routing table~~ (Resolved)**

The BIRD configuration now includes `scan time` (default 10 seconds, configurable via `--bird-kernel-scan-time` / `MERIDIO_BIRD_KERNEL_SCAN_TIME`) in both IPv4 and IPv6 kernel protocol blocks.

**14. ~~PMTU handling not implemented in LB Pods~~ (Resolved)**

PMTU handling is implemented: the LB controller creates a nftables PMTU SNAT chain at startup that rewrites ICMP Frag Needed / Packet Too Big source addresses to the VIP. Requires `fwmark_reflect` sysctls — see [Gateway controller docs](../controllers/gateway.md#sysctl-prerequisites-for-lb-pods).

**15. BGP authentication not supported**

The GatewayRouter CRD has no field for BGP MD5 or TCP-AO authentication. Meridio v1 supported this.

**16. ~~Static routing with BFD not supported~~ (Resolved)**

Static routing is now supported via `spec.protocol: Static` in the GatewayRouter CRD. When configured, the router generates a BIRD static protocol block with a default route via the specified address/interface, supervised by BFD when `spec.static.bfd` is set.

**BFD interface parameter constraint:** BIRD allows only one set of BFD parameters per interface. When multiple static GatewayRouters share the same interface, the controller uses the BFD parameters from the first GatewayRouter alphabetically (by `.metadata.name`). Users must define the same BFD parameters (`minTx`, `minRx`, `multiplier`) for all static GatewayRouters on the same interface to avoid unexpected behavior.

**17. ~~BFD not fully restricted~~ (Resolved)**

BFD source ports comply with RFC 5881 (range 49152–65535) when `net.ipv4.ip_local_port_range` is set to `49152 65535` in the LB Pod's network namespace via sysctl configuration. One way to achieve this is through a tuning NAD (see [Gateway controller docs](../controllers/gateway.md#sysctl-prerequisites-for-lb-pods)). BFD sessions are restricted to directly connected peers (`accept direct`), which enforces single-hop BFD mode — the multi-hop BFD port (4784) is not opened. Sessions are further restricted to the configured external interface(s) per GatewayRouter.

## Sidecar Controller

**18. ~~No sidecar restart recovery~~ (Resolved)**

The sidecar implements restart recovery via hybrid approach: emptyDir persistence for table ID mappings and kernel VIP scanning. Primary issues (table ID shifts, VIP leaks on in-scope interfaces, orphaned routes/rules) are resolved. Some edge cases remain (VIP cleanup for removed-gateway exclusive interfaces). See [Sidecar Controller docs](../controllers/sidecar.md#restart-recovery) for details.

**19. Sidecar policy routing rule priority not configurable** *(architectural constraint)*

Source-based routing rules (`ip rule`) are created without an explicit priority. The kernel auto-assigns priorities just below the `main` table (32766), which produces correct ordering for the current use case. However, the priority is not configurable.

**20. Sidecar uses routing table ID range 50000–55000** *(architectural constraint)*

The network sidecar allocates kernel routing table IDs from the range 50000–55000 (one table per Gateway connection). This range must not overlap with routing tables used by other components in the Pod's network namespace. The range is configurable via `--min-table-id` / `--max-table-id` (or `MERIDIO_MIN_TABLE_ID` / `MERIDIO_MAX_TABLE_ID`).

## DistributionGroup Controller

**21. ~~Default `maxEndpoints` per DistributionGroup is 32~~ (Resolved)**

The CRD-level default for `maxEndpoints` has been removed. When the `maglev` block is specified, `maxEndpoints` must be set explicitly — the API server rejects `maglev: {}` without it. When the entire `maglev` block is omitted (DG defaults to type Maglev), the controller applies the built-in default of 102 (defined by `DefaultMaglevMaxEndpoints` in the API package).

**22. Node failure detection relies on Kubernetes Pod eviction** *(architectural constraint)*

When a Node becomes unreachable, the DG controller does not independently detect this. Endpoint removal (and Maglev ID reallocation) is deferred until Kubernetes evicts and deletes the Pod. With default tolerations this takes ~5m40s. Applications can control this via Pod `tolerationSeconds` for `node.kubernetes.io/not-ready` and `node.kubernetes.io/unreachable` taints (e.g., 30s for faster eviction). This is intentional: relying on Kubernetes Pod lifecycle prevents premature Maglev ID reallocation that could occur if the node is temporarily unreachable rather than permanently failed.

## Deployment / Operations

**23. ~~No runtime log level change~~ (Resolved)**

Log level can now be changed at runtime via an opt-in HTTP endpoint. Set `--log-level-api` / `MERIDIO_LOG_LEVEL_API` to a loopback address (e.g., `127.0.0.1:9901`) to enable the feature. The endpoint exposes GET/PUT on `/log/level` and is restricted to localhost only. Changes are ephemeral (reset on container restart). When the env var is empty (default), the feature is disabled and log level remains static as before.

**24. ~~No cert-wait-timeout~~ (Resolved)**

The controller-manager now waits for TLS certificates before starting (default: 10s, maximum: 1m, configurable via `--cert-wait-timeout`). This avoids unnecessary restart cycles when deployed simultaneously with cert-manager.

**25. Minimum Kubernetes version 1.31** *(architectural constraint)*

Required by CEL CIDR/IP validation libraries used in CRD validation rules (`isCIDR()`, `cidr().prefixLength()`, `ip().family()`). For MVP, some CEL validations have been temporarily removed to allow running on older Kubernetes versions where test environments with 1.31+ were not available. This is a temporary workaround — the full CEL validations must be restored for production use.

**26. Upgrades not verified**

No upgrade path has been tested or documented. In-place upgrades of the controller-manager, LB Pods, or sidecar containers should be treated as untested. CRD schema changes, controller behavior changes between versions may cause disruption.

**27. Scaling not extensively verified**

Basic functionality has been tested with a small number of Gateways, DistributionGroups, and application Pods. Behavior at scale (many Gateways, large numbers of endpoints per DG, high Pod churn) has not been systematically verified. Dynamic scaling of LB Deployment replicas (via `GatewayConfiguration.spec.horizontalScaling` or HPA) has also not been extensively tested. Scaling may work but is best-effort for the MVP.

**28. ~~Controller-manager multi-replica deployment not verified~~ (Resolved)**

Leader election is enabled by default in the deployment manifest (`--leader-elect`). Leader election tuning parameters are exposed (`--leader-elect-lease-duration`, `--leader-elect-renew-deadline`, `--leader-elect-retry-period`, `--leader-elect-release-on-cancel`). Pod tolerations for `node.kubernetes.io/not-ready` and `node.kubernetes.io/unreachable` are set to 30 seconds (reduced from default 300s) for faster failover on node failure. Multi-replica deployment with leader election has been verified.
