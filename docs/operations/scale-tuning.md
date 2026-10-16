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

| Component | Watches L34Routes | Other cached kinds | Cache scope |
|---|---|---|---|
| controller-manager | Yes (Gateway, DistributionGroup, ENC controllers) | Gateways, GatewayClasses, GatewayConfigurations, DistributionGroups, ENCs, LoadBalancerEndpointSlices, Deployments, Pods | `--namespace`, or **all namespaces** when empty |
| stateless-load-balancer | Yes | Gateways, DistributionGroups, LoadBalancerEndpointSlices | The Gateway's namespace |
| router | No | Gateways, GatewayRouters, **all Secrets** | The Gateway's namespace |
| network-sidecar | No (single ENC) | — | Own Pod's ENC |

The LB only reconciles L34Routes that reference its own Gateway, but it caches all
L34Routes in the namespace — filtering happens when mapping events to reconcile
requests, not at the cache level. Cache-level filtering is not currently possible
without either mandating labels on user-owned L34Routes or depending on CRD
`selectableFields` (Kubernetes 1.31+).

The router does not watch L34Routes, but it is not unconditionally cheap either: its
Secret watch (for TCP-AO keychains) is unfiltered, so its informer caches **every**
Secret in the Gateway's namespace — see
[BGP Authentication (TCP-AO)](../controllers/router.md#bgp-authentication-tcp-ao).
In a namespace that also holds unrelated Secrets (Helm release data, image pull
credentials, service account tokens) this can dominate the router's cache and its sync
time. The router currently exposes no tuning flags.

## Configuration

Available on the **controller-manager** and the **stateless-load-balancer**.
Precedence: flags > environment variables > defaults.

### Cache sync timeout

This is the setting that addresses the startup failure described above.

| Flag | Env Var | Default | Description |
|---|---|---|---|
| `--cache-sync-timeout` | `MERIDIO_CACHE_SYNC_TIMEOUT` | `2m` | Time limit for each controller's initial informer cache sync. `0` falls back to the controller-runtime default (2m). |

It applies manager-wide: the value becomes the default for every controller registered
with the manager, so no per-controller configuration is needed.

### Client-side rate limiting

These two options are **not** a remedy for a slow cache sync. They are exposed so that
you can deliberately throttle Meridio-2's traffic towards the API server, for example on
a shared or otherwise sensitive control plane.

| Flag | Env Var | Default | Description |
|---|---|---|---|
| `--kube-api-qps` | `MERIDIO_KUBE_API_QPS` | `-1` (disabled) | Sustained client-side request rate towards the API server. Values `<= 0` disable client-side rate limiting. |
| `--kube-api-burst` | `MERIDIO_KUBE_API_BURST` | `30` | Client-side burst allowance. Only effective when `--kube-api-qps > 0`. |

Both are passed straight through to the Kubernetes Go client, setting
[`rest.Config.QPS` and `rest.Config.Burst`](https://pkg.go.dev/k8s.io/client-go/rest#Config).
client-go defines their exact meaning; Meridio-2 adds no behaviour of its own beyond the
normalization noted below.

Client-side rate limiting is off by default: controller-runtime's `GetConfig()` sets
`QPS = -1`, and client-go only builds a token-bucket limiter when QPS is positive.
Protection is left to server-side
[API Priority and Fairness](https://kubernetes.io/docs/concepts/cluster-administration/flow-control/).

Two consequences are worth knowing:

- **Raising QPS cannot speed up a slow cache sync.** With the default there is no
  client-side throttle to lift, so a sync that misses its deadline is bound by object
  count, available CPU or API server latency — never by this limiter.
- **`--kube-api-qps=0` is normalized to `-1`.** client-go reads a zero QPS as "use
  `DefaultQPS`" (5), which would otherwise silently throttle the client to 5 requests
  per second.

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
| > 3000 | `9m` | 2 | 2Gi |

> **Keep the timeout below 10m.** Neither the LB deployment template nor the
> controller-manager manifest sets `progressDeadlineSeconds`, so the Kubernetes default
> of 600s applies. A cache sync timeout at or above that leaves room for a Pod to still
> be legitimately syncing when its Deployment has already been marked
> `ProgressDeadlineExceeded` — which GitOps tooling typically reports as a failed
> rollout. Gateway status is unaffected (`Programmed` does not read Deployment
> conditions). If you genuinely need a longer timeout, raise
> `progressDeadlineSeconds` on the affected Deployment to match.

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

**Stateless-load-balancer** — LB Pods are not launched directly; the controller-manager
renders them from the LB deployment template (`--template-path`, default `/templates`).
Tune them by setting `MERIDIO_CACHE_SYNC_TIMEOUT` on the `loadbalancer` container's `env`
in the template ConfigMap. The LB Pods roll when the value changes, since it alters the
Pod template:

```yaml
- name: MERIDIO_CACHE_SYNC_TIMEOUT
  value: "5m"
```

The manager's `--cache-sync-timeout` does **not** propagate to the LB Pods — raise both
together, or the LB Pods silently stay at the 2m default.

Note that template edits propagate lazily: nothing watches the ConfigMap, so the change
takes effect only after the kubelet refreshes the mounted volume (up to ~1 minute) and the
next Gateway reconcile — which, with the default 10h resync, may not be imminent in an idle
cluster. Touch the Gateway or restart the controller-manager to apply it immediately.

Raising the LB's CPU limit is done either through the same template or through
`GatewayConfiguration.spec.verticalScaling`.
