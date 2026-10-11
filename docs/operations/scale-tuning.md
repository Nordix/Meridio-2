# Scale Tuning

## Overview

At large object counts (thousands of L34Routes, hundreds of DistributionGroups) the
controller-manager and stateless-load-balancer can fail to start because the initial
informer cache sync does not finish within controller-runtime's default deadline:

```
ERROR  Could not wait for Cache to sync
  {"controller": "endpointnetworkconfiguration", "source": "kind source: *v1alpha1.L34Route",
   "error": "timed out waiting for cache to be synced"}
```

The sync must List and deserialize every watched object before any controller starts
reconciling. Two factors drive the duration: the number of objects, and how fast the
container may deserialize them. A conservative CPU limit makes the second factor
dominant, because CFS throttling stretches wall-clock time while the deadline does not
move.

## Which components cache L34Routes

| Component | Watches L34Routes | Cache scope |
|---|---|---|
| controller-manager | Yes (Gateway, DistributionGroup, ENC controllers) | `--namespace`, or **all namespaces** when empty |
| stateless-load-balancer | Yes | The Gateway's namespace |
| router | No (GatewayRouters only) | The Gateway's namespace |
| network-sidecar | No (single ENC) | Own Pod's ENC |

The LB only reconciles L34Routes that reference its own Gateway, but it caches all
L34Routes in the namespace — filtering happens when mapping events to reconcile
requests, not at the cache level. Cache-level filtering is not currently possible
without either mandating labels on user-owned L34Routes or depending on CRD
`selectableFields` (Kubernetes 1.31+).

## Configuration

Available on the **controller-manager** and the **stateless-load-balancer**.
Precedence: flags > environment variables > defaults.

| Flag | Env Var | Default | Description |
|---|---|---|---|
| `--cache-sync-timeout` | `MERIDIO_CACHE_SYNC_TIMEOUT` | `2m` | Time limit for each controller's initial informer cache sync. `0` falls back to the controller-runtime default (2m). |
| `--kube-api-qps` | `MERIDIO_KUBE_API_QPS` | `-1` (disabled) | Sustained client-side request rate towards the API server. Values `<= 0` disable client-side rate limiting. |
| `--kube-api-burst` | `MERIDIO_KUBE_API_BURST` | `30` | Client-side burst allowance. Only effective when `--kube-api-qps > 0`. |

The timeout applies manager-wide: it is set as the default for every controller
registered with the manager, so no per-controller configuration is needed.

### A note on QPS and burst

Client-side rate limiting is **disabled by default and was never active** in Meridio-2.
controller-runtime's `GetConfig()` sets `QPS = -1`, and client-go only constructs a
token-bucket limiter when QPS is positive. Server-side
[API Priority and Fairness](https://kubernetes.io/docs/concepts/cluster-administration/flow-control/)
handles protection instead.

Consequently, **raising QPS cannot speed up a slow cache sync** — there is no client-side
throttle to lift. These flags exist to deliberately *throttle* Meridio-2's API traffic,
for example on a shared or sensitive API server. Leave them at their defaults unless you
have that specific need.

Setting `--kube-api-qps=0` is treated as "disabled" rather than passed through, because
client-go interprets a zero QPS as "use `DefaultQPS` (5)", which would silently throttle
the client to 5 requests per second.

## Tuning guidance

Diagnose before tuning. A cache sync timeout has two distinct causes with different fixes:

1. **CPU throttling** — check `container_cpu_cfs_throttled_seconds_total` for the
   container. If it is throttled during startup, raise the CPU limit. This is the more
   common cause and the more effective fix, because it shortens the sync rather than
   just allowing more time for it.
2. **Slow API server responses** — if the API server is the bottleneck, extra CPU will
   not help and the timeout must be raised.

Suggested starting points, to be validated against your own measurements:

| L34Routes | `--cache-sync-timeout` | CPU limit (controller-manager / LB) | Memory limit |
|---|---|---|---|
| < 1000 | `2m` (default) | 500m | 512Mi |
| 1000–3000 | `5m` | 1 | 1Gi |
| > 3000 | `10m` | 2 | 2Gi |

Scope the controller-manager with `--namespace` whenever possible: with an empty value it
caches every namespace, which multiplies both sync time and memory. Consider
[Pod cache filtering](pod-cache-filtering.md) to further reduce the Pod informer's share
of the cache.

Both binaries log the effective settings at startup:

```
INFO  setup  Kubernetes API client tuning  {"cacheSyncTimeout": "5m0s", "clientRateLimiting": false, "qps": -1, "burst": 30}
```

## Applying the settings

**Controller-manager** — add the flag to the manager container's `args`, or the env var to
its `env`, in your deployment overlay:

```yaml
args:
  - run
  - --cache-sync-timeout=5m
```

**Stateless-load-balancer** — LB Pods are rendered by the controller-manager from the LB
deployment template (`--template-path`, default `/templates`). Set the env var on the
`loadbalancer` container in that template; existing LB Deployments are updated on the next
reconcile:

```yaml
- name: MERIDIO_CACHE_SYNC_TIMEOUT
  value: "5m"
```

Raising the LB's CPU limit requires editing the same template.
