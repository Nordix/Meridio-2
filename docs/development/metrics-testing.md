# Metrics Testing Guide

How to exercise the controller-manager's custom Prometheus collectors. Most of
this guide covers **local** testing (out-of-cluster `go run`, plain HTTP,
`curl`) that needs no data-plane environment (Multus, real LB Pods, or a
metrics-scraping Prometheus). The final section,
[Scraping with an in-cluster Prometheus](#scraping-with-an-in-cluster-prometheus),
covers wiring a real Prometheus/Grafana to the deployed controller-manager's
secure metrics endpoint.

The controller-manager exposes these custom metrics (all prefixed by
`--metrics-prefix`, default `meridio_2`):

| Metric | Labels | Meaning |
| --- | --- | --- |
| `<prefix>_gateway_count` | (none) | Number of Gateways with `Accepted=True` managed by this controller. |
| `<prefix>_gateway_programmed` | `gateway`, `namespace` | `Programmed` status condition (0/1) per Gateway destined for this controller by its GatewayClass (`spec.gatewayClassName` → `GatewayClass.spec.controllerName`), independent of `Accepted`. |
| `<prefix>_distributiongroup_ready` | `dg`, `namespace` | DistributionGroup `Ready` status condition (0/1). DG-wide, no Gateway dimension. |
| `<prefix>_distributiongroup_endpoints` | `gateway`, `gateway_namespace`, `dg`, `namespace` | Current endpoint count for the DG under a given Gateway. |
| `<prefix>_distributiongroup_max_endpoints` | `gateway`, `gateway_namespace`, `dg`, `namespace` | Upper bound on endpoint count per the DG's distribution strategy (Maglev capacity; `+Inf` for strategies without a bounded capacity). |

## The `--metrics-collect-timeout` flag

Each collector's `Collect` waits for the informer cache to finish its initial
sync before listing objects, bounded by `--metrics-collect-timeout` (default
5s). It is kept deliberately shorter than Prometheus's own `scrape_timeout`
(default 10s): `prometheus/client_golang`'s `Gather` cannot be cancelled by the
scraper disconnecting, so this timeout is the only thing that turns a
not-yet-synced-cache scrape into our specific, actionable error instead of a
generic timeout on Prometheus's side. Setting it equal to or above
`scrape_timeout` loses that race most of the time (Prometheus's clock starts
before ours), so raising it further is discouraged — keep it below whatever
`scrape_timeout` is configured.

## Unit Tests

```bash
go test ./internal/metrics/... -v
go test ./internal/common/metrics/... -v
go test ./internal/common/gatewayutil/... -v
```

## Manual Testing (controller-manager run locally)

The controller-manager can run as an ordinary local process (out-of-cluster).
It connects to whatever cluster your current kubectl context points at (via
`~/.kube/config`) and serves `/metrics` on your local machine. There is no need
to build an image or deploy into the cluster to exercise the collectors.

### Prerequisites

1. A reachable cluster (a Kind cluster is fine). Confirm your context:
   ```bash
   kubectl config current-context
   kubectl get nodes
   ```

2. Meridio-2 and Gateway API CRDs installed in that cluster:
   ```bash
   make install   # installs Meridio-2 CRDs (config/crd)
   ```
   Gateway API CRDs are also required (the controller-manager checks for them as
   a startup prerequisite). Install them if not already present.

3. Confirm the CRDs the collectors read are present:
   ```bash
   kubectl get crd | grep -E 'gateway.networking.k8s.io|meridio-2.nordix.org'
   # in particular: gateways, gatewayclasses, distributiongroups, loadbalancerendpointslices
   ```

### Run with metrics enabled over plain HTTP

Metrics are **disabled by default** (`--metrics-bind-address` defaults to `0`),
and the deployed manifest does not enable them either — so they must be turned
on explicitly. For local testing, serving plain HTTP (`--metrics-secure=false`)
avoids the TLS/authn setup that secure serving requires.

```bash
go run ./cmd/controller-manager run \
  --metrics-bind-address=:8080 \
  --metrics-secure=false \
  --leader-elect=false \
  --enable-webhooks=false \
  --namespace=default \
  --template-path=config/templates
```

Notes on the non-obvious flags:

- `--template-path=config/templates` — startup validation checks for the LB
  deployment template. The default path (`/templates`) is the in-container
  mount point and does not exist when running locally; point it at the in-repo
  templates directory instead (run from the repo root, or use an absolute path).
- `--enable-webhooks=false` — avoids needing webhook certificates locally.
- `--metrics-secure=false` — serve plain HTTP so `curl` works without a token or
  certificates. Do **not** use this in a real deployment.

Scrape it from another shell:

```bash
curl -s http://127.0.0.1:8080/metrics | grep ^meridio
```

On an empty cluster you will see only `meridio_2_gateway_count 0`. That is
correct: `gateway_count` is a constant metric (one series always), while the
other metrics are labeled and emit one series *per matching object* — with no
objects, they emit no series, and Prometheus omits the `# HELP`/`# TYPE` lines
for a metric that has no series. Their absence is the correct representation of
"nothing to report", not a bug.

## Producing non-zero values

### 1. An accepted Gateway + DistributionGroups

A Gateway only reaches `Accepted=True` when it references a valid
`GatewayConfiguration` via `spec.infrastructure.parametersRef` — this is
required, not optional. Without it, the Gateway is `Accepted=False`, so
`gateway_count` stays `0` and any DG referencing it falls back to the empty
`gateway=""` label (the empty-union fallback).

> Note: `gateway_programmed` is gated on GatewayClass ownership, not on `Accepted`.
> So as soon as a Gateway references our GatewayClass it emits a
> `gateway_programmed{gateway=...}` series — reading `0` while it is not yet
> programmed (e.g. still `Accepted=False`), and `1` once the LB Deployment is
> reconciled. This series appears independently of `gateway_count`.

```bash
cat <<'EOF' | kubectl apply -f -
---
apiVersion: gateway.networking.k8s.io/v1
kind: GatewayClass
metadata:
  name: meridio-2
spec:
  controllerName: meridio-2.nordix.org/gateway-controller
---
apiVersion: meridio-2.nordix.org/v1alpha1
kind: GatewayConfiguration
metadata:
  name: gatewayconfiguration-sample
  namespace: default
spec:
  networkAttachments:
    - description: "MACVLAN network for application endpoints"
      type: NAD
      nad:
        name: macvlan-nad-1
        namespace: default
        interface: net1
  internalSubnets:
    - attachmentType: NAD
      cidr: "169.111.100.0/24"
  horizontalScaling:
    replicas: 1
    enforceReplicas: true
---
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: sllb-sample
  namespace: default
spec:
  gatewayClassName: meridio-2
  infrastructure:
    parametersRef:
      group: meridio-2.nordix.org
      kind: GatewayConfiguration
      name: gatewayconfiguration-sample
  listeners:
    - name: all
      protocol: ALL
      port: 1
---
apiVersion: meridio-2.nordix.org/v1alpha1
kind: DistributionGroup
metadata:
  name: distributiongroup-sample
  namespace: default
spec:
  selector:
    matchLabels:
      app: target-application-sample
  maglev:
    maxEndpoints: 102
  parentRefs:
    - name: sllb-sample
---
apiVersion: meridio-2.nordix.org/v1alpha1
kind: DistributionGroup
metadata:
  name: distributiongroup-sample-2
  namespace: default
spec:
  selector:
    matchLabels:
      app: target-application-sample
  maglev:
    maxEndpoints: 10
  parentRefs:
    - name: sllb-sample
EOF
```

After this, expect `gateway_count 1`, `gateway_programmed{gateway="sllb-sample",...} 1`,
and both DGs' `endpoints`/`max_endpoints` series now labeled with
`gateway="sllb-sample", gateway_namespace="default"` (the referenced-and-accepted
Gateway branch of the union), with `endpoints 0` and `ready 0` until targets
exist.

> Creating an accepted Gateway triggers LB Deployment creation from the template.
> On a bare cluster its Pods will not run (missing image/NADs) — this is harmless
> for metrics testing.

### 2. Target Pods to populate `endpoints` and `ready`

The DistributionGroup reconciler discovers endpoints by reading each matching
Pod's `k8s.v1.cni.cncf.io/network-status` annotation and CIDR-matching the
advertised secondary IPs against the GatewayConfiguration's `internalSubnets`.
It reads only the annotation — it does not verify that a real interface exists —
so **Multus/Whereabouts are not required for metrics testing**. Plain Pods with
a hand-written annotation are sufficient.

```bash
cat <<'EOF' | kubectl apply -f -
apiVersion: v1
kind: Pod
metadata:
  name: target-sample-1
  namespace: default
  labels:
    app: target-application-sample
  annotations:
    k8s.v1.cni.cncf.io/network-status: |
      [
        {"name":"kindnet","interface":"eth0","default":true,"ips":["10.244.0.50"]},
        {"name":"default/macvlan-nad-1","interface":"net1","ips":["169.111.100.11"]}
      ]
spec:
  containers:
    - name: pause
      image: registry.k8s.io/pause:3.9
---
apiVersion: v1
kind: Pod
metadata:
  name: target-sample-2
  namespace: default
  labels:
    app: target-application-sample
  annotations:
    k8s.v1.cni.cncf.io/network-status: |
      [
        {"name":"kindnet","interface":"eth0","default":true,"ips":["10.244.0.51"]},
        {"name":"default/macvlan-nad-1","interface":"net1","ips":["169.111.100.12"]}
      ]
spec:
  containers:
    - name: pause
      image: registry.k8s.io/pause:3.9
EOF
```

The load-bearing parts of the annotation are: a non-default entry
(`default` absent or false) whose `ips` contains an address inside the
GatewayConfiguration's `internalSubnet` CIDR. The `name` field is **not** matched
by the scraper — it is cosmetic here.

Once the Pods are `Running`, expect (for each DG sharing the selector):

```
meridio_2_distributiongroup_endpoints{dg="distributiongroup-sample",gateway="sllb-sample",gateway_namespace="default",namespace="default"} 2
meridio_2_distributiongroup_ready{dg="distributiongroup-sample",namespace="default"} 1
```

Watch it live:

```bash
watch -n1 'curl -s http://127.0.0.1:8080/metrics | grep ^meridio'
```

## What can and cannot be observed locally

Observable with the setup above (no data plane needed):

- `gateway_count` transitioning 0 → 1 on Gateway acceptance.
- `gateway_programmed` series appearing per Gateway destined for this controller
  by its GatewayClass — emitted regardless of the `Accepted` condition, so it
  appears (as `0`) even for a class-ours Gateway that is not yet accepted, and
  becomes `1` once the reconciler sets `Programmed=True`. Note `Programmed` is set
  by the Gateway reconciler based on observing the LB Deployment, not on its Pods
  actually running — so it can read `1` locally even though no data plane exists.
- `distributiongroup_ready`, `distributiongroup_endpoints`, and per-Gateway
  attribution, via fake-annotation target Pods.
- `distributiongroup_max_endpoints` per-DG capacity from `spec.maglev.maxEndpoints`.
- Both forms of the `gateway`/`gateway_namespace` label on the DG's `endpoints`
  and `max_endpoints` series:
  - `gateway=""` (empty) — emitted when the collector cannot associate the DG
    with any Gateway. A DG is associated with a Gateway when either it references
    an `Accepted=True` Gateway, or it already owns endpoint slices scoped to a
    Gateway; when neither holds, the DG still gets one series with an empty
    `gateway` label rather than disappearing from the metric stream. In the
    walkthrough above you see this before the Gateway is accepted (the DG
    references `sllb-sample`, but it is not yet accepted and has no slices).
  - `gateway="<name>"` — emitted once the DG is associated with a Gateway, either
    because the referenced Gateway becomes `Accepted=True` (as in step 1 above) or
    because endpoint slices exist for it (step 2).

Not meaningfully observable without a full data plane (belongs in the e2e suite):

- Endpoint counts driven by *real* secondary-network IPs from Multus/Whereabouts.
- Per-Gateway attribution across multiple *distinct* Gateways.

## Troubleshooting

- **`gateway_count 0`, DG series show `gateway=""`** — the Gateway is not
  `Accepted=True`. Check why:
  ```bash
  kubectl get gateway sllb-sample -n default -o jsonpath='{.status.conditions}' | jq .
  ```
  The `Accepted=False` message names the rejected field (most commonly a missing
  or invalid `GatewayConfiguration` reference).

  Cross-check with `gateway_programmed`, which is gated on GatewayClass ownership
  rather than `Accepted`, so its presence tells you whether the Gateway is even
  destined for this controller:
  - a `gateway_programmed{gateway="sllb-sample"}` series **is present** (reading
    `0`) — the Gateway's `gatewayClassName` resolves to a GatewayClass whose
    `controllerName` is ours, so this controller owns it; it is simply not
    accepted/programmed yet (the `Accepted=False` case above).
  - **no** `gateway_programmed` series for the Gateway — its `gatewayClassName`
    does not resolve to one of our GatewayClasses (wrong/missing class, or a class
    owned by a different controller), so this controller is not managing it at all.
    Check `spec.gatewayClassName` and the target GatewayClass's `controllerName`.

- **`endpoints` stays 0 after Pods are Running** — check whether slices were
  created:
  ```bash
  kubectl get lbeslice -n default
  ```
  If slices exist but the metric is 0, look at the collector's slice counting. If
  no slices exist, the reconciler is not matching/scraping the Pods — verify the
  Pod labels match the DG selector and the annotation IP falls inside the
  GatewayConfiguration `internalSubnet` CIDR.

- **`/metrics` returns a 500 with "informer cache did not sync"** — the scrape
  landed during the manager's initial cache-sync window (or the cache genuinely
  cannot sync, e.g. a required CRD is missing). This is the collectors'
  `--metrics-collect-timeout` guard surfacing an actionable error rather than
  hanging. Confirm all required CRDs are installed; the error clears once the
  cache syncs.

