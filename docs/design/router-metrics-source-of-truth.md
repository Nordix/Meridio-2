# Router Metrics: Source of Truth (BIRD-truth, not control-loop state)

## Overview

The router container exposes Prometheus metrics about BGP/BFD sessions and
route counts (see issue #153 §3). A design decision had to be made about **what
these metrics represent**, because two mutually exclusive sources of truth are
possible. This document records that decision and its consequences so future
work (and future metrics for the router/LB) stays consistent.

## The two options

### Option 1 — BIRD-truth (chosen)

Metrics report exactly what `birdc` reports, with no correlation back to
`GatewayRouter` custom resources. Each metric uses the natural key of the BIRD
object it describes.

- Source of truth: BIRD internal state, read via `birdc` at scrape time.
- The collector is **independent** of the router's control loop
  (`monitorConnectivity`): it runs its own, timeout-bounded `birdc` queries and
  parses them. It does not read the controller cache and does not reuse the
  connectivity decision.

### Option 2 — Router-effective-status (rejected)

Metrics report the router's *effective* connectivity view — the same
BFD-correlated state the control loop computes in `applyBfdState` /
`monitorConnectivity` and acts on for readiness gates. `peer` would be the
`GatewayRouter`.

- Source of truth: the control loop's derived snapshot (post-`applyBfdState`),
  not raw `birdc`.
- Would **have to** share data at the `monitorConnectivity` level — replicating
  `applyBfdState` in an independent metrics collector is pointless and
  dangerous, because a second implementation could diverge from the real
  connectivity decision.

## Decision

**Option 1 (BIRD-truth).** Rationale:

1. **Consistency across the whole router metric set.** Only a subset of router
   metrics is correlatable to a CRD (BGP sessions → `GatewayRouter`). Others are
   not: netlink `route_config_errors`, raw BFD sessions, and future nfqueue/LB
   metrics. Baking CRD correlation into the correlatable subset yields an
   inconsistent product (half CRD-correlated, half not). BIRD/system-truth is
   uniform.
2. **Exposes failure modes the control loop hides.** Stuck/zombie sessions and
   orphaned sessions that no current `GatewayRouter` maps to are visible under
   BIRD-truth, precisely because the metric is not sanitized through the
   control-loop decision.
3. **BFD is correctly first-class.** A BFD session is identified by neighbor IP
   (+ interface, + optional local IP) and can back multiple protocols at once
   (e.g. a BGP and a Static `GatewayRouter` to the same neighbor). It has no
   single `GatewayRouter` owner, so a GatewayRouter-keyed `bfd_session_up` is
   structurally wrong (one session ↔ N routers). IP+interface is the only
   correct key. (BIRD 3.x user guide: BFD sessions are created on demand by
   requesting protocols and identified by neighbor IP + optional interface.)
4. **Architecturally clean.** Independent collector, no control-loop coupling,
   no replicated-logic divergence risk, no shared mutable snapshot.
5. **Lifecycle is satisfied by construction** (see below).

### Cost accepted

End users who want "which `GatewayRouter` is this peer" must map IP →
`GatewayRouter` themselves. This is acceptable because several router metrics
are not CRD-correlatable anyway. See "Future: query-time correlation" below for
how to add this without reopening Option 2's problems.

## Labeling

Each metric uses its own BIRD object's natural key. This is not an
inconsistency — a BGP protocol and a BFD session are different BIRD objects with
different identities.

| Metric | Key labels | Notes |
|--------|-----------|-------|
| `bgp_session_up` | `gateway`, `gateway_router` | `gateway_router` = BIRD protocol name with the `NBR-` prefix stripped. This equals the `GatewayRouter` name but is read from BIRD (the protocol's own key), not resolved against the CRD. |
| `bgp_routes_imported` | `gateway`, `gateway_router` | BGP only. Imported-route count, summed across the protocol's channels (see "Route counts" below). |
| `bgp_prefixes_announced` | `gateway`, `gateway_router` | BGP only. Exported-route count, summed the same way. |
| `bgp_session_last_established_timestamp_seconds` | `gateway`, `gateway_router` | BGP only. See "timeformat" below. |
| `bfd_session_up` | `gateway`, `neighbor`, `interface` | `neighbor` = neighbor IP; `interface` = BIRD session interface. Together they form the BFD session key; a session may underlie multiple protocols and must not be tied to a `GatewayRouter`. |
| `router_metrics_collect_success` | `gateway`, `source` | Per-scrape outcome of this collector's own fetch. `source` ∈ {`bgp_sessions`, `bfd_sessions`}. See "Fetch-failure handling" below. |
| `router_route_config_errors_total` | `gateway`, `reason` | Push counter (netlink failures). `reason` ∈ {`rule`, `route`}. |

Each metric is keyed by its own BIRD object, and **each label name denotes
exactly one kind of identity**. #153 proposed a single `peer` label for both
families, but that would have named the `GatewayRouter` on the `bgp_*` metrics
and a neighbor IP on `bfd_session_up` — one label meaning two different things
depending on the metric, and therefore useless for joining across them. Hence
`gateway_router` (BGP protocol, keyed by name) vs. `neighbor` + `interface`
(BFD session, keyed by its endpoint).

- `gateway` is kept on all metrics (semantically meaningful, not auto-injected;
  a router Pod serves exactly one Gateway).
- `pod` / `instance` are **not** set by us — Prometheus injects them from
  per-Pod target discovery (PodMonitor), consistent with the network-sidecar
  decision in #153/#236.
- Static `GatewayRouter`s produce **no** `bgp_*` series (no BGP session). Their
  health is observed via `bfd_session_up`. This matches the existing
  `applyBfdState` logic: a Static protocol is always "up" in BIRD, so BFD is the
  real liveness signal.

## Future: query-time correlation to `GatewayRouter` (not built now)

If filtering/grouping the session metrics by `GatewayRouter` attributes
(namespace, spec fields, etc.) is ever needed, add it via a Prometheus
**info metric** joined at query time — do **not** add `GatewayRouter` labels
to the session metrics directly. That distinction matters:

- **Info metric pattern:** a separate, controller-sourced metric whose only
  purpose is to carry mapping/metadata as labels, always valued `1`, e.g.
  `gatewayrouter_info{gateway="gw-a", name="gw-a1-router-v4",
  address="169.254.10.150", interface="vlan-100"} 1`. An operator joins it onto
  a session metric at query time:
  ```promql
  bgp_session_up * on(gateway_router) group_left(address, interface)
    label_replace(gatewayrouter_info, "gateway_router", "$1", "name", "(.*)")
  ```
- **Why not bake `GatewayRouter` labels into the session metrics instead:**
  doing so would silently reopen Option 2's problems.
  - **It would hide stuck/orphaned sessions.** A BIRD session with no matching
    current `GatewayRouter` could not be labeled, so it would either be
    dropped from the metric or stamped with an arbitrary/"unknown" value —
    exactly the observability loss Option 1 was chosen to avoid. With the
    join approach, an orphaned session simply has no matching row in
    `gatewayrouter_info`; the `group_left` join leaves the extra labels
    empty but **the session series is still present** — visibly
    un-correlated rather than hidden.
  - **It would couple two different lifecycles.** The info metric follows the
    CRD's lifecycle (controller-managed, can be pre-seeded/removed on
    reconcile); the session metric follows BIRD's lifecycle (lazy, present
    if and only if `birdc` reports it). Merging them into one series ties
    BIRD-truth data to CRD-driven label management, reintroducing the
    "forge zero on CRD intent" and "unregister on CRD delete" issues
    Option 1 was meant to avoid.
  - The join happens only when an operator chooses to perform it; the
    session metrics themselves stay a pure, unconditional reflection of
    BIRD state.

