# Constraints and Limitations

This document covers the known constraints and limitations of the Meridio-2 release.

Items marked *(architectural constraint)* reflect deliberate design decisions that are unlikely to change. All other items are known limitations within the current release scope, intended to be addressed in future releases.

## Architecture / Cross-Controller

**- IP scraping relies on Multus network-status annotation, not kernel state** *(architectural constraint)*

The controller-manager determines Pod secondary network IPs from the `k8s.v1.cni.cncf.io/network-status` annotation written by Multus, not by inspecting the Pod's network namespace. This requires only API client access (no access to Pod network namespaces). As a consequence, manual changes to interface or address configuration within the kernel are not detected by the controller-manager.

**- Network subnet CIDRs must uniquely identify a single interface IP per Pod** *(architectural constraint)*

Each CIDR in `GatewayConfiguration.spec.internalSubnets` must match exactly one secondary interface IP within application Pods. The DistributionGroup controller uses these subnets to select the correct IP from Multus network-status annotations when building endpoint slices — if multiple interfaces match, the selected IP is ambiguous. Similarly, the network sidecar discovers the target interface by matching the subnet against interface addresses, and would apply VIPs and routing rules to the wrong interface if multiple interfaces match.

To avoid ambiguity, the default network (`0.0.0.0/0`, `::/0`) and `fe80::/10` link-local addresses are explicitly not accepted.

**- VIPs cannot be shared across Gateways** *(architectural constraint)*

Each VIP (defined in `L34Route.spec.destinationCIDRs`) must belong to exactly one Gateway. The L34Route API enforces a single `parentRef`, so a given L34Route — and its VIPs — is bound to one Gateway. Reusing the same VIP address in L34Routes attached to different Gateways is not supported and leads to undefined behavior, as multiple LB Deployments would attract and load-balance the same traffic independently. Additionally, if an application Pod joins both Gateways, the network sidecar would assign the same VIP on different interfaces with conflicting source-based routing rules, making return path selection ambiguous.

**- Only flat (L2) secondary networks are supported** *(architectural constraint)*

LB Pods and application Pods must share the same L2 broadcast domain on the internal secondary network. The LB controller forwards traffic to application Pods by routing directly to their secondary network IPs via next-hop, which requires L2 adjacency. Routed (L3) secondary networks between LB and application Pods are not supported.

## Gateway Controller

**- In-place Pod vertical scaling not implemented**

Resource changes in `GatewayConfiguration.spec.verticalScaling` trigger Pod recreation via RollingUpdate. In-place resize (zero downtime) requires the `InPlacePodVerticalScaling` feature gate and has RBAC security concerns.

## LB Controller

**- LB uses dynamic routing table/fwmark ranges starting at fwmarkBase+2** *(architectural constraint)*

The LB controller assigns fwmarks and routing table IDs dynamically per DistributionGroup. Each DG maps to one NFQLB instance — a named shared memory segment (`nfqlb init --shm=<dg-name>`) containing a Maglev hash table. The single `nfqlb flowlb` process reads all instances concurrently: when a packet arrives, nfqlb matches it to a flow, looks up the corresponding instance's hash table, selects a target slot, and sets the fwmark on the packet. Each instance gets a contiguous fwmark range of size `maxEndpoints` (from the DG's Maglev configuration, default 100). Ranges are allocated sequentially starting at `fwmarkBase+2` (default 5002 with `--fwmark-base=5000`) and packed without gaps — the allocator finds the first non-overlapping range based on actual `maxEndpoints` values of existing instances. The formula is `fwmark = dgOffset + endpoint_identifier`, where `dgOffset` is the per-DG base allocated by `getOffset()` (first DG gets `fwmarkBase+2`, next gets `fwmarkBase+2+maxEndpoints`, etc.) and `fwmark == tableID`. The first two values below the instance range (`fwmarkBase+0` and `fwmarkBase+1`) are reserved for drop-accounting (no-flow and no-targets marks used in nftables). This range must not overlap with other fwmark or routing table usage in the LB Pod's network namespace. At startup, the LB cleans up all stale policy routing rules with `mark >= fwmarkBase+2` from a previous instance. DG offset assignment is in-memory — different LB Pods may assign different offsets to the same DistributionGroup. This is acceptable as routing tables are local to each Pod.