## Scraping with an in-cluster Prometheus

The sections above serve metrics over plain HTTP to a local `curl`. This section
covers the deployed path: the controller-manager serving **secure** metrics and a
real Prometheus scraping it via a `ServiceMonitor`.

The `ServiceMonitor` is applied separately (not part of `config/default`) so the
Kustomize base does not depend on the Prometheus Operator CRDs — `kubectl apply -k
config/default` must work on a cluster without the Operator installed.

### Three ports, three roles

This section touches three different HTTP endpoints. Keeping them straight avoids
most of the confusion:

| Port | Belongs to | Who talks to it | Used for |
| --- | --- | --- | --- |
| `8443` | the controller-manager's metrics endpoint | Prometheus (the scraper) | serving `/metrics` over HTTPS |
| `9090` | the Prometheus server's own web UI / HTTP API | you, the operator | checking scrape targets and running queries |
| `3000` | the Grafana server's web UI | you, the operator | dashboards / Explore |

You never scrape `8443` by hand in this flow — Prometheus does. You port-forward
`9090` (Prometheus) and `3000` (Grafana) to your machine to *look at the result*.

### What the base deploy already provides

`config/default` renders a metrics Service and (with secure serving enabled) a
cert-manager certificate for it. `make deploy` applies these; the controller-manager
then serves metrics over HTTPS on port `8443`:

