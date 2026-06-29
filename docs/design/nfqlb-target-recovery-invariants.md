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

The `doExec` calls to `nfqlb activate`/`deactivate` use the reconcile context without additional timeouts. This guarantees an unambiguous outcome: the call either succeeds or fails, never times out mid-operation. A timed-out activate might have succeeded in NFQLB's shared memory, but the caller wouldn't know whether to retry or clean up. With broken-state tracking, a timeout would be handled as a broken state (mark broken, retry on next reconcile), so custom timeouts would not break recovery — but they add complexity without clear benefit.

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

The `broken` map (`map[int]struct{}`) tracks identifiers in an inconsistent state — where any operation in AddTarget or DeleteTarget partially failed. An identifier is marked broken when:

1. **AddTarget — route creation fails** (activate never attempted): routes may be partially created, no nfqlb slot active. Target committed to `s.targets` as broken.
2. **AddTarget — activate fails** (routes were created successfully): routes exist, nfqlb slot state unknown. Target committed to `s.targets` as broken.
3. **AddTarget — IPs-unchanged or IPs-changed re-apply path**: route or activate retry fails. Marked broken.
4. **DeleteTarget — deactivate fails**: slot may still be active, routes still exist. Stays in `s.targets`, marked broken.
5. **DeleteTarget — route deletion fails** (deactivate succeeded): slot inactive, routes partially remain. Stays in `s.targets`, marked broken.

Without broken tracking, the following stuck state occurs:
1. AddTarget: routes fail → target in `s.targets`, no retry trigger
2. Target disappears from EndpointSlices before retry
3. Nobody calls DeleteTarget (identifier not in stale `c.targets` nor `newTargets`)
4. With broken tracking: reconcile loop cleans up broken targets not in `newTargets`

The broken flag is cleared only when **all** operations in AddTarget or DeleteTarget complete successfully:
- AddTarget: routes created + activate succeeded (applies to all paths: new, same-IPs, IPs-changed)
- DeleteTarget: deactivate + all route deletions succeeded → entry removed from both maps

All paths (same-IPs, IPs-changed) include an `activate` call after route creation. Since `activate` is idempotent (no-op on already-active slot per invariant #5), this ensures recovery when the previous failure was an activate failure — not just a route failure.

**Note on DeleteTarget clearance**: The `delete(s.broken, identifier)` at the end of a successful DeleteTarget also removes the target from `s.targets`. If DeleteTarget fails at any step, the identifier remains in both `s.targets` and `broken`, ensuring the next reconcile retries the deletion.
