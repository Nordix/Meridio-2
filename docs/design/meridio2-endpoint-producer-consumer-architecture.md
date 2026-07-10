# Meridio-2: Producer-Consumer Architecture for Endpoint Data

**Context:** Meridio-2 Maglev ID allocation design — architectural considerations for
how the controller-manager exposes endpoint data to consumers.

---

## The Problem

The Meridio-2 controller-manager (CM) produces endpoint data (Pod IPs, Maglev IDs,
readiness). Multiple consumers need this data in different forms:

- **LBs**: Need all endpoints for a DG — Maglev IDs, IPs per family, readiness
- **App-side consumer** (future): Needs only its own Pod's Maglev ID across all DGs it
  belongs to, grouped by DG (and optionally Gateway). Like the network sidecar consuming
  the ENC, this consumer should be simple — it reads a per-Pod resource produced by a
  dedicated CM-side controller, rather than piecing together DG/Gateway/EndpointSlice
  relationships itself. The CM bears the complexity of resolving the architecture; the
  sidecar just reads explicit fields.
- **Monitoring/debugging** (future): May need counts, status summaries, Pod names

The challenge: any change to how the CM represents endpoint data is a potential breaking
change for all consumers. Consumers that understand internal conventions (label semantics,
Zone field abuse, multi-object correlation) are tightly coupled to the CM's implementation.

## What makes this harder than typical API versioning

The "schema" isn't just CRD fields — it includes conventions on top:
- How many objects exist per DG (capacity splitting)
- How to correlate entries across objects (same Maglev ID = same Pod)
- What readiness means operationally (act on it or ignore it?)
- How to discover relevant objects (labels, ownerRefs, field selectors, naming patterns)

CRD versioning with conversion webhooks handles field-level changes but not
convention-level changes. Every convention a consumer relies on is an implicit contract
that can break silently.

## Alternatives

### 1. Single generic resource (all consumers read the same object)

One resource type stores all endpoint data. Every consumer reads it and extracts what it
needs.

**Pros:**
- One CRD to maintain
- One write path for the CM
- No data duplication

**Cons:**
- All consumers coupled to the same schema — a field added for one consumer's needs
  bloats the resource for all others
- Consumers carry interpretation logic (which fields are relevant to me? how do I filter?)
- Schema changes affect all consumers simultaneously
- Different consumers may need fundamentally different projections of the same data (all
  endpoints vs my own endpoint; per-DG vs per-Pod)

### 2. Per-consumer-type resources (each consumer class gets a tailored view)

The CM produces purpose-built resources for each consumer class. Each resource contains
exactly what that consumer needs, pre-digested.

**Pros:**
- Consumer logic is minimal — read fields, no interpretation
- Changing the CM's internal model doesn't affect consumers as long as their resource
  schema stays stable
- Different consumers evolve independently (LB resource gets a new field without affecting
  app-side resource)
- CRD versioning works cleanly per consumer type
- The resource IS the contract — no side-channel conventions
- CM internals can change freely (different Pod scraping mechanism, external data source,
  different internal representation) as long as the consumer-facing schema is maintained

**Cons:**
- More CRDs to maintain (one per consumer type)
- CM writes more objects (one per consumer type per DG, or per Pod for app-side)
- Data duplication across consumer-specific resources
- CM must understand what each consumer needs at design time

### 3. Layered: internal canonical model + derived consumer views

The CM maintains an internal canonical representation as a visible Kubernetes resource.
Separate reconciliation paths derive consumer-specific resources from it.

**Pros:**
- Clean separation between internal model and external contracts
- Internal model can be optimized for the CM's needs (e.g., per-CIDR for efficient Pod
  scraping)
- Consumer resources are stable contracts independent of internal changes
- **Observability**: Operators can `kubectl get` the internal state to understand what the
  CM computed — independent of what any consumer sees. Useful for debugging discrepancies.
- **Declarative checkpoint**: The internal model is a verifiable intermediate state. If a
  consumer-facing resource looks wrong, operators can check the internal model to isolate
  whether the bug is in endpoint logic or in consumer-resource generation.
- **Foundation for future consumers**: New consumers can be built against the internal
  model without the CM needing to produce yet another tailored resource immediately. The
  internal model is the source of truth that consumer-specific resources are derived from.

**Cons:**
- Extra reconciliation step (internal → consumer resource) adds latency and complexity
- More objects in the cluster (internal + per-consumer)
- If the internal model and the primary consumer's resource are nearly identical (as they
  would be today with only the LB as consumer), the internal model is redundant overhead
- More complex CM architecture

## Recommended approach for Meridio-2

**Option 2 for now, with awareness of option 3 for the future.**

Today there is one consumer (LB). The LB-facing resource (`LoadBalancerEndpointSlice`) is the
CM's direct output — no intermediate internal representation. This is simple, efficient,
and sufficient.

When a second consumer with a different shape requirement appears (app-side Maglev
context), two paths are available:
- **Independent derivation** (like the ENC controller): The new consumer's reconciliation
  path resolves the same upstream resources (Pods, DGs, GatewayConfigs) independently,
  producing its own per-Pod resource. No shared intermediate model needed.
- **Introduce option 3**: If multiple consumers need overlapping but differently-shaped
  data, and debugging/observability of the CM's internal state becomes important, introduce
  a canonical internal resource as a shared derivation checkpoint. Both consumer resources
  are derived from it.

Option 3 becomes worthwhile when:
- Multiple consumer resources need to be kept consistent with each other (shared checkpoint
  ensures they derive from the same state)
- Observability of the CM's internal endpoint model is needed independently of any
  consumer's view
- The derivation logic from upstream resources is complex enough that duplicating it across
  multiple reconciliation paths is error-prone

## Key properties

**Stable contract**: The `LoadBalancerEndpointSlice` schema is the contract between CM and LBs.
How the CM produces it is an internal detail that can change without affecting consumers.

**Independent evolution**: Each consumer-type resource evolves independently. CRD
versioning handles schema changes per resource type.

**Minimal consumer logic**: Consumers read explicit fields — no interpretation of
conventions, no multi-object correlation, no label semantics beyond discovery.

**Upgrade resilience**: Schema changes use CRD versioning + conversion webhooks.
Convention-level changes (capacity splitting, naming, discovery) remain vulnerable but are
minimized by keeping conventions simple and documenting them as part of the contract.

**Vulnerability**: Convention-level changes (capacity splitting strategy, naming patterns,
discovery mechanism) are still potential breaking changes not covered by CRD versioning.
Minimized by keeping conventions simple (field-selector-based discovery, deterministic
naming) and documenting them explicitly.

## Existing pattern in Meridio-2

The ENC (EndpointNetworkConfiguration) already follows the per-consumer-type model:
- It's a per-Pod resource produced by the Meridio-2 CM
- It contains exactly what the sidecar needs (VIPs, next-hops, interface identity)
- The sidecar is a dumb consumer — reads fields, applies network config
- The CM's internal resolution chain (Pod→DG→Gateway→GatewayConfig→SLLBR Pods) is
  invisible to the sidecar

The `LoadBalancerEndpointSlice` resource would be the LB equivalent of what the ENC is for
the sidecar.