- Service `meridio-2-controller-manager-metrics-service`, labels
  `control-plane: controller-manager` and `app.kubernetes.io/name: meridio-2`,
  port name `https`, `targetPort: 8443`. This is the Service the ServiceMonitor
  selects.
- Certificate `meridio-2-metrics-certs` → secret `metrics-server-cert`, mounted into
  the manager at `--metrics-cert-path`.
- Auth: the endpoint uses controller-runtime's
  `WithAuthenticationAndAuthorization` filter, so a scraper must present a bearer
  token (a ServiceAccount token) allowed to GET `/metrics`.

A `namePrefix`/`namespace`/`nameSuffix` in the deploy renames this same Service
accordingly — e.g. the e2e `separate-appnetwork` suite deploys it as
`meridio-2-controller-manager-metrics-service-separate-appnet` in namespace
`e2e-separate-appnetwork`. The labels above are unchanged by renaming, so the
ServiceMonitor selector below still matches.

### 1. Install kube-prometheus-stack

This installs the Prometheus Operator, a Prometheus server, and Grafana into a
`monitoring` namespace.

```bash
helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
helm repo update
helm install prometheus prometheus-community/kube-prometheus-stack \
  --namespace monitoring --create-namespace \
  --set prometheus.prometheusSpec.serviceMonitorSelectorNilUsesHelmValues=false
```

