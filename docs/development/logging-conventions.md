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
| a named component logger (`ctrl.Log.WithName(...)`) outside reconcile/setup | `<component>Log` (e.g. `metricsLog`, `monitorLog`, `birdcLog`) | a long-lived logger for a subsystem; the suffix keeps it distinct from both the reconcile `log` and `setupLog` |
| an injected `logr.Logger` parameter/field | `logger` | distinct role: a dependency passed in, not a local |

> **Known exception:** `internal/bird/routing.go` declares a package-level
> `var log = logf.Log.WithName("routing")`. A package-level `log` blocks the
> `log := logf.FromContext(ctx)` pattern throughout that package (which is why
> `internal/bird/client.go` has a separate `clientLog`). This is a known
> exception slated to be renamed; do not copy it.

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
string:

```go
log.V(1).Info("matching pods", "count", len(pods))   // good
log.V(1).Info(fmt.Sprintf("matching %d pods", n))     // avoid
```

**Do not re-attach the reconciled object's identity.** controller-runtime's
default `LogConstructor` already enriches the reconcile logger with the object
key (e.g. `DistributionGroup="ns/name"`) plus `namespace` and `name`, so every
line from `logf.FromContext(ctx)` carries it already. Adding your own
`WithValues("distributiongroup", req.NamespacedName)` just duplicates this — and
a lower-cased key collides by case with the framework's in JSON output. Just use:

```go
log := logf.FromContext(ctx)
```

If a helper genuinely needs *additional* context on every line, enrich the
logger and put it back into the context with `logf.IntoContext`, then pass that
`ctx` down — `WithValues` alone does not propagate, since it returns a new logger
without touching `ctx`:

```go
ctx = logf.IntoContext(ctx, log.WithValues("vip", vip))
doWork(ctx) // helpers calling logf.FromContext(ctx) now see "vip"
```

Use a key that does not collide with the framework's object key, and use stable,
entity-specific keys (`"gateway"`, `"vip"`, `"flow"`) consistently across
controllers.

## Message style

Write log messages as short, capitalized phrases (`"Reconciling DistributionGroup"`,
not `"reconciling DistributionGroup"`). Keep them constant — put the variable
part in key/value pairs, never in the message string. Consistent casing keeps
messages greppable and avoids the split where the same event reads two ways in
two controllers.

## Config dumps and secrets

Binaries log their whole config struct once at startup (`V(0)`). That is fine
today because no config struct carries a secret (BGP passwords, for example,
reach BIRD through a separate resolve step, not through the logged config). This
is a standing requirement, not an accident: **config structs that are logged
must not carry secret fields.** If a credential-bearing field is ever added to a
logged config, it must implement a redacting `MarshalLogObject` (or be excluded
from the logged struct) so it does not become a `V(0)` log line by default.

## Note on non-`logr` output

- Cobra `version` subcommands print to stdout via `fmt.Println` — this is program
  output, not logging; leave it.
- The embedded BIRD process logs through its own mechanism to stderr/files; that
  is independent of `logr` by design.