**- L34Route CIDR count is limited to 32 per field by the nfqlb data plane** *(architectural constraint)*

`L34Route.spec.destinationCIDRs` and `spec.sourceCIDRs` allow `MaxItems=100` at the CRD level, but the data plane (nfqlb) accepts at most **32 CIDRs per field per flow** (`MAX_CIDRS` in nfqlb `src/lib/flow.c`). nfqlb's `parseCidrs()` rejects a longer list, which makes the `nfqlb flow-set` command fail — so the flow is **not programmed**. The limit is applied independently to the `--dsts` and `--srcs` lists, i.e. it is a per-field limit (32 destinationCIDRs and 32 sourceCIDRs separately).

To prevent silently non-functional configuration, the L34Route validating webhook rejects an L34Route with more than 32 entries in either CIDR field (constant `maxCIDRsPerFlow` in `internal/webhook/v1alpha1/l34route_webhook.go`, mirroring nfqlb's `MAX_CIDRS`). The CRD `MaxItems=100` is a higher ceiling that is intentionally left unchanged; the webhook is the load-bearing limit.

Raising the effective limit requires an **nfqlb change** (increase `MAX_CIDRS`) plus bumping the webhook constant — no CRD change is required.

**Planned/deferred:** raising the CRD `MaxItems` (e.g. to 1000) was considered but deferred; it would only widen the gap above the data-plane cap without increasing what actually works. Any such increase should follow (not precede) an increase of nfqlb's `MAX_CIDRS`.

**Related scaling considerations for VIPs (`destinationCIDRs`):**

- **nftables** imposes no practical limit — VIPs populate a dynamic named interval set matched by a single `ip daddr @vip-set` rule (O(1) per packet); it scales cleanly well beyond the nfqlb per-flow cap.
- **BGP is the practical ceiling for total VIPs per Gateway.** Each VIP is advertised as an individual `/32` or `/128` static route (see the router controller); host routes cannot be aggregated. The number of VIPs a Gateway can advertise is ultimately bounded by the **upstream BGP peer's max-prefix limit** (operator/peer configured). Exceeding it causes the peer to tear down the BGP session — a session-wide failure affecting all VIPs on that Gateway, and not detectable at admission. Note the nfqlb `MAX_CIDRS` limit is per L34Route flow, whereas the BGP/nftables cost scales with the union of all VIPs across the Gateway.

**- L34Route port count is bounded by the nfqlb data-plane port-string buffer (~85 entries)** *(architectural constraint)*

`L34Route.spec.sourcePorts` and `destinationPorts` accept up to `MaxItems=1000` at the CRD level, but the number of ports that actually take effect is smaller and bounded by the data plane. The LB controller passes a DistributionGroup's ports to nfqlb as a single comma-joined `--sports`/`--dports` value (see `internal/nfqlb` `Instance.AddFlow`), and nfqlb copies that value with `strndupa(str, 1024)` in `rangeSetAddStr()` (`src/lib/rangeset.c`) — anything beyond **1024 bytes is silently truncated in the data plane**. With the 11-byte-per-entry CRD item limit (`"65535-65535"`), roughly **85 port entries** fit before truncation.

To prevent silently-truncated configuration, the L34Route validating webhook rejects a port set whose comma-joined form exceeds the nfqlb buffer (constant `nfqlbPortStringMaxBytes` in `internal/webhook/v1alpha1/l34route_webhook.go`, mirroring nfqlb's `strndupa` size). `"any"` is excluded from this accounting because the controller does not serialize a full-range/any port set to nfqlb.

Raising the effective port capacity is a **data-plane change**: increase nfqlb's buffer and bump `nfqlbPortStringMaxBytes` together. No CRD change is required — the CRD `MaxItems` is deliberately a generous ceiling above this limit.

By contrast, `L34Route.spec.byteMatches` (`MaxItems=100`) has no fixed nfqlb data-plane cap; nfqlb stores matches in a dynamically-grown list. The `byteMatches` limit is a CRD-level ceiling only.

## Router Controller

**-**

## Sidecar Controller

**- Sidecar policy routing rule priority not configurable** *(architectural constraint)*

Source-based routing rules (`ip rule`) are created without an explicit priority. The kernel auto-assigns priorities just below the `main` table (32766), which produces correct ordering for the current use case. However, the priority is not configurable.

**- Sidecar uses routing table ID range 50000–55000** *(architectural constraint)*

The network sidecar allocates kernel routing table IDs from the range 50000–55000 (one table per Gateway connection). This range must not overlap with routing tables used by other components in the Pod's network namespace. The range is configurable via `--min-table-id` / `--max-table-id` (or `MERIDIO_MIN_TABLE_ID` / `MERIDIO_MAX_TABLE_ID`).

## DistributionGroup Controller

**- Node failure detection relies on Kubernetes Pod eviction** *(architectural constraint)*

When a Node becomes unreachable, the DG controller does not independently detect this. Endpoint removal (and Maglev ID reallocation) is deferred until Kubernetes evicts and deletes the Pod. With default tolerations this takes ~5m40s. Applications can control this via Pod `tolerationSeconds` for `node.kubernetes.io/not-ready` and `node.kubernetes.io/unreachable` taints (e.g., 30s for faster eviction). This is intentional: relying on Kubernetes Pod lifecycle prevents premature Maglev ID reallocation that could occur if the node is temporarily unreachable rather than permanently failed.

**- Direct and indirect Gateway references are treated as equivalent association sources** *(architectural constraint)*

A DistributionGroup can be associated with a Gateway two ways: directly via `DistributionGroup.spec.parentRefs`, or indirectly via an L34Route whose `backendRefs` point at the DG and whose `parentRefs` point at the Gateway. The DG controller resolves the union of both, deduplicated by object key, then keeps only Gateways with an `Accepted=True` condition set by this controller. Neither source is privileged over the other.

This equal handling is a deliberate design decision, chosen primarily to keep the architecture extensible toward supporting multiple Gateway associations per DG in the future, rather than baking in special validation for the direct `parentRefs` path that would later have to be unwound.

The implicit consequence is that an "invalid" direct `parentRef` is silently ignored rather than rejected: a `parentRef` naming a non-existent Gateway is dropped during resolution, and one naming a Gateway that is not Accepted by this controller is dropped by the Accepted filter. In either case the DG still functions if it has a valid indirect association — the unusable direct `parentRef` simply contributes nothing, and produces no error, event, or status condition. The one case where a stray `parentRef` changes behavior is when it resolves to an accepted Gateway *different* from the one reached indirectly: the DG then references more than one accepted Gateway and is frozen with `Available=False`, reason `MultipleGateways` (single-Gateway restriction). If it resolves to the same Gateway, deduplication collapses them and the redundant `parentRef` is harmless.

## Deployment / Operations

**- Minimum Kubernetes version 1.31** *(architectural constraint)*

Required by CEL CIDR/IP validation libraries used in CRD validation rules (`isCIDR()`, `cidr().prefixLength()`, `ip().family()`). Some CEL validations have been temporarily removed to allow running on older Kubernetes versions where test environments with 1.31+ were not available. This is a temporary workaround — the full CEL validations must be restored for production use.

**- Upgrades not verified**

No upgrade path has been tested or documented. In-place upgrades of the controller-manager, LB Pods, or sidecar containers should be treated as untested. CRD schema changes, controller behavior changes between versions may cause disruption.

**- Scaling not extensively verified**

Basic functionality has been tested with a small number of Gateways, DistributionGroups, and application Pods. Behavior at scale (many Gateways, large numbers of endpoints per DG, high Pod churn) has not been systematically verified. Dynamic scaling of LB Deployment replicas (via `GatewayConfiguration.spec.horizontalScaling` or HPA) has also not been extensively tested.