`serviceMonitorSelectorNilUsesHelmValues=false` makes Prometheus adopt
ServiceMonitors in any namespace regardless of labels. Without it, the chart
defaults to selecting only ServiceMonitors carrying its own release label, and the
ServiceMonitor below would be ignored.

### 2. Apply a ServiceMonitor targeting the metrics Service

A `ServiceMonitor` tells the Prometheus Operator which Service to scrape. Apply it
in the **same namespace** the controller-manager was deployed to. The
`selector.matchLabels` are the metrics Service's labels (a subset match — the
Service may carry extra labels, e.g. an e2e-suite label). Adjust `namespace` to
match your deploy.

```bash
kubectl apply -f - <<'EOF'
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: meridio-2-controller-manager-metrics-monitor
  namespace: e2e-separate-appnetwork   # the controller-manager's namespace
spec:
  selector:
    matchLabels:
      control-plane: controller-manager
      app.kubernetes.io/name: meridio-2
  endpoints:
    - path: /metrics
      port: https          # the Service's port named "https" (targetPort 8443)
      scheme: https
      bearerTokenFile: /var/run/secrets/kubernetes.io/serviceaccount/token
      tlsConfig:
        # insecureSkipVerify: the scrape is still encrypted (HTTPS) and
        # authenticated (bearer token), but the serving certificate is not
        # CA-verified. Verified TLS is not used here because a ServiceMonitor's
        # tlsConfig secret refs resolve only in Prometheus's own namespace, while
        # the cert-manager metrics-server-cert lives in the controller-manager's
        # namespace (there is no namespace field on tlsConfig secret refs). Verified
        # TLS would require copying/issuing that CA into the Prometheus namespace.
        insecureSkipVerify: true
EOF
```

