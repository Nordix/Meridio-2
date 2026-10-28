# Logging Conventions

All Go code logs through the [`logr`](https://github.com/go-logr/logr) interface
(via controller-runtime, backed by zap). Do not introduce other logging libraries
(`logrus`, `klog`, `slog`, stdlib `log`, `fmt.Print*`) for application logging.

## Imports and variable naming

| Thing | Name | Why |
|-------|------|-----|
| `sigs.k8s.io/controller-runtime/pkg/log` | import alias `logf` | the package is named `log`; aliasing frees the name `log` for the local logger variable and avoids shadowing |
| `github.com/nordix/meridio-2/internal/common/log` | import alias `logServer` | also named `log`; aliased so the local logger can be `log` |
| the context/request logger variable | `log` | `log := logf.FromContext(ctx)` |
| the setup-time logger variable | `setupLog` | `setupLog := ctrl.Log.WithName("setup")` |
| an injected `logr.Logger` parameter/field | `logger` | distinct role: a dependency passed in, not a local |

```go
import (
    logf "sigs.k8s.io/controller-runtime/pkg/log"
)

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
    log := logf.FromContext(ctx)
    ...
}
```

## The two axes: method and verbosity

`logr` has only two output methods, and verbosity (`V(n)`) applies **only to Info**:

- `log.Info(msg, kv...)` is `log.V(0).Info(...)` — a bare `Info` is V(0).
- `log.V(1).Info(...)`, `log.V(2).Info(...)` — higher = less important = hidden at lower verbosity.
- `log.Error(err, msg, kv...)` has **no** V-level — errors are always emitted.

There is no `Warn`/`Debug`/`Fatal`. Use the V-level ladder instead.

## When to use which level

| Level | Use for | Shown at default (`info`)? |
|-------|---------|----------------------------|
| `Error(err, ...)` | Actionable failures the operator may need to respond to. Always carries the `err`. | always |
| `Info` / `V(0)` | Low-frequency, operator-relevant **state changes**: controller started, config applied, readiness flipped, entering a non-obvious degraded/skip state. | yes |
| `V(1).Info` | Routine per-reconcile progress and "waiting on dependency / skipping" decisions. | no |
| `V(2).Info` | Detailed diagnostic dumps (full object/connection contents, per-rule operations). | no |

**Rule of thumb:** if a line fires on most reconciles, it belongs at `V(1)`, not `V(0)`.

Keeping V(0) reserved for genuine events is what makes `--log-level` and the
runtime log-level API useful — operators can quiet routine output or opt into
deeper detail without code changes.

### Error vs. tolerable failure

Not every failure is an `Error`. Expected or transient conditions (a dependency
not yet created, a tolerable netlink errno) should be logged at `V(1).Info` with
an `"error"` key, so they are filtered out at the default level and don't alarm
operators:

```go
// Actionable: surface it.
log.Error(err, "failed to reconcile endpointslices")

// Tolerable/transient: quiet and leveled.
log.V(1).Info("failed to delete stale rule", "src", rule.Src, "table", rule.Table, "error", err)
```

## Structured context

Attach context as key/value pairs; do not interpolate values into the message
string. Enrich the logger once per `Reconcile` with the object key so every
subsequent line inherits it:

```go
log := logf.FromContext(ctx).WithValues("distributiongroup", req.NamespacedName)
log.V(1).Info("reconciling DistributionGroup")   // object key attached automatically
...
log.V(1).Info("matching pods", "count", len(pods))
```

Use stable, entity-specific keys (`"gateway"`, `"distributiongroup"`, `"vip"`,
`"flow"`) consistently across controllers.

## Note on non-`logr` output

- Cobra `version` subcommands print to stdout via `fmt.Println` — this is program
  output, not logging; leave it.
- The embedded BIRD process logs through its own mechanism to stderr/files; that
  is independent of `logr` by design.
