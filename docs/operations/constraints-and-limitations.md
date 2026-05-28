# Meridio-2 MVP Constraints and Limitations

This document covers the known constraints and limitations of the Meridio-2 MVP (Minimum Viable Product) release.

Items marked *(architectural constraint)* reflect deliberate design decisions that are unlikely to change. All other items are known limitations within the MVP scope, intended to be addressed in future releases.

## Architecture / Cross-Controller

**1. ~~Dual-stack internal networking partially supported (IPv4 + IPv6 simultaneously)~~ (Resolved)**

The DistributionGroup controller assigns Maglev IDs per DG per Gateway, shared across all networks (IPv4/IPv6). The LB controller accumulates IPs from both IPv4 and IPv6 EndpointSlices per identifier and creates policy routes for both families. See [#70](https://github.com/Nordix/Meridio-2/issues/70), [#144](https://github.com/Nordix/Meridio-2/issues/144).

**2. ~~RBAC uses ClusterRole instead of namespace-scoped Roles (controller-manager)~~ (Resolved)**

The controller-manager's RBAC is now split into a namespace-scoped Role (`manager-role.yaml`) for namespace-scoped resources and a minimal ClusterRole (`manager-clusterrole.yaml`) for cluster-scoped resources (GatewayClass).

**3. IP scraping relies on Multus network-status annotation, not kernel state** *(architectural constraint)*

The controller-manager determines Pod secondary network IPs from the `k8s.v1.cni.cncf.io/network-status` annotation written by Multus, not by inspecting the Pod's network namespace. This requires only API client access (no access to Pod network namespaces). As a consequence, manual changes to interface or address configuration within the kernel are not detected by the controller-manager.

**4. No metrics exposed**

No metrics are exposed by any component. Future metrics should include traffic-related metrics from LB Pods (NFQLB hit counts, target distribution) as well as operational metrics (BGP session state, route counts).

**5. Network subnet CIDRs must uniquely identify a single interface IP per Pod** *(architectural constraint)*

Each CIDR in `GatewayConfiguration.spec.internalSubnets` must match exactly one secondary interface IP within application Pods. The DistributionGroup controller uses these subnets to select the correct IP from Multus network-status annotations when building EndpointSlices — if multiple interfaces match, the selected IP is ambiguous. Similarly, the network sidecar discovers the target interface by matching the subnet against interface addresses, and would apply VIPs and routing rules to the wrong interface if multiple interfaces match.

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

## Router Controller

**10. ~~VIPs advertised regardless of LB distribution readiness~~ (Resolved)**

The router now gates VIP advertisement on LB readiness. The collocated LB controller writes `lb-ready-<distGroupName>` files to a shared directory when a DistributionGroup has ready targets. The router watches this directory via fsnotify and suppresses VIPs until at least one readiness file exists. Configurable via `--readiness-dir` / `MERIDIO_READINESS_DIR` (default: `/var/run/meridio`).

**11. No connectivity-based readiness signaling to the controller-manager**

The router only logs BGP state changes — it does not set Pod readiness gates or signal the controller-manager about external connectivity status. As a result, the ENC controller cannot react to a running LB Pod losing external connectivity. The network sidecar's next-hop list will not be updated when an LB Pod's BGP sessions go down, meaning application Pods may continue routing return traffic through an LB that has lost external connectivity.

**12. BIRD error propagation missing**

If BIRD crashes, the router controller continues running with healthy probes while doing nothing. BIRD failure is not propagated to the process lifecycle.

**13. BGP-learned routes may be delayed up to 60 seconds in kernel routing table**

The `kernel` protocol blocks in the generated bird.conf do not set `scan time`. BIRD 3.x's default is 60 seconds, which can delay BGP-learned routes from appearing in the kernel routing table by 30-50 seconds. Meridio v1 fixed this with `scan time 10`.

**15. ~~PMTU handling not implemented in LB Pods~~ (Resolved)**

PMTU handling is implemented: the LB controller creates a nftables PMTU SNAT chain at startup that rewrites ICMP Frag Needed / Packet Too Big source addresses to the VIP. Requires `fwmark_reflect` sysctls — see [Gateway controller docs](../controllers/gateway.md#sysctl-prerequisites-for-lb-pods).

**15. BGP authentication not supported**

The GatewayRouter CRD has no field for BGP MD5 or TCP-AO authentication. Meridio v1 supported this.

**16. Static routing with BFD not supported**

Only BGP-based GatewayRouter configuration is implemented. Static routing with BFD (as an alternative to BGP for simpler deployments) is not supported.

**17. BFD not fully restricted**

BFD source ports comply with the IANA-approved range (49152–65535) per RFC 5881 only when `net.ipv4.ip_local_port_range` is set to `49152 65535` in the LB Pod's network namespace. This can be achieved via a tuning NAD (e.g., the `sysctl-tuning` NAD example in [Gateway controller docs](../controllers/gateway.md#sysctl-prerequisites-for-lb-pods)) attached to the LB Pods through GatewayConfiguration. Without it, BIRD uses the kernel default range which violates the RFC. BFD is not restricted to single-hop mode port (3784), and BFD sessions are not restricted by configuration to the external interface(s) only.

## Sidecar Controller

**18. No sidecar restart recovery**

All in-memory state (`tableIDs`, `managedVIPs`) is lost on sidecar container restart. This causes VIP leaks (old VIPs not removed from the interface) and orphaned routing rules/tables when table IDs are reallocated differently after restart. Restarting the sidecar container/process is not recommended.

**19. Sidecar policy routing rule priority not configurable** *(architectural constraint)*

Source-based routing rules (`ip rule`) are created without an explicit priority. The kernel auto-assigns priorities just below the `main` table (32766), which produces correct ordering for the current use case. However, the priority is not configurable.

**20. Sidecar uses routing table ID range 50000–55000** *(architectural constraint)*

The network sidecar allocates kernel routing table IDs from the range 50000–55000 (one table per Gateway connection). This range must not overlap with routing tables used by other components in the Pod's network namespace. The range is configurable via `--min-table-id` / `--max-table-id` (or `MERIDIO_MIN_TABLE_ID` / `MERIDIO_MAX_TABLE_ID`).

## DistributionGroup Controller

**21. Default `maxEndpoints` per DistributionGroup is 32**

When `DistributionGroup.spec.maglev.maxEndpoints` is not set, the controller defaults to 32 endpoints. This is the current MVP default and may be revised. The equivalent parameter in Meridio v1 (`MaxTargets`) defaulted to 100.

**22. Node failure detection not implemented**

When a Node becomes NotReady, the controller does not immediately reconcile affected DistributionGroups to remove endpoints on that Node. Endpoint removal is delayed until the Pod deletion propagates, or Pod readiness changes.

## Deployment / Operations

**23. No runtime log level change**

Log level is set at startup via `--log-level` / `MERIDIO_LOG_LEVEL` and cannot be changed without restarting the process. All four controllers (controller-manager, router, loadbalancer, sidecar) share this limitation.

**24. ~~No cert-wait-timeout~~** *(resolved)*

The controller-manager now waits for TLS certificates before starting (default: 10s, maximum: 1m, configurable via `--cert-wait-timeout`). This avoids unnecessary restart cycles when deployed simultaneously with cert-manager.

**25. Minimum Kubernetes version 1.31** *(architectural constraint)*

Required by CEL CIDR/IP validation libraries used in CRD validation rules (`isCIDR()`, `cidr().prefixLength()`, `ip().family()`). For MVP, some CEL validations have been temporarily removed to allow running on older Kubernetes versions where test environments with 1.31+ were not available. This is a temporary workaround — the full CEL validations must be restored for production use.

**26. Upgrades not verified**

No upgrade path has been tested or documented. In-place upgrades of the controller-manager, LB Pods, or sidecar containers should be treated as untested. CRD schema changes, controller behavior changes between versions may cause disruption.

**27. Scaling not extensively verified**

Basic functionality has been tested with a small number of Gateways, DistributionGroups, and application Pods. Behavior at scale (many Gateways, large numbers of endpoints per DG, high Pod churn) has not been systematically verified. Dynamic scaling of LB Deployment replicas (via `GatewayConfiguration.spec.horizontalScaling` or HPA) has also not been extensively tested. Scaling may work but is best-effort for the MVP.

**28. Controller-manager multi-replica deployment not verified**

The controller-manager enables leader election by default in the deployment manifest (`--leader-elect`, using controller-runtime's lease-based leader election with ID `e9d059a3.nordix.org`), but running multiple replicas for high availability has not been tested.