The `bearerTokenFile` is Prometheus's own ServiceAccount token; that SA must be
authorized to GET `/metrics` (see Troubleshooting on 401/403).

### 3. Verify the scrape (Prometheus, port 9090)

Port-forward the **Prometheus server's** UI/API port (`9090`) to your machine:

```bash
kubectl -n monitoring port-forward svc/prometheus-kube-prometheus-prometheus 9090:9090
```

Now `http://localhost:9090` is the Prometheus web UI/API:

- **Targets:** open `http://localhost:9090/targets` and confirm the target for the
  metrics Service is **UP** with no scrape error. Discovery can lag a few tens of
  seconds after applying the ServiceMonitor (the Operator regenerates the Prometheus
  config, then Prometheus reloads it) — a missing target right after applying is
  usually just this lag.
- **Query a metric** via the HTTP API:
  ```bash
  curl -s 'http://localhost:9090/api/v1/query?query=meridio_2_gateway_count'
  ```

Expected series match the deployed topology (for the `separate-appnetwork` suite:
`gateway_count 2`; `gateway_programmed` = 1 for each of `gw-a1`/`gw-a2`;
`distributiongroup_ready`, `distributiongroup_endpoints`, and
`distributiongroup_max_endpoints` for each DG).

> **Label note:** the collectors emit a `namespace` label, but the scrape target
> also injects a `namespace` label (the target Pod's namespace). On collision
> Prometheus renames the metric's own label to **`exported_namespace`**. So filter
> the DG/Gateway namespace with `exported_namespace="..."`; `namespace` reflects the
> scraped Pod's namespace. Set `honorLabels: true` on the endpoint if you want the
> collector's `namespace` to win the name instead.

### 4. Grafana (port 3000)

Port-forward the **Grafana server's** UI (its Service listens on `80`; map it to a
local `3000`):

```bash
kubectl -n monitoring port-forward svc/prometheus-grafana 3000:80
```

Open `http://localhost:3000`. Username `admin`; read the password from the secret
(do **not** assume the chart default — it may have been overridden):

```bash
kubectl -n monitoring get secret prometheus-grafana -o jsonpath='{.data.admin-password}' | base64 -d; echo
```

Use **Explore** with the built-in Prometheus datasource and query `meridio_2_*`.
These are gauge-style state metrics that change only when the underlying CRs change,
so **Stat** or **Table** panels read more naturally than a time-series graph.

