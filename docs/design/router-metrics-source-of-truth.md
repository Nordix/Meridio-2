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
`monitorConnectivity` and acts on for readiness gates, keyed by `GatewayRouter`.

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

End users who want "which `GatewayRouter` is this BFD session for" must map
IP → `GatewayRouter` themselves. This is acceptable because several router
metrics are not CRD-correlatable anyway. See "Future: query-time correlation"
below for how to add this without reopening Option 2's problems.

## Labeling

Each metric uses its own BIRD object's natural key. This is not an
inconsistency — a BGP protocol and a BFD session are different BIRD objects with
different identities.

| Metric | Key labels | Notes |
|--------|-----------|-------|
| `bgp_session_up` | `gateway`, `gateway_router` | `gateway_router` = BIRD protocol name with the `NBR-` prefix stripped. This equals the `GatewayRouter` name but is read from BIRD (the protocol's own key), not resolved against the CRD. |
| `bgp_routes_imported` | `gateway`, `gateway_router` | BGP only. Imported-route count, summed across the protocol's channels (see "Route counts" below). |
| `bgp_prefixes_announced` | `gateway`, `gateway_router` | BGP only. Exported-route count, summed the same way. |
| `bgp_session_uptime_seconds` | `gateway`, `gateway_router` | BGP only. Seconds the session has been established, `0` while it is not. See "Session uptime" below. |
| `bfd_session_up` | `gateway`, `neighbor`, `interface` | `neighbor` = neighbor IP; `interface` = BIRD session interface. Together they form the BFD session key; a session may underlie multiple protocols and must not be tied to a `GatewayRouter`. |
| `router_metrics_collect_success` | `gateway`, `source` | Per-scrape outcome of this collector's own fetch. `source` ∈ {`bgp_sessions`, `bfd_sessions`}. See "Fetch-failure handling" below. |
| `router_route_config_errors_total` | `gateway`, `reason` | Push counter (netlink failures). `reason` ∈ {`rule`, `route`}. |

**Each label name denotes exactly one kind of identity.** #153 proposed a single
`peer` label for both families, but that would have named the `GatewayRouter` on
the `bgp_*` metrics and a neighbor IP on `bfd_session_up` — one name meaning two
things, and useless for joining across them.

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
to the session metrics directly.

An info metric is a separate, controller-sourced series carrying mapping
metadata as labels, always valued `1`, e.g.
`gatewayrouter_info{gateway="gw-a", name="gw-a1-router-v4",
address="169.254.10.150", interface="vlan-100"} 1`. An operator joins it on
demand:
```promql
bgp_session_up * on(gateway_router) group_left(address, interface)
  label_replace(gatewayrouter_info, "gateway_router", "$1", "name", "(.*)")
```

Baking those labels into the session metrics instead would reopen Option 2's
problems:

- **Stuck/orphaned sessions would be hidden.** A BIRD session with no matching
  `GatewayRouter` could not be labeled, so it would be dropped or stamped
  "unknown". With the join, it simply has no matching info row: the extra labels
  are empty but **the session series is still present** — visibly
  un-correlated rather than missing.
- **It would couple two lifecycles.** The info metric follows the CRD (controller
  -managed, pre-seeded/removed on reconcile); the session metric follows BIRD
  (present iff `birdc` reports it). Merging them ties BIRD-truth data to
  CRD-driven label management, reintroducing the "forge zero on CRD intent" and
  "unregister on CRD delete" problems Option 1 avoids.

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

On a `birdc` failure the collector emits no session gauges for that scrape —
absence, not forged zeros. This is the metrics-appropriate form of the "status
report validity" concern raised on PR #233: do not fabricate state on a failed
fetch. Scrapes do not actuate, so a one-scrape gap is benign (unlike the
connectivity gate, which does actuate and is handled separately).

**Why not `prometheus.NewInvalidMetric`,** as the cache-backed collectors
(controller-manager, network-sidecar) use for a failed read: controller-runtime's
handler uses `promhttp.HTTPErrorOnError`, so a `Gather` error serves HTTP 500
instead of the payload, and Prometheus treats a failed scrape as an *empty* one —
staling **every** series on the endpoint, including controller-runtime, Go-runtime
and process metrics. (Prometheus `scrape.go`: "A failed scrape is the same as an
empty scrape, we still call sl.append to trigger stale markers".) `birdc` is a
subprocess call that can fail transiently, so that collateral loss is worse than
the absence of the session series alone.

**Scrape-health signal.** `router_metrics_collect_success{gateway,source}`
(gauge, `1`/`0`) is emitted for each source on every scrape, keeping "nothing
configured" (`success=1`, no series) distinguishable from "could not read"
(`success=0`, no series). A stuck fetch alerts on `== 0 for 5m`; a blip is a
single `0` sample.

A gauge, not a counter: the collector only queries BIRD during `Collect`, so
every attempt is observed by the scrape that caused it and no cross-scrape state
is needed. Emitted from within `Collect` rather than as a registered
`CounterVec`, because the registry gathers collectors concurrently — a separate
counter could be gathered before the increment lands, reporting the failure a
scrape late.

The name is scoped to the collection path deliberately: the router also runs
`birdc` for connectivity monitoring, so a name like `bird_query_success` would
read as reporting that path's health. A `0` here means these metrics could not be
gathered, not that connectivity is down.

## Route counts

`bgp_routes_imported` / `bgp_prefixes_announced` come from the `Routes:` line of
the protocol's detail block in `birdc show protocols all`. A BGP protocol may
declare more than one channel, so the parser **sums the counts across all
channels** rather than identifying the active one.

This is safe because BIRD's `imported`/`exported` counts are **post-filter**
(accepted routes only, per the BIRD 3.x user guide): a channel disabled via
`REJECT` reports `0 imported, 0 exported` and contributes nothing, so the sum
equals the active channel's counts. Detecting the active channel instead would
require sniffing filter names out of the detail block, which is brittle; summing
needs no such inference and stays correct if a protocol ever legitimately has
multiple active channels.

(The per-family templates `BGP_TEMPLATE_V4`/`BGP_TEMPLATE_V6` mean a generated
protocol normally declares only its own channel; the summing is what makes the
parser robust regardless.)

## Session uptime

`bgp_session_uptime_seconds` reports how long a session has been established,
derived at scrape time as `now - Since`, and is `0` while it is not established.

**A duration, not a timestamp.** Prometheus's guidance ("Timestamps, not time
since") targets monotonic "last time X happened" events and has no answer for a
value that is not currently true — there is no sensible timestamp for a session
that is down, whereas a duration has one: `0`. `czerwonk/bird_exporter` likewise
exports `bird_uptime` as a seconds gauge.

**Gated on established.** BIRD reports `Since` for whatever state the protocol is
currently in, not only `Established`, so the duration is an uptime only while the
session is established. The parsed field is named `LastStateChangeUnix` to keep
this explicit.

**What it adds over `bgp_session_up`.** Flaps shorter than the scrape interval:
a session that drops and recovers between two scrapes reads `up=1` in both, but
its uptime resets, so a value below the time since the previous scrape is
positive evidence of a restart.

**Unparseable `Since`** (bird.conf missing `timeformat protocol "%s"`) omits the
series rather than reporting `0`, which would look like a session that just reset.

`now` and `Since` share one host clock — BIRD runs in the router container — so
the subtraction carries no cross-host skew.

## `timeformat` dependency

`bgp_session_uptime_seconds` needs a parseable `Since`. BIRD's default
`timeformat protocol` (`iso short ms`) switches representation by age
(time-of-day under 20h, date beyond), so the generated `bird.conf` sets
`timeformat protocol "%s"` (Unix epoch seconds) instead. Trade-off accepted:
`birdc show protocols` becomes less human-readable; the other timeformat
purposes (log, base, route) are unchanged.

## Consequences for implementation

- `birdc` query + parsing live in `internal/bird` as the single source of
  parsing truth; both the monitor and the metrics collector consume the parsed
  structs (no duplicated parsing, no raw-text parsing in the metrics package).
- The BGP detail parser extracts route counts and the `Since` field **only for
  `Proto == "BGP"`**, summing the route counts across the protocol's channels
  (see "Route counts" above).
- The collector holds its own stateless `bird.Client`, so it never touches the
  `Bird` value the controller reconfigures through, nor the mutex serializing
  that reconfiguration.
