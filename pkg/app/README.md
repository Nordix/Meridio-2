# pkg/app — Meridio-2 Controller Manager Extension Point

This package allows downstream consumers to embed Meridio-2's controllers in their own binary and register additional controllers that share the same manager instance.

## Benefits

- **Shared informer cache** — one set of API watches (no duplicated list/watch connections)
- **Shared leader election** — one leader across all controllers (consistent state)
- **Single binary** — one process, one pod, minimal operational overhead

## Exported API

```go
// Config exposes resolved controller-manager configuration to additional controllers.
// Values reflect the final precedence: CLI flags > environment variables > defaults.
type Config struct {
    Namespace      string
    ControllerName string
}

// ControllerSetup registers an additional controller with the manager.
// Called after built-in controllers are registered and before the manager starts.
// The manager's scheme can be extended via mgr.GetScheme().
type ControllerSetup func(mgr ctrl.Manager, cfg Config) error

// NewCommand creates a Cobra command with the same flags and environment variable
// bindings as the default controller-manager binary. Additional controllers are
// registered after built-in ones.
func NewCommand(additional ...ControllerSetup) *cobra.Command
```

## Downstream Usage

```go
package main

import (
    "os"

    "github.com/nordix/meridio-2/pkg/app"
    mycontroller "github.com/example/my-extension/internal/controller"
)

func main() {
    cmd := app.NewCommand(mycontroller.SetupWithManager)
    if err := cmd.Execute(); err != nil {
        os.Exit(1)
    }
}
```

The downstream controller:

```go
package controller

import (
    ctrl "sigs.k8s.io/controller-runtime"

    "github.com/nordix/meridio-2/pkg/app"
)

func SetupWithManager(mgr ctrl.Manager, cfg app.Config) error {
    // cfg.Namespace and cfg.ControllerName are resolved from flags/env/defaults
    // Register custom types with the scheme if needed
    // myv1alpha1.AddToScheme(mgr.GetScheme())

    return ctrl.NewControllerManagedBy(mgr).
        For(&MyResource{}).
        Complete(&MyReconciler{
            Client:    mgr.GetClient(),
            Namespace: cfg.Namespace,
        })
}
```

## Configuration

`NewCommand()` provides the same CLI flags and environment variables as the default Meridio-2 controller-manager binary. No additional configuration is needed to embed controllers.

Key flags available to all deployments:

| Flag | Env Var | Default | Description |
|------|---------|---------|-------------|
| `--namespace` | `MERIDIO_NAMESPACE` | (all) | Namespace scope |
| `--controller-name` | `MERIDIO_CONTROLLER_NAME` | `meridio-2.nordix.org/gateway-controller` | GatewayClass controller name |
| `--leader-elect` | `MERIDIO_LEADER_ELECT` | `false` | Enable leader election |
| `--enable-webhooks` | `MERIDIO_ENABLE_WEBHOOKS` | `true` | Enable validation webhooks |
| `--template-path` | `MERIDIO_TEMPLATE_PATH` | `/templates` | LB deployment template directory |

Full flag list: see `--help` output.

## Constraints

### Leader Election

All controllers (built-in and additional) share a single leader election with ID `e9d059a3.nordix.org`. This means:

- **Only one instance can be leader** — if you deploy multiple replicas with `--leader-elect=true`, only the leader runs reconcilers.
- **Do not run the extended binary alongside the standard Meridio-2 binary** in the same namespace with leader election enabled — they will compete for the same lease, causing split-brain or mutual exclusion.
- **Deploy one or the other** — either the default `controller-manager` binary or your extended binary, not both.

### Scheme Registration

The manager's scheme is populated with Meridio-2 and Gateway API types before `ControllerSetup` functions are called. Additional types can be registered via `mgr.GetScheme()` inside `ControllerSetup` — this is safe because informers start lazily when `mgr.Start()` is called (after all setup functions complete).

### RBAC

The default Meridio-2 RBAC (Role/ClusterRole) covers only built-in controllers. If your additional controller watches resources beyond what Meridio-2 manages, you must extend RBAC for the controller-manager ServiceAccount accordingly.
