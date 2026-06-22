# NFQLB Target Management: Recovery Invariants

## Overview

The LB controller manages NFQLB targets (activate/deactivate slots, create/delete policy routes). The recovery model relies on several implicit invariants that must be preserved for self-healing to work correctly.

## Invariants

### 1. Operation ordering: routes before activate

`AddTarget` creates policy routes before activating the NFQLB slot. This order ensures:
- If routes fail → no nfqlb slot is activated, no traffic reaches partial routes
- If activate fails after routes → target is marked broken, routes exist but harmless (invariant #3)
- On retry, routes are re-applied idempotently then activate is retried

This order is preferred because an active slot with missing routes would cause traffic drops, while routes without an active slot receive no traffic (NFQLB hasn't marked packets with the fwmark yet).

### 2. No context timeouts on nfqlb CLI calls

The `doExec` calls to `nfqlb activate`/`deactivate` use the reconcile context without additional timeouts. This guarantees an unambiguous outcome: the call either succeeds or fails, never times out mid-operation. Adding a custom timeout would break the recovery model — a timed-out activate might have succeeded in NFQLB's shared memory, but the caller wouldn't know whether to retry or clean up.

**Constraint**: Do not wrap nfqlb CLI calls in shortened-timeout contexts.

### 3. Orphaned routes are harmless

Policy routes (fwmark-based) only receive traffic if NFQLB actively sets the corresponding fwmark on packets. A route without an active NFQLB slot never gets traffic. This means:
- Failed route deletions don't cause traffic blackholes
- Stale routes from a previous activation are overwritten by `RouteReplace` on retry

**Dependency**: This relies on the fwmark routing design where NFQLB is the sole source of fwmarks. If any other component sets fwmarks in the same range, orphaned routes could receive unintended traffic.

### 4. `s.targets` commit position determines recovery behavior

- `AddTarget`: commits `s.targets[id] = ips` on any partial progress (routes created or activate attempted). If any step fails, the target is in `s.targets` and marked broken. On retry, the "IPs unchanged" path re-applies routes and retries activate if needed.
- `DeleteTarget`: removes `delete(s.targets, id)` only after all operations succeed (deactivate + route deletion). If any step fails, the identifier stays in `s.targets` and is marked broken so the next reconcile will retry `DeleteTarget`.

The reconciler layer always commits `c.targets = newTargets ∪ failedDeletes`, ensuring the diff on next reconcile triggers DeleteTarget for disappeared-but-failed targets.

### 5. Idempotent nfqlb and route operations

The recovery model relies on:
- `nfqlb activate` on an already-active slot: no-op (returns success)
- `nfqlb deactivate` on an inactive slot: no-op (returns success)
- `RouteReplace`: overwrites existing routes (idempotent by design)
- `ensureRule`: skips if rule already exists (checks before adding)
- `doDeletePolicyRoute`: tolerates ENOENT/ESRCH (route/rule not found)

If any of these operations became non-idempotent (e.g., returning errors for "already exists" or "not found"), retries would fail or loop indefinitely.

## Broken State Tracking

The `broken` map (`map[int]struct{}`) tracks identifiers where activate succeeded but route creation failed. This prevents the stuck state where:
1. AddTarget: activate succeeds, routes fail → target in `s.targets`, marked broken
2. Target disappears from EndpointSlices before retry
3. Without broken tracking: nobody calls DeleteTarget (identifier not in stale `c.targets` nor `newTargets`)
4. With broken tracking: reconcile loop cleans up broken targets not in `newTargets`

The broken flag is cleared on:
- Successful route creation (all paths: new, same-IPs, changed-IPs)
- DeleteTarget (regardless of outcome)