## Lifecycle (relative to #153 implementation note)

#153 states metrics must be "initialized to zero when their label combinations
become known ... and actively unregistered" when a resource is removed. That
requirement targets **push-style registries** that retain series until
explicitly deleted.

Under Option 1 the session/route gauges are produced by a **lazy
(pull/`Collect`-time) collector** that builds series fresh from the current
`birdc` output on every scrape. Therefore:

- A session exists in the output if and only if BIRD currently reports it —
  absence of a series is the truthful "no such session" signal.
- There is no retained registry to seed with zeros or to unregister from.

So the lifecycle requirement is **satisfied by construction** for the lazy
gauges. We deliberately do **not** forge `bfd_session_up=0` (or similar) for a
`GatewayRouter` that declares BFD but has no session yet in BIRD — that would
fabricate observation data from CRD intent. If "configured but absent" is ever
needed, it belongs in a separate intent-vs-observation metric, not as a forged
value on an observation gauge.

The push counter (`router_route_config_errors_total`) is labeled
`{gateway,reason}` (no per-peer/per-session label), so it has no dynamic-peer
unregistration concern.

## Fetch-failure handling

The metrics collector runs its own `birdc` calls (independent of the monitor).
On a `birdc` failure it must **not** report "everything down": it simply emits
no session gauges for that scrape (absence, not false negatives). This is the
metrics-appropriate form of the "status report validity" concern raised on
PR #233 — do not fabricate state on a failed fetch. Because scrapes have no
actuation, serving no/stale session series for one scrape is benign (unlike the
connectivity gate, which does actuate and is handled separately).