### Troubleshooting (in-cluster)

- **Target missing from `/targets`** — usually discovery lag; wait ~30–60s. If it
  never appears, confirm the ServiceMonitor's `selector.matchLabels` are a subset of
  the metrics Service's labels, and that the Service has endpoints
  (`kubectl -n <ns> get endpoints -l control-plane=controller-manager` should show
  the manager Pod IP on `:8443`). Also check the Operator regenerated the config
  (`kubectl -n monitoring logs deploy/prometheus-kube-prometheus-operator`).
- **Target DOWN with 401/403** — a token/authorization problem at the manager's
  `WithAuthenticationAndAuthorization` filter. Two separate pieces are involved:
  - The manager validates the scraper's token via TokenReview/SubjectAccessReview.
    The base grants the manager's own SA this ability
    (`config/rbac/metrics_auth_role.yaml` + `_binding`, bound to the
    `controller-manager` SA), so this normally works out of the box.
    A 401 here points at a missing/invalid bearer token on the scrape.
  - Authorization to GET `/metrics`: the base ships a `metrics-reader` ClusterRole
    (`config/rbac/metrics_reader_role.yaml`, `get` on the `/metrics`
    nonResourceURL) but **no binding for Prometheus's ServiceAccount** — that SA
    lives in the Operator's namespace. kube-prometheus-stack's own ClusterRole
    typically already grants its Prometheus SA `get /metrics` cluster-wide (which is
    why the scrape works without extra wiring); a 403 means it does not, and you must
    bind `metrics-reader` (or equivalent) to the scraping SA yourself.
- **Target DOWN with x509/TLS error** — the endpoint is HTTPS; ensure `scheme: https`
  and `insecureSkipVerify: true` are set (the Service serves a cert-manager cert
  whose CA Prometheus does not trust by default).

## Note: implications of a future OpenTelemetry (OTEL) migration

The collectors are Prometheus-native today (`prometheus.Collector` registered
against controller-runtime's Prometheus registry). If Meridio-2 later moves toward
OTEL, the implications differ sharply by scope:

- **OTEL Collector scraping `/metrics` (no code change).** Run an OTEL Collector
  with its `prometheusreceiver` pointed at the existing HTTPS `/metrics` endpoint and
  export OTLP downstream. This is purely a deployment change and the reason the
  current design is not a dead end.
- **In-process Prometheus→OTEL bridge (moderate).** Feed the existing registry into
  an OTEL `MeterProvider` via `go.opentelemetry.io/contrib/bridges/prometheus` and
  export OTLP. The collector code stays; only `pkg/app` wiring changes. The bridge is
  a migration aid rather than a full translation, so verify against its current
  documentation whether any of our metrics are affected before relying on it.
- **Rewriting instrumentation to the OTEL metrics API (largest).** Here the
  pull-based `Collect()` logic maps reasonably onto OTEL *observable* (async) gauge
  callbacks — the "read the informer cache when asked" shape survives. Two things do
  **not** port cleanly:
  - **The cache-sync fail-fast contract.** On a scrape before the informer cache has
    synced, `Collect()` emits `prometheus.NewInvalidMetric(...)`, which makes
    client_golang's handler return an HTTP 500 to the scraper (an actionable error
    instead of a partial or blocking scrape — see `internal/metrics/cache_sync.go`
    and the `Collect` methods). OTEL observable callbacks have **no equivalent** of
    failing a collection back to the reader; a callback cannot turn "cache not synced"
    into a scrape error. This behavior would need redesigning (e.g. a separate
    `cache_synced` gauge) rather than a literal port.
  - **controller-runtime's own metrics stay Prometheus-native.** The framework
    registers reconcile/workqueue metrics against its Prometheus registry and does not
    emit OTEL. Even after rewriting our collectors, those built-ins still require the
    Prometheus registry (bridged or scraped) — you cannot fully leave Prometheus by
    changing only our code.

  Metric naming/semantics also differ (Prometheus `snake_case` + the `+Inf` sentinel
  used by `distributiongroup_max_endpoints` for unbounded strategies vs OTEL dotted
  names/units); renaming is a breaking change for any dashboards or alerts.
