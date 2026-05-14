# Study: Maglev ID Allocation and Dual-Stack Support

**Issue:** [#70 — Maglev ID allocation must be shared within a DistributionGroup](https://github.com/Nordix/Meridio-2/issues/70)

---

## Problem Statement

*This section describes the original problem that motivated this study.*

The DistributionGroup (DG) controller assigned Maglev IDs independently per network/CIDR.
Each call to `assignMaglevIDs()` operated on a per-CIDR subset of Pods, meaning the same
Pod could receive different Maglev IDs in the IPv4 and IPv6 EndpointSlices for the same DG.
Furthermore, different Pods could be assigned the same Maglev ID in different EndpointSlices.

The LoadBalancer (LB) controller creates a single NFQLB shared-memory hash table per DG.
When it iterated EndpointSlices, it built a target map keyed by identifier
(`newTargets[identifier] = endpoint.Addresses`). Since this was a plain map assignment,
the last-processed slice overwrote the previous entry. This caused:

- **Non-deterministic routing**: Race of EndpointSlice updates; order of items after
  list operation was undefined.
- **Cross-Pod identifier collision**: Pod-A got `maglev:3` in IPv4, Pod-B got `maglev:3`
  in IPv6 — one overwrote the other.
- **Broken target removal**: Deactivating an identifier removed it for all IP families.

Creating multiple NFQLB hash tables per DG (e.g., one per IP family) is not viable. NFQLB
uses user-defined selectors and priorities to steer packets to hash tables, and these
selectors correspond to DG-level traffic classification criteria visible to external packets.
Splitting a DG into sub-tables would require fine-grained selector criteria based on
cluster-internal details (IP family, network attachment) that are not meaningful at the
external traffic classification level.

---

## Prior Art

### Meridio v1

The NSP server handled identifier allocation centrally. A Pod registered with all its IPs
(IPv4 + IPv6) under a single identifier. The LB learned the target from NSP and created
fwmark routes for each IP under the same fwmark/routing table. IPv4 and IPv6 routing tables
are independent in the Linux kernel, so this works without conflict.

The NSP used a keepalive mechanism: Pods periodically re-registered (default refresh cycle
~1s, timeout 60s configurable via `NSP_ENTRY_TIMEOUT`). If a Pod stopped refreshing (e.g.,
node failure), NSP removed the target after the timeout. This provided faster failure
detection (~60s) compared to Kubernetes Pod eviction (~5m40s with default tolerations).

Key design principle: IP connectivity was a prerequisite for identifier allocation, and one
identifier covered all IP families for a given Pod.

### Meridio 2.x POC

The POC followed the same principle as v1: it built separate IPv4 and IPv6 EndpointSlices,
merged them by Pod UID into a single view (`MergeEndpointSlices`), assigned identifiers once
on the merged set, then split back into per-family slices (`SplitEndpointSlices`) preserving
the shared identifiers.

---

## Design Dimensions

The following aspects influence the design space for Maglev ID allocation. Each option
in this document makes different trade-offs across these dimensions.

### DG-to-Gateway cardinality

Can a DistributionGroup be linked to multiple Gateways?

- **Single Gateway per DG** (current `parentRefs maxItems=1`): Simplifies everything —
  no Gateway context needed on EndpointSlices, no cross-Gateway coordination.
- **Multiple Gateways per DG**: Requires encoding Gateway context on EndpointSlices (or
  in DG status), and handling shared vs exclusive EndpointSlice organization. Users can
  achieve the same effect with multiple DGs using the same label selector.
- **Indirect multi-Gateway**: Even with `parentRefs maxItems=1`, a DG can indirectly serve
  multiple Gateways if multiple L34Routes from different Gateways reference it as a
  backendRef. This must be detected and handled.

### Maglev ID scoping

At what level are Maglev IDs assigned? This is related to but independent of DG-to-Gateway
cardinality and EndpointSlice organization — cardinality determines the relationship model,
slice organization determines how data is structured, and ID scoping determines where IDs
are unique.

- **Per DG, per network**: Independent allocation per CIDR. Same Pod can get different IDs
  across IP families. This was the initial implementation, which was broken due to the
  NFQLB architecture (single hash table per DG cannot handle different IDs for the same
  Pod across IP families).
- **Per DG**: One ID per Pod UID across all networks within a DG. Requires merging Pod
  lists across CIDRs before assignment. If the DG serves multiple Gateways, all Gateways
  share the same ID assignments.
- **Per DG, per Gateway**: Each Gateway gets its own independent ID space for the same DG.
  Different Gateways can assign different IDs to the same Pod. Only relevant if
  multi-Gateway DGs are to be supported.

### Namespace scoping

Can DGs and Gateways reside in different namespaces?

- **Same namespace** (current LB implementation): LB cache scoped to `GatewayNamespace`.
  DGs, L34Routes, EndpointSlices, and Gateway all in the same namespace.
- **Cross-namespace**: DG in namespace B, Gateway in namespace A. OwnerReferences cannot
  cross namespace boundaries (Kubernetes constraint). Labels, DG status-based discovery,
  or a custom resource with explicit `gatewayRef` field could address this.
- **Cluster-wide controller-manager**: The controller process can manage objects across
  all namespaces (just API calls). The namespace constraint is on ownerReference
  relationships and LB cache scoping, not on the controller process itself.

### EndpointSlice organization

How are EndpointSlices structured and associated with Gateways? This is independent of
Maglev ID scoping — the same ID scoping can be implemented with different slice
organizations.

- **Per network only** (current): One set of slices per CIDR, no Gateway dimension.
- **Per Gateway, per network** (exclusive): Separate slices per (DG, Gateway) pair, even
  if Gateways share the same network. Duplicate data but simple and independent.
- **Shared across Gateways**: Single slice serves multiple Gateways when they share a
  network. Fewer objects but requires multi-Gateway association metadata on slices.

### LB discovery mechanism

How does the LB find the EndpointSlices relevant to its Gateway?

- **Label-based** (current): Filter by `distribution-group` label.
- **Gateway labels on EndpointSlices**: Fixed or dynamic label keys encoding Gateway
  name/namespace. Subject to label length limits and multi-value challenges.
- **Gateway ownerReferences**: Limited to same-namespace. GC semantics complicate dual
  ownership.
- **DG status index**: DG status lists EndpointSlices and their Gateway associations.
  Extensible, cross-namespace capable, supports gradual migration.

### Endpoint representation format

What object type carries endpoint data?

- **Kubernetes EndpointSlice** (current): Core type, fixed schema. Single `addressType`
  per slice forces separate IPv4/IPv6 slices. `Zone` field abused for Maglev IDs.
- **Custom resource**: Purpose-built schema with dual-stack addresses, explicit Maglev ID
  field, native Gateway references.

### Network degradation policy

What happens when a Pod's network state degrades? See "Network Degradation and Activation
Policy" section for full analysis.

- **Gatekeeper for new Pods**: Only add Pods with all expected IPs.
- **Existing Pod degradation**: DG keeps Pod in remaining slices (truthful mirror); LB
  decides activation policy (deactivate entirely or keep working family).

### Upgrade and backward compatibility

How do schema changes propagate across independently deployed components? See Appendix
for full analysis.

- Cannot simultaneously have arbitrary rollback, zero consumer code debt, and independent
  deployment ordering.

---

## Chosen Direction

Restrict each DG to a single Gateway. Merge all network contexts (IPv4/IPv6) within the DG
before Maglev ID assignment, producing one ID per Pod UID. The LB accumulates IPs per
identifier across EndpointSlices.

Proposed complementary mechanisms (explored in later sections, not necessarily implemented):
- DG gatekeeper: only add new Pods when they have all expected IPs
- LB activation policy: skip incomplete new targets, deactivate degraded existing targets
- NetworkConsistency condition: alert operators to partial network presence

**Pros:**
- Simplest model — no Gateway dimension on EndpointSlices
- No Gateway label or ownerReference needed; all EndpointSlices owned by a DG serve its
  single Gateway
- EndpointSlice naming unchanged: `<dg>-<hashCIDR>-<index>`
- Maglev IDs are just per-DG
- Future-proof for cross-namespace: if the LB is extended to watch other namespaces, it
  finds the DG and all its EndpointSlices are unambiguously for its Gateway. No label
  disambiguation needed.
- The `parentRefs` field already has `maxItems: 1` in the CRD
- Users who need the same Pods behind multiple Gateways create multiple DGs with the same
  selector — trivial, explicit, no hidden coupling

**Indirect multi-Gateway detection:**

A DG can indirectly serve multiple Gateways if multiple L34Routes from different Gateways
reference the same DG as a backendRef (the `parentRefs maxItems=1` only restricts the direct
reference). The DG controller handles this by:

1. Resolving all Gateways for the DG (direct parentRef + indirect via L34Routes)
2. If more than one Gateway is found: set `Ready=False, Reason=MultipleGatewaysDetected,
   Message="DistributionGroup is referenced by multiple Gateways; only a single Gateway
   is supported"` and skip reconciliation. Existing EndpointSlices are preserved (no
   teardown — avoids disrupting traffic if the conflict is transient or accidental).
3. If exactly one Gateway: proceed normally
4. If zero Gateways: existing behavior (no EndpointSlices, status reflects no referenced
   Gateways)

**Trade-off — asymmetric network presence:**

With shared allocation, a Pod that has IPs in only a subset of the DG's networks still
consumes an ID slot globally. For example, if Pod-A has only an IPv4 address and gets
`maglev:5`, that ID is reserved across all networks — no other Pod can use `maglev:5` in
the IPv6 EndpointSlice. Pod-A simply won't appear in the IPv6 slice.

In the NFQLB hash table, `maglev:5` is still a valid entry. When an IPv6 packet hashes to
a bucket pointing to identifier 5, the LB sets the corresponding fwmark, but no IPv6 route
exists in that routing table. The packet gets dropped — a localized blackhole for that hash
bucket in IPv6.

Severity depends on the degree of asymmetry:
- **All Pods dual-stack** (expected case): No impact. Every identifier has routes for both
  families.
- **One Pod missing IPv6 out of 32**: ~3% of IPv6 hash buckets blackhole. Maglev distributes
  buckets roughly evenly, so the impact is proportional to the fraction of Pods missing from
  that family.
- **Disjoint sets** (e.g., 16 IPv4-only + 16 IPv6-only): ~50% blackholing in each family.
  Severe misconfiguration.

This is an inherent trade-off of a single hash table serving multiple IP families. In
practice, Pods selected by a DG should have identical network attachments (same Deployment
template, same NAD annotations), making asymmetry an operator misconfiguration. The DG
controller could detect this and surface a status condition warning (e.g.,
`NetworkConsistency=False`) when Pod sets diverge across networks (see [#141](https://github.com/Nordix/Meridio-2/issues/141)).

**Verdict:** Chosen direction. Minimal complexity, solves the dual-stack problem,
future-proof for cross-namespace.

---

## Alternatives Considered

### Option A: Per-IP-family NFQLB hash tables

Split each DG into an IPv4 and IPv6 NFQLB instance, with L34Route flows split per IP version.

**Pros:**
- No change to DG controller's per-CIDR allocation model
- Each hash table is internally consistent
- Natively supports DG-to-multi-Gateway linkage — EndpointSlices are scoped per network
  only, with no Gateway context needed. The LB splits them by IP family internally.

**Problems:**
- Doubles the number of NFQLB hash tables and associated flows
- Per-packet overhead in nfqlb's matching path (more selectors to evaluate per packet).
  The whole point of Maglev is one hash decision per packet; doubling tables adds runtime
  cost for every packet rather than a one-time configuration cost.
- NFQLB uses user-defined selectors and priorities tied to DG-level traffic classification.
  Splitting a DG into sub-tables would require fine-grained selector criteria based on
  cluster-internal details (IP family) that are not meaningful at the external traffic
  classification level.
- L34Routes can match both IPv4 and IPv6 packets while referencing a single DG — splitting
  would require duplicating or rewriting route logic on the nfqlb level.

**Verdict:** Not desirable. Adds per-packet runtime overhead instead of one-time
configuration overhead.

### Option B: Encoding Gateway context on EndpointSlices

B1 and B2 encode Gateway context directly on EndpointSlices (via labels or ownerRefs),
differing in whether slices are exclusive to a Gateway or shared across multiple Gateways.

#### B1: Exclusive per-Gateway EndpointSlices

Each EndpointSlice belongs to exactly one Gateway. The DG controller maintains separate
EndpointSlices per (DG, Gateway) pair, even if two Gateways share the same internal network.

**Gateway association:** Two fixed-key labels on each EndpointSlice:
- `meridio-2.nordix.org/gateway-name: <name>`
- `meridio-2.nordix.org/gateway-namespace: <namespace>`

No multi-value problem since each slice serves exactly one Gateway. LBs filter by both
labels to find their slices.

Alternatively, ownerReferences can encode the Gateway relationship (see common issues
below), with the same namespace constraint.

**Pros:**
- Natively supports DG-to-multi-Gateway setup — each Gateway gets its own set of slices
  with independent Maglev IDs
- Supports DG and Gateway in different namespaces without label length limitations — two
  fixed-key labels with namespace and name as values (label values allow up to 63
  characters each, no need to encode both into a single key)
- Simple labeling — fixed keys, no dynamic label names
- ID allocation naturally scoped per Gateway
- No cross-Gateway coordination needed
- Adding/removing a Gateway from a DG doesn't reshuffle IDs for other Gateways

**Cons:**
- Duplicate EndpointSlices when Gateways share internal networks (same Pod IPs, same
  network, but separate slices with potentially different Maglev IDs per Gateway)
- More objects to manage (proportional to number of Gateways per DG)
- EndpointSlice naming must ensure uniqueness across Gateways, e.g.,
  `<dg>-<gw>-<hashCIDR>-<index>` or an alternative convention that prevents collisions
  (see general naming consideration below)

#### B2: Shared EndpointSlices with multi-Gateway association

A single EndpointSlice can serve multiple Gateways when they share the same internal
network. The slice carries association metadata indicating which Gateways it belongs to.

**Gateway association — labeling challenges:**

Labels are flat key-value pairs. Encoding a one-to-many relationship (EndpointSlice →
multiple Gateways) doesn't map cleanly:

- A single label key `meridio-2.nordix.org/gateway: <name>` can only hold one value, so
  it cannot represent multiple Gateways on a shared EndpointSlice
- Dynamic label keys like `meridio-2.nordix.org/gateway-<namespace>-<name>: ""` work for
  multiple Gateways (one label per Gateway) but are unusual, length-limited (63 chars
  after prefix), and harder to query
- Cross-namespace Gateways compound the problem — the label key must encode both namespace
  and name

**Pros:**
- Fewer EndpointSlice objects when Gateways share internal networks
- Single source of truth for endpoint data per network

**Cons:**
- Complex labeling for multi-Gateway association
- Sharing an EndpointSlice across Gateways forces Maglev ID allocation to cross Gateway
  boundaries: if any internal network is shared between two Gateways, the shared slice
  must use the same IDs for both, which means ID allocation can no longer be independent
  per Gateway. This applies regardless of IP family mode (IPv4-only, IPv6-only, or
  dual-stack). In dual-stack mode, if only one family's network is shared while the other
  is separate, the constraint is amplified — the shared family's IDs dictate allocation
  for the non-shared family too (the LB expects one ID per Pod across all families),
  pulling more networks into the cross-Gateway coordination scope.
- Updating a shared slice affects all Gateways using it — larger blast radius
- Dynamic label keys are harder to query and validate

#### Common issues for B1 and B2

**OwnerReference approach (alternative to labels):**

Adding the Gateway as a second owner on EndpointSlices was considered but has fundamental
problems:
- GC requires all owners to be gone before deleting dependents — DG deletion alone won't
  clean up EndpointSlices if Gateway still exists
- Kubernetes requires ownerReferences to point to objects in the same namespace — cross-
  namespace Gateway-to-DG linking is impossible via ownerReferences
- LBs still need DG context first, so ownerReference is an additional filter, not a
  replacement for the DG-first discovery flow

**Upgrade and backward compatibility:**

Introducing Gateway markings on EndpointSlices (labels or ownerRefs) creates upgrade
challenges: old and new LBs coexist during rollout, and must handle both marked and
unmarked slices. Three mitigations were considered:
- *Fallback*: New LBs fall back to old behavior if markings absent. Fragile for multi-
  Gateway scenarios where old format is ambiguous.
- *Versioning*: Version label on slices tells LBs which semantics to apply. Risk limited
  to upgrade window.
- *CM-migrates-first*: CM augments existing slices with new markings before LB rollout.
  Requires cleanup step; rollout ordering cannot be controlled.

All three accumulate backward-compatibility debt over time. See Appendix for the general
schema evolution framework and fundamental trade-off.

#### Option B verdict

B1 and B2 add complexity for multi-Gateway DG support. B1 (exclusive per-Gateway slices)
is simpler than B2 (shared slices), which forces cross-Gateway Maglev ID coordination when
any internal network is shared. Both require Gateway association metadata directly on
EndpointSlices and face upgrade challenges when introducing or changing the marking scheme.

Multi-Gateway DG support has limited practical benefit: users who need the same Pods behind
multiple Gateways can create separate DGs (one per Gateway) with the same `spec.selector`,
achieving the same effect without any of the complexity above. This is why the
single-Gateway restriction was selected for the current implementation.

### Option C: DG status-based discovery

An alternative to encoding Gateway context on EndpointSlices (Option B). Instead of
marking EndpointSlices with labels or ownerReferences, the DG's `status` field serves as
a discovery index, listing which EndpointSlices exist and which Gateways they serve.
Option C supports both exclusive and shared EndpointSlice organization internally — the
DG controller can change its strategy without affecting how LBs discover slices.

The DG controller maintains this index as part of its normal status updates.

**Example:**
```yaml
status:
  endpointSlices:
    - name: dg-1-abc123-0
      gateways:
        - name: gw-a
          namespace: ns-1
    - name: dg-1-def456-0
      gateways:
        - name: gw-a
          namespace: ns-1
        - name: gw-b
          namespace: ns-2
```

The LB would: get DG → read `status.endpointSlices` → filter entries by its own Gateway
→ fetch those specific EndpointSlices by name.

**Pros:**
- No labels or ownerReferences needed for Gateway association on EndpointSlices
- DG status is a custom resource — full schema control, no Kubernetes API constraints
- Supports multi-Gateway and cross-namespace natively (status can reference Gateways in
  any namespace — no ownerReference same-namespace constraint)
- No label key length limits or dynamic key conventions
- Agnostic to EndpointSlice organization — natively supports both exclusive (one Gateway
  per entry) and shared (multiple Gateways per entry) approaches. The DG controller can
  change its internal strategy without affecting how LBs discover slices. The status is
  the stable contract.
- No GC interference (status is data, not an ownership relationship)
- LB discovery is a direct lookup by name (no list+filter on EndpointSlices)
- Dynamic expansion supported naturally — adding/removing Gateways, EndpointSlice splits
  due to capacity, network changes all handled through the normal reconcile cycle. The
  status is rebuilt from actual state each reconcile.
- Extensible — the DG status can be extended with new fields (e.g., schema version,
  Maglev ID extraction hints, per-slice metadata) without changing EndpointSlice format.
  New fields don't break old consumers (they ignore unknown fields), and new consumers
  can fall back when fields are absent. This makes Option C the most upgrade-resilient
  discovery mechanism.
- Can serve as a universal discovery layer across different endpoint representations. By
  including a `kind` and `apiGroup` per entry, the DG status can direct LBs to fetch
  EndpointSlices, a future custom resource (see Option D), or a mix of both. This
  decouples the LB from the underlying endpoint representation entirely. See
  "Combining Option C with Option D" below for details on how this enables gradual
  migration.

**Cons:**
- Unconventional pattern — using status as a discovery index rather than purely
  informational reporting. Not prohibited, but unusual. No known Kubernetes project uses
  status as a lookup table for owned resources; the standard pattern is labels +
  ownerReferences. There may be undocumented reasons why this pattern hasn't been adopted
  in the ecosystem.
- DG controller must keep status in sync with actual EndpointSlices — another thing to
  reconcile (though the DG controller already maintains status)
- Transient staleness: the DG controller updates EndpointSlices and status in separate
  API calls, so there's a brief window where the status references an EndpointSlice that
  doesn't exist yet (or still references a deleted one). LBs must handle NotFound
  gracefully. Self-healing on next DG reconcile.
- Status update frequency increases with the number of EndpointSlices and Gateways —
  every EndpointSlice change triggers a status update on the DG, increasing conflict
  probability and API server load. Practically manageable (status entries are small,
  ~200 bytes each), but a consideration at scale.
- Reduced motivation with a custom resource (Option D): Option C's primary value is for
  the EndpointSlice world where custom fields cannot be added. With a custom resource,
  `gatewayRef` and `distributionGroup` fields can be added directly to the resource and
  queried via field selectors — making the DG status index unnecessary for discovery.
  Option C retains value only for gradual migration scenarios (see Combining Option C
  with Option D).

**Verdict:** Option C avoids the labeling and ownerReference challenges of B1/B2 by moving
Gateway association to the DG status. It is a discovery mechanism rather than an
EndpointSlice organization strategy — it can be combined with either exclusive slices
(B1-style) or shared slices (B2-style). However, shared slices retain the cross-Gateway
Maglev ID coordination problem regardless of how discovery works, making Option C combined
with exclusive slices the natural combination. Option C is the most extensible and
upgrade-resilient discovery mechanism, but introduces an unconventional pattern (status as
discovery index) and adds status update overhead.

### Option D: Custom EndpointSlice replacement

Replace Kubernetes EndpointSlices with a dedicated Meridio-2 custom resource designed for
the specific requirements of the system.

**Motivation:** Many of the challenges in the selected approach and Options A and B stem
from EndpointSlice being a Kubernetes core type with a fixed schema. The `addressType`
field forces separate slices per IP family. The `Zone` field is abused to carry Maglev IDs.
There is no Gateway association field. Labels and ownerReferences are the only extensibility
mechanisms, each with limitations. A purpose-built resource eliminates these constraints.

**Example schema:**
```yaml
apiVersion: meridio-2.nordix.org/v1alpha1
kind: LoadBalancerEndpointSlice
metadata:
  name: dg-1-abc123-0
  namespace: default
  ownerReferences:
    - apiVersion: meridio-2.nordix.org/v1alpha1
      kind: DistributionGroup
      name: dg-1
      controller: true
spec:
  distributionGroup: dg-1
  gatewayRefs:                    # cross-namespace, following Gateway API conventions, native multi-Gateway support
    - name: gw-a
      namespace: ns-1
    - name: gw-b
      namespace: ns-2
  endpoints:
    - podRef:
        name: app-pod-1
        uid: abc-123
      addresses:
        - ip: "192.168.1.10"
          family: IPv4
        - ip: "2001:db8::10"
          family: IPv6
      maglevID: 5                 # optional, explicit field
      ready: true
    - podRef:
        name: app-pod-2
        uid: def-456
      addresses:
        - ip: "192.168.1.11"
          family: IPv4
        - ip: "2001:db8::11"
          family: IPv6
      maglevID: 12
      ready: true
```

**Pros:**
- **Dual-stack in a single object**: Both IPv4 and IPv6 addresses per endpoint (as a list
  with explicit family field), eliminating the split-slice race condition entirely. No
  inter-API-call window for the LB to worry about.
- **Explicit Maglev ID field**: No Zone field abuse. The field is optional, supporting
  future distribution types that don't use Maglev.
- **Native Gateway references**: `gatewayRefs` with namespace support, following Gateway
  API conventions. Supports multi-Gateway DGs and cross-namespace linking without labels
  or ownerReference hacks.
- **Full schema control**: Can be extended with new fields (network context metadata,
  per-endpoint hints) without Kubernetes API constraints. Additive field changes are the
  standard CRD evolution approach.
- **Capacity splitting supported**: Multiple objects per DG when endpoint count exceeds a
  single object's practical capacity (etcd size limits, watch event size). The "Slice"
  naming convention signals this. A Pod's dual-stack addresses always reside in the same
  object — splitting is by endpoint count, not by IP family.
- **Cleaner LB consumption**: LB reads one object type with all information needed —
  addresses, IDs, readiness. No multi-slice correlation for IP families, no Zone parsing.
- **Field-selector-based discovery**: The `spec.distributionGroup` field can be indexed
  for server-side filtering, avoiding the 63-character label value limit that constrains
  label-based discovery. Works with arbitrary-length DG names.

**Cons:**
- **New CRD to maintain**: Additional API surface, documentation, validation, versioning.
- **No ecosystem tooling**: Kubernetes tooling (kubectl, dashboards) understands
  EndpointSlices natively. A custom resource requires custom tooling or printer columns
  for visibility.
- **Migration from EndpointSlices**: If introduced after the initial release, requires the
  same upgrade challenges as any schema change (see upgrade discussion above). Best
  introduced early.
- **LB controller changes**: Must be rewritten to consume the new type instead of
  EndpointSlices. The DG controller must produce the new type instead of EndpointSlices.
- **Consumer impact**: Any current or future component that reads endpoint data would need
  to understand the new type. However, the per-consumer-type architecture (see
  `meridio2-endpoint-producer-consumer-architecture.md`) addresses this by design — each
  consumer class gets a tailored resource, so the `LoadBalancerEndpointSlice` is the LB's
  dedicated contract, not a generic endpoint store that all consumers must parse.

**Verdict:** The cleanest long-term solution. Eliminates the root cause of most problems
discussed in this document (split slices, Zone abuse, Gateway association). The cost is a
new CRD and migration effort. Best introduced early before EndpointSlice usage is deeply
entrenched. Can be combined with Option C (DG status index) for Gateway discovery, gradual
migration between endpoint representations, and as a universal discovery layer that
decouples LBs from the underlying endpoint format (see Combining Option C with Option D below).

#### Combining Option C with Option D: Gradual migration

*Note: This section is relevant only if Option D is introduced after EndpointSlices are
already in production use. If Option D is adopted from the start (the recommended path),
no migration is needed and this section is purely informational.*

Option C's status-based discovery can serve as the bridge between EndpointSlices and a custom
resource, enabling gradual migration without a hard cutover. The DG status index would
include type information per entry, allowing the DG controller to maintain both old and
new representations simultaneously during the transition:

```yaml
status:
  endpointGroups:
    - preferred:                          # new format, used by upgraded LBs
        name: dg-1-abc123-0
        kind: LoadBalancerEndpointSlice
        apiGroup: meridio-2.nordix.org
      legacy:                             # old format, kept for non-upgraded LBs
        - name: dg-1-ipv4-0
          kind: EndpointSlice
          apiGroup: discovery.k8s.io
        - name: dg-1-ipv6-0
          kind: EndpointSlice
          apiGroup: discovery.k8s.io
      gateways:
        - name: gw-a
          namespace: ns-1
```

**Migration flow:**

The mechanism works regardless of upgrade ordering (CM first or LBs first):
- New CM produces both formats (custom resource + EndpointSlices) and populates the status
  with `preferred`/`legacy` entries linking them.
- Old LBs continue using label-based EndpointSlice discovery (unaffected by status changes).
- New LBs read the DG status: use `preferred` if present, fall back to label-based
  EndpointSlice discovery if not.
- Post-upgrade cleanup: CM stops producing `legacy` entries and deletes old EndpointSlices.

**Key properties:**
- Ordering doesn't matter — `preferred` is only populated when the new-format object
  exists; absence means "use old path"
- Old LBs never read the DG status — completely unaffected
- The CM bears the cost of dual-write during migration

**Limitations:**
- New LBs need interpretation code for both old and new formats during migration (LB code
  debt). Option C makes discovery explicit but doesn't eliminate interpretation logic.
- Rollback requires old-format EndpointSlices to still exist (skip cleanup step before
  rolling back). If already cleaned up, the old CM must regenerate them from scratch.
- See Appendix for the general schema evolution trade-off framework.

---

## GatewayConfiguration: Proposed Schema Improvements

### Consider adding ipFamily field

Consider adding an explicit `ipFamily` field (`IPv4` / `IPv6`) to each InternalSubnet entry:
- Makes intent explicit — no guessing from CIDR format
- Enables CEL validation for duplicate detection and cross-validation
- Consistent with `NetworkDomain.ipFamily` in the EndpointNetworkConfiguration API

### Schema Tightening

**Enforce limits:**
- Max 2 `InternalSubnet` entries (one per IP family)
- Exactly 1 CIDR per `InternalSubnet`

**CEL validation rules:**
- Each InternalSubnet must represent a different IP family

**Why max 1 CIDR per InternalSubnet:**

The architecture requires flat L2 networks (constraint #7). Multiple CIDRs in a single
InternalSubnet would imply multiple L2 segments or routed subnets, which are not supported.
If someone needs to operate on the primary network, they must use Multus + NAD to create
an overlay achieving a flat internal network. External traffic attraction is fundamentally
different from what Kubernetes offers for primary networks.

**Why max 2 InternalSubnets (one per IP family):**

Future attachment types (beyond NAD) may require different configuration per IP family.
Keeping separate entries per family preserves this flexibility. But within a single family,
only one subnet makes sense given the flat L2 requirement.

---

## LB Controller: Explored Changes

*This section documents explored directions for the LB controller.*

### Accumulate IPs per Identifier

The original code used a plain map assignment (`newTargets[identifier] = endpoint.Addresses`)
which caused last-writer-wins when the same identifier appeared in multiple EndpointSlices.
The fix is to accumulate addresses across slices for the same identifier, or to classify
them by IP family using the EndpointSlice's `AddressType`.

### Route per IP Family

The original code created one route per fwmark using only `ips[0]`. The fix is to create
routes for all IPs under the identifier. IPv4 and IPv6 routing tables are independent in
the Linux kernel, so a single fwmark can have both an IPv4 and IPv6 route without conflict.

### IP Family Awareness

*This topic is deferred to [#128](https://github.com/Nordix/Meridio-2/issues/128).*

The LB needs awareness of which IP families it should expect per target to:
- Mitigate race conditions when IPv4 and IPv6 endpoint data arrives non-atomically
  (avoid premature activation with incomplete routing)
- Set up blackhole safety nets for fwmarked traffic that has no target route
  (prevent packets from leaking to the main routing table)
- Handle mode transitions (cleanup stale routes when a family is removed)

How this awareness is provided (startup config, DG status field, endpoint resource field)
remains an open question. Requiring the LB to resolve GatewayConfiguration directly was
considered but conflicts with the "dumb consumer" principle — the LB should process
whatever endpoints it receives without understanding the upstream configuration chain.

---

## Network Degradation and Activation Policy (explored)

*This section explores how the system should handle partial network presence and endpoint
readiness. These are proposals — no decisions have been committed.*

### The Fundamental Tension

Maglev's value is connection stickiness — once a flow is assigned to a target, it stays
there. Withdrawing a target from the hash table reshuffles ~1/N of all buckets, disrupting
existing connections for other Pods. Keeping a broken target preserves stickiness for
everyone else but blackholes traffic for that slot.

The key distinction is: **can traffic physically reach the target?**
- If no (IP gone, route impossible) → deactivate (correctness, no alternative)
- If yes but Pod may be unhealthy (readiness change) → keep (stability, minimize blast
  radius)

**Trade-off:** Localized blackhole for one target's buckets vs distributed disruption
across all targets' connections (twice) for a transient event.

For long-lived connections (the primary Maglev use case), the conservative approach
(ignore readiness, keep target in hash table) is preferable — it sacrifices new connections
to the failed target but preserves all existing connections to healthy targets.

This trade-off is specific to consistent hashing. For round-robin (future distribution
type), deactivating a target has no cascading effect — there's no hash table to reshuffle.

### Degradation Scenarios

**Partial IP family loss** (Pod loses one IP family):
- The route for the lost family physically cannot work — no IP to route to
- Keeping the target means guaranteed blackhole for that family's traffic in that bucket
- Deactivating causes one reshuffle but redirects traffic to targets that can serve it

**Single-network degradation** (Pod loses its only IP/interface):
- Same as above — no route possible

**Pod readiness change** (PodReady=False, but IPs still present):
- The route still exists — traffic might still work
- Deactivating causes two reshuffles (deactivate + reactivate on recovery), disrupting
  other targets' connections twice
- Keeping it means new connections to that target may fail, but all other targets are
  unaffected

**GatewayConfiguration change** (CIDR changed or removed):
- An operational decision, not a failure

**Practical likelihood:** With static secondary network configuration (Multus thin plugin),
losing an interface or IP on a running Pod is extremely unlikely — interfaces are created
at Pod startup and persist for the Pod's lifetime. The realistic scenarios are Pod startup
(IP not yet assigned), config changes, and node failure (Pod stays "ready" with stale
status until Kubernetes eviction ~5m40s with default tolerations). Note: Multus thick
plugin supports dynamic interface changes at runtime, which would make degradation
scenarios more realistic. Currently, only static secondary configuration is supported.

### DG Controller Role

The DG controller currently acts as a truthful mirror of Pod network state — it sets the
`Ready` field based on actual Pod readiness and includes Pods in EndpointSlices based on
their actual IPs, without policy decisions about partial presence.

**Gatekeeper proposal:** The DG controller could restrict adding *new* Pods to
EndpointSlices until they have IPs in all configured InternalSubnets. This would prevent
wasting Maglev ID slots on partially-configured Pods and ensure the LB never sees a
brand-new identifier with incomplete families. Existing Pods that lose a family would
remain in the slices where they still have IPs. Like the LB's activation policy, this
behavior could be policy-driven per DG if required in the future.

**NetworkConsistency condition proposal** (see [#141](https://github.com/Nordix/Meridio-2/issues/141)):
The DG controller could surface a status condition when any Pod has partial network
presence, alerting operators without taking disruptive action.

### LB Controller Role

The LB manages the data plane and would be responsible for activation/deactivation
decisions based on endpoint state.

**Possible policy for new targets** (identifier not active in hash table):
- If all expected IP families are present: activate with routes for all families
- If incomplete: skip activation until complete

**Possible policy for existing targets** (identifier already active):
- If a family was lost: deactivate entirely (correctness over stability)
- If all families present: update routes as needed
- If Pod readiness changed but IPs unchanged: ignore (for Maglev)

**Policy-driven approach:** Both readiness and network degradation handling could be
policy-driven per DG. The LB would ship with sensible defaults (deactivate on degradation,
ignore readiness for Maglev) that can be overridden via an optional policy field on the DG
CRD if demand arises. This keeps the initial implementation simple while allowing per-DG
tuning later (e.g., keep degraded targets for a DG where partial service is preferable to
reshuffling). The DG CRD can be extended with the policy block without breaking existing
DGs.

**Policy should be data-driven, not code-driven:** The policy should be explicit in
configuration, not implicit in the LB code version. This ensures all LB replicas behave
consistently regardless of software version during rolling upgrades.

### Alternative: Graceful Degradation (keep working family)

An alternative where the LB keeps a degraded target active for the remaining family, only
removing the broken family's route. The DG controller would also participate: removing the
Pod only from the broken family's EndpointSlice while preserving its Maglev ID in the
remaining family's slice.

**Pros:**
- No disruption to the working family's traffic
- No Maglev reshuffling for the remaining family
- Finer granularity than v1's all-or-nothing approach

**Cons:**
- Silent blackhole for the missing family's traffic — harder to diagnose
- Requires the DG controller to make policy decisions about partial presence
- Operator mistakes would not disrupt the working family but the broken family's
  blackhole could go unnoticed

Both approaches are viable. The simpler approach (deactivate entirely) has cleaner failure
semantics. Graceful degradation could be revisited if operational experience shows that
partial family loss is common and reshuffling impact is unacceptable.

---

## Same-Namespace Restriction

### Current State

The LB controller's cache is scoped to `GatewayNamespace` via
`cache.Options.DefaultNamespaces` in `cmd/stateless-load-balancer/cmd/run.go`. All list
operations use `client.InNamespace(c.GatewayNamespace)`. DGs, L34Routes, EndpointSlices,
and the Gateway are all expected to reside in the same namespace.

The `endpointSliceEnqueue` mapper explicitly rejects events from other namespaces:
```go
if obj.GetNamespace() != c.GatewayNamespace {
    return nil
}
```

The DG's `ParentReference` type includes an optional `Namespace` field, allowing
cross-namespace Gateway references at the API level. However, the LB controller cannot
discover DGs or EndpointSlices outside its own namespace.

### Recommendation

Document as a current implementation limitation, not an architectural constraint. The
`ParentReference.Namespace` field is preserved for future use.

The path to cross-namespace support is clear:
1. LB finds relevant DGs via L34Routes (which already resolve cross-namespace backendRefs
   — the code checks `backendRef.Namespace` and compares against `distGroup.Namespace`)
2. For each DG, the LB knows the DG's namespace from the backendRef
3. It lists EndpointSlices in that namespace filtered by the DG name label
4. The only changes needed: extend the LB's cache scope and replace hardcoded
   `InNamespace(c.GatewayNamespace)` calls with the DG's actual namespace

The single-Gateway restriction ensures EndpointSlices are unambiguous regardless of
namespace topology — all slices owned by a DG serve exactly one Gateway.

Note: DGs are namespace-scoped and their label selector only matches Pods in the same
namespace. So even with cross-namespace DG-to-Gateway references, application Pods must
reside in the DG's namespace. This mirrors how Kubernetes Services select Pods only within
their own namespace.

**OwnerReference constraint for cross-namespace:** If multi-Gateway DG support were added
via Gateway ownerReferences on EndpointSlices (see Option B), this would only work when
the Gateway is in the same namespace as the DG and its EndpointSlices. Kubernetes requires
ownerReferences to point to objects in the same namespace as the dependent. A cluster-wide
controller-manager can manage objects across namespaces (it's just API calls), but the
ownerReference relationship itself is namespace-scoped. This means cross-namespace
Gateway-to-DG linking via ownerReferences is fundamentally impossible — labels or another
discovery mechanism would be required.

---

## EndpointSlice Cleanup on DG Deletion

### Current Behavior

When a DG is deleted and the reconciler is triggered (via `.Owns()` watch on EndpointSlices),
the `r.Get()` call returns NotFound. The current code calls `client.IgnoreNotFound(err)` and
returns nil — it does **not** actively delete EndpointSlices. Cleanup relies entirely on
Kubernetes GC via the DG's ownerReference (`controller: true`).

The `deleteAllOwnedSlices` function is only called when the DG exists but has zero matching
Pods — it is not part of the deletion path.

### Improvement idea

Add active cleanup on the NotFound path using label-based lookup. The reconcile request
carries `req.Name` and `req.Namespace` (the DG's identity), and EndpointSlices have the
`meridio-2.nordix.org/distribution-group: <name>` label:

```go
if err := r.Get(ctx, req.NamespacedName, &dg); err != nil {
    if apierrors.IsNotFound(err) {
        // DG gone — clean up orphaned EndpointSlices by label
        return r.deleteSlicesByLabel(ctx, req.Namespace, req.Name)
    }
    return ctrl.Result{}, err
}
```

This is a defense-in-depth improvement that works regardless of the ownership model. It
ensures cleanup even if GC is delayed, and it would be essential if a future change adds
additional owners (e.g., Gateway) that would prevent GC from acting when only the DG is
deleted.

---

## EndpointSlice Naming and Length Limits

Kubernetes object names are limited to 253 characters, and label values are limited to 63
characters. Since DG names are used as label values on EndpointSlices
(`meridio-2.nordix.org/distribution-group: <dg-name>`), the practical DG name limit is 63
characters — not 253. This is currently an implicit limitation, not enforced at the CRD/API
level. Gateway names would be subject to the same limitation if used as label values (e.g.,
in Option B1).

With this constraint, the current naming convention (`<dg>-<hashCIDR>-<index>`) stays well
under 253 characters. Multi-Gateway conventions (e.g., `<dg>-<gw>-<hashCIDR>-<index>` as
discussed in Option B1) would also fit comfortably (~140 chars with two 63-char names).

For robustness, a truncate-and-hash approach can be used:
`<truncated-prefix>-<hash(full-inputs)>-<index>`. The hash guarantees uniqueness regardless
of truncation. This is a general best practice but not strictly necessary given the 63-char
label constraint on input names.

**Note on Option D (custom resource):** With a custom resource replacing EndpointSlices,
the DG name would be stored in a spec field (e.g., `spec.distributionGroup`) rather than a
label, removing the 63-character label value limitation. However, the object naming
challenge remains — multiple custom endpoint objects may coexist per DG (due to capacity
splitting), requiring a naming convention that ensures uniqueness across slices.

---

## Multi-Gateway DG Support

If multi-Gateway DG support is needed in the future, see Option B for EndpointSlice-based
approaches and Option D (custom resource replacing EndpointSlices) which is the intended
mid-term direction regardless of multi-Gateway needs. Option C (DG status-based discovery)
was explored as a possible migration bridge between EndpointSlices and Option D.

---

## Note on Data Footprint vs Simplicity

The core Kubernetes EndpointSlice controller creates separate EndpointSlices per Service,
even when two Services select the exact same Pods. There is no deduplication or sharing
across Services — each Service owns its own set of slices independently. Kubernetes chose
clear ownership and simplicity over data footprint optimization.

This precedent supports the same trade-off in Meridio-2: exclusive per-DG (and potentially
per-Gateway) endpoint objects with duplicate data are acceptable. Investing effort in
shared-slice optimization (e.g., Option B2) sacrifices simplicity for marginal storage
savings that Kubernetes itself does not pursue.

---

## Appendix: Schema Evolution with Independent Deployment Lifecycles

The upgrade and migration challenges discussed throughout this document (B1/B2 upgrade
mitigations, Option C+D gradual migration, rollback concerns) are instances of a well-known
distributed systems problem: **schema evolution when producers and consumers have
independent deployment lifecycles**.

The same challenge exists in Kubernetes API versioning (storage versions with conversion
webhooks), message queues (Kafka schema registry, Avro/Protobuf evolution rules), and
microservices (API versioning, backward/forward compatibility contracts).

### Established Patterns

**1. Additive-only changes (the Kubernetes API way):**
Never remove or rename fields — only add new ones. Old consumers ignore unknown fields.
New consumers use new fields if present, fall back if not. Rollback is safe because old
producers still write the fields old consumers need.
*Limitation:* Fields accumulate forever.

**2. Conversion layer (the Kubernetes storage version way):**
Store data in one canonical format. A conversion webhook translates between versions on
the fly. Consumers always see their expected version.
*Limitation:* Requires a running conversion webhook. Not applicable to core Kubernetes
types (EndpointSlices), but viable with custom resources (Option D).

**3. Dual-write with explicit contract (what Option C+D proposes):**
The producer writes both old and new formats and advertises what's available via an
explicit contract. Consumers pick the format they understand.
*Limitation:* Producer complexity during migration. Rollback requires old-format data to
still exist.

**4. Consumer-side adaptation (the pragmatic approach):**
Accept that consumers must handle N format versions. Keep N small (current + previous).
*Limitation:* Does not inherently guarantee rollback — once old-format data is removed,
rolling back the producer leaves a gap until it regenerates. Consumers accumulate legacy
interpretation code over time.

### The Fundamental Trade-off

There is no solution that simultaneously provides arbitrary rollback, zero consumer code
debt, and independent deployment ordering. You pick two:

| Choice | Consequence |
|--------|-------------|
| Independent ordering + zero code debt | No rollback support |
| Rollback + zero code debt | Coordinated ordering required |
| Rollback + independent ordering | Consumer code debt (support N formats) |

For Meridio-2, the practical recommendation is: support upgrade and rollback within one
version only (N and N-1), accept temporary dual-format support in consumers during
migration windows, and document that multi-version jumps are not supported without
intermediate steps.