**Why not report the failure to the registry.** The cache-backed collectors
(controller-manager, network-sidecar) surface a failed read via
`prometheus.NewInvalidMetric`. That is deliberately **not** done here:
controller-runtime's metrics handler uses `promhttp.HTTPErrorOnError`, so a
`Gather` error serves HTTP 500 instead of the payload, and Prometheus treats a
failed scrape as an *empty* one — writing stale markers for **every** series on
the endpoint, including the router's controller-runtime, Go-runtime and process
metrics (verified in `scrape.go`: "A failed scrape is the same as an empty
scrape, we still call sl.append to trigger stale markers"). Since `birdc` is a
subprocess call against a Unix socket and can fail transiently, that collateral
loss is worse than the absence of the session series alone.

**Scrape-health signal.** To keep the failure observable without failing the
scrape, the collector emits `router_metrics_collect_success{gateway,source}`
(gauge, `1`/`0`) for each source on every scrape. This keeps "nothing
configured" (`success=1`, no series) distinguishable from "could not read"
(`success=0`, no series) — the gap that emitting nothing would otherwise leave.
A stuck fetch alerts on `== 0 for 5m`; a transient blip is a single `0` sample.

It is a gauge rather than a counter because the collector only queries BIRD
*during* `Collect`: every attempt is created and observed by the same scrape, so
there are no unobserved attempts between scrapes for a counter to capture, and
no cross-scrape state is needed. It is emitted from within `Collect` rather than
as a separately registered `CounterVec` because the registry gathers collectors
concurrently — a separate counter incremented inside `Collect` could be gathered
before the increment lands, reporting the failure a scrape late.

The name is scoped to the collection path on purpose: the router also runs
`birdc` continuously for connectivity monitoring, so a name like
`bird_query_success` would read as reporting that operational path's health. A
`0` here means these metrics could not be gathered — not that BGP/BFD
connectivity is down.

## Route counts

`bgp_routes_imported` / `bgp_prefixes_announced` come from the `Routes:` line of
the protocol's detail block in `birdc show protocols all`. A BGP protocol may
declare more than one channel, so the parser **sums the counts across all
channels** of the protocol rather than trying to identify the active one.

This works because BIRD's `imported`/`exported` counts are **post-filter**
(accepted routes only, verified against the BIRD 3.x user guide): a channel
disabled via `REJECT` filters always reports `0 imported, 0 exported`, so it
contributes nothing and the sum equals the active channel's counts. Summing is
preferred over detecting the active channel because the latter would require
sniffing filter names / `REJECT` markers out of the detail block, which is
brittle; the sum needs no such inference and stays correct if a protocol ever
legitimately has multiple active channels.

Note that the per-family BGP templates (`BGP_TEMPLATE_V4`/`BGP_TEMPLATE_V6`)
mean each generated protocol normally declares only its own channel; the summing
behavior is what makes the parser robust regardless.

## `timeformat` dependency

`bgp_session_last_established_timestamp_seconds` is parsed from BIRD's protocol
`Since` field. BIRD's default `timeformat protocol` (`iso short ms`) switches
representation by age (time-of-day for < 20h, date for older), which is
unreliable to parse. The generated `bird.conf` therefore sets
`timeformat protocol "%s"` (Unix epoch seconds) so the field is a single
unambiguous absolute timestamp. Trade-off accepted: `birdc show protocols`
output becomes less human-readable (epoch instead of clock time); other
timeformat purposes (log, base, route) are unchanged.

## Consequences for implementation

- `birdc` query + parsing live in `internal/bird` as the single source of
  parsing truth; both the monitor and the metrics collector consume the parsed
  structs (no duplicated parsing, no raw-text parsing in the metrics package).
- The BGP detail parser extracts route counts and the established timestamp
  **only for `Proto == "BGP"`**, summing the route counts across the protocol's
  channels (see "Route counts" above).
- The metrics collector is independent of `monitorConnectivity`; it does not
  take the `Bird` instance's lifecycle mutex and is unaffected by `Configure`.
