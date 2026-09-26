# Scraping Meridio-2 Metrics with Prometheus

How to wire a real in-cluster Prometheus/Grafana to Meridio-2 components' secure
metrics endpoints, and what an eventual OpenTelemetry migration would entail.

The [shared setup](#shared-setup) (install kube-prometheus-stack, port-forward,
Grafana) is common to every component. Each component then has its own chapter for
the parts that genuinely differ — the controller-manager is scraped via a
`ServiceMonitor` over a Service, while the network-sidecar is scraped per-Pod via a
`PodMonitor`, with different auth/cert mechanics.

> **Structure is provisional.** Only the controller-manager and network-sidecar are
> covered so far. Once the LB and Router metrics are implemented, this doc's
> chapter-vs-split structure should be revisited — their collection models (nfqlb /
> nftables, BIRD via `birdc`) may share less with the above than these two do.

## Choosing ServiceMonitor vs PodMonitor

Both CRDs make Prometheus scrape **every matching Pod individually** — the Operator
expands either into one target per Pod (a ServiceMonitor discovers Pods via a
Service's Endpoints; a PodMonitor selects Pods by label directly). So the choice is
**not** "ServiceMonitor for Deployments, PodMonitor for Pods" — that shorthand is
misleading, since a replicated Deployment is scraped per-Pod under both. The actual
deciding factors are:

1. **Is there already a metrics Service?** If yes, a ServiceMonitor reuses it. If not,
   a PodMonitor selects Pods by label without inventing one.
2. **Do you need verified TLS?** A serving certificate's SAN must match what Prometheus
   connects to. A Service provides a **stable DNS name** to put in the SAN; ephemeral
   per-Pod IPs cannot be pre-covered, forcing `insecureSkipVerify` (encrypted but
   unverified).

This is why the two components below differ, despite **both being Deployments**:

- **Controller-manager** — kubebuilder scaffolds a metrics Service, and its
  cert-manager serving cert keys its SAN off that Service's DNS name. A Service
  already exists *and* enables verifiable TLS → **ServiceMonitor**.
- **Network-sidecar** — runs in arbitrary application Pods with no metrics Service and
  no stable per-Pod DNS/SAN → **PodMonitor** + self-signed cert + `insecureSkipVerify`.

> Note for repackagers: if you deploy the controller-manager **without** the kubebuilder
> Service (e.g. a custom Helm chart that omits it), the ServiceMonitor recipe below no
> longer applies as-is — either add an equivalent metrics Service, or scrape the
> manager Pods with a PodMonitor (accepting `insecureSkipVerify`, since you'd then lack
> the stable-SAN cert the Service enables). The choice follows the two factors above,
> not the workload type.

## Shared setup

This is the common Prometheus/Grafana wiring used by every component chapter below;
do it once.

### Three ports, three roles

These flows touch three different HTTP endpoints. Keeping them straight avoids most
of the confusion:

| Port | Belongs to | Who talks to it | Used for |
| --- | --- | --- | --- |
| `8443` | a component's metrics endpoint | Prometheus (the scraper) | serving `/metrics` over HTTPS |
| `9090` | the Prometheus server's own web UI / HTTP API | you, the operator | checking scrape targets and running queries |
| `3000` | the Grafana server's web UI | you, the operator | dashboards / Explore |

You never scrape `8443` by hand — Prometheus does. You port-forward `9090`
(Prometheus) and `3000` (Grafana) to your machine to *look at the result*.

### Install kube-prometheus-stack

This installs the Prometheus Operator, a Prometheus server, and Grafana into a
`monitoring` namespace.

```bash
helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
helm repo update
helm install prometheus prometheus-community/kube-prometheus-stack \
  --namespace monitoring --create-namespace \
  --set prometheus.prometheusSpec.serviceMonitorSelectorNilUsesHelmValues=false \
  --set prometheus.prometheusSpec.podMonitorSelectorNilUsesHelmValues=false
```

Both `NilUsesHelmValues=false` flags make Prometheus adopt ServiceMonitors **and**
PodMonitors in any namespace regardless of labels. Without them, the chart defaults
to selecting only monitors carrying its own release label (`release: prometheus`),
and the monitors below would be ignored. (Alternatively, label each monitor with
`release: prometheus` instead of setting these flags.)

### Verify a scrape (Prometheus, port 9090)

Port-forward the **Prometheus server's** UI/API port (`9090`):

```bash
kubectl -n monitoring port-forward svc/prometheus-kube-prometheus-prometheus 9090:9090
```

Then at `http://localhost:9090`:

- **Targets:** `http://localhost:9090/targets` — confirm the component's target is
  **UP** with no scrape error. Discovery can lag a few tens of seconds after
  applying a monitor (the Operator regenerates the Prometheus config, then Prometheus
  reloads it) — a missing target right after applying is usually just this lag.
- **Query** via the HTTP API:
  ```bash
  curl -s 'http://localhost:9090/api/v1/query?query=<metric>' | jq .
  ```

> Note: the port-forward dies when its terminal closes or the Prometheus Pod
> restarts. An empty query result plus a failed `curl` to `:9090` usually just means
> the forward dropped — restart it before assuming the scrape broke.

### Grafana (port 3000)

Port-forward Grafana's UI (its Service listens on `80`; map it to a local `3000`):

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
Grafana queries the same Prometheus — a metric absent there is absent here too;
Grafana is only a view.

## Controller-manager

The controller-manager is scraped via a `ServiceMonitor` over a stable metrics
Service. The `ServiceMonitor` is applied separately (not part of `config/default`)
so the Kustomize base does not depend on the Prometheus Operator CRDs — `kubectl
apply -k config/default` must work on a cluster without the Operator installed.

It exposes these custom metrics (all pull-based from the informer cache):

- `meridio_2_gateway_count` (no labels) — number of Gateways `Accepted=True` by this
  controller.
- `meridio_2_gateway_programmed{gateway,namespace}` — `Programmed` condition (0/1)
  per Gateway destined for this controller by its GatewayClass.
- `meridio_2_distributiongroup_ready{dg,namespace}` — DG `Ready` condition (0/1).
- `meridio_2_distributiongroup_endpoints{gateway,gateway_namespace,dg,namespace}` —
  current endpoint count per DG per Gateway.
- `meridio_2_distributiongroup_max_endpoints{gateway,gateway_namespace,dg,namespace}`
  — capacity per DG per Gateway (`+Inf` for unbounded strategies).

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

### Apply a ServiceMonitor targeting the metrics Service

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

> `bearerTokenFile` is deprecated on ServiceMonitor (still functional) in favor of
> `authorization`. It is kept here because it reads Prometheus's automounted SA token
> directly — the simplest working form. Switching to `authorization.credentials`
> would require minting a token Secret in this namespace (as the network-sidecar
> chapter must, since PodMonitor dropped `bearerTokenFile` entirely), which is more
> setup for no functional gain here.

### Expected series

Verify via the [shared setup](#verify-a-scrape-prometheus-port-9090) (port-forward
`9090`, check `/targets` is UP, query). Expected series match the deployed topology
(for the `separate-appnetwork` suite: `gateway_count 2`; `gateway_programmed` = 1 for
each of `gw-a1`/`gw-a2`; `distributiongroup_ready`, `distributiongroup_endpoints`, and
`distributiongroup_max_endpoints` for each DG).

> **Label note:** the collectors emit a `namespace` label, but the scrape target
> also injects a `namespace` label (the target Pod's namespace). On collision
> Prometheus renames the metric's own label to **`exported_namespace`**. So filter
> the DG/Gateway namespace with `exported_namespace="..."`; `namespace` reflects the
> scraped Pod's namespace. Set `honorLabels: true` on the endpoint if you want the
> collector's `namespace` to win the name instead.

### Troubleshooting (controller-manager)

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

## Network-sidecar

The network-sidecar is fundamentally different from the controller-manager: it runs
as a container in **every application Pod**, so each sidecar is its own per-Pod
metrics source. It is scraped via a **`PodMonitor`** (each Pod scraped by IP),
whereas the controller-manager uses a `ServiceMonitor` over one Service.

Pod identity is 1:1 with the scrape target, so the sidecar metrics carry **no `pod`
label** — the scraper injects `pod`/`instance` automatically. The metrics are:
`meridio_2_sidecar_vips_configured{gateway}`,
`meridio_2_sidecar_nexthops{gateway,ip_family}` (both gauges from the ENC), and
`meridio_2_sidecar_config_errors_total{reason}` (a push counter of failed netlink
ops; `reason` ∈ `link`/`address`/`route`, pre-initialized to 0).

The sidecar's metrics endpoint is **off by default** (`--metrics-bind-address`
defaults to `0`). The steps below enable it and wire per-Pod scraping. Substitute
`<ns>` with the namespace the application Pods (and their sidecar SA) run in, and the
`app:` selector value with your application Pod label.

### 1. Enable the sidecar metrics endpoint

Add `--metrics-bind-address=:8443` to the `network-sidecar run` args and declare a
`metrics` container port. Secure serving is the binary default; controller-runtime's
metrics server **self-signs** a serving cert when none is mounted, so no cert
plumbing into the application Pod is needed.

```yaml
# in the network-sidecar container of the application Pod spec:
        args:
          - run
          - --pod-name=$(POD_NAME)
          # …existing args…
          - --metrics-bind-address=:8443
        ports:
        - name: metrics
          containerPort: 8443
          protocol: TCP
```

### 2. Sidecar-side auth RBAC

The secure endpoint's auth filter validates each scraper via TokenReview /
SubjectAccessReview, so the **sidecar's** ServiceAccount needs those (cluster-scoped)
verbs — bound with a ClusterRoleBinding:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: meridio-2-network-sidecar-metrics-auth
rules:
- apiGroups: ["authentication.k8s.io"]
  resources: ["tokenreviews"]
  verbs: ["create"]
- apiGroups: ["authorization.k8s.io"]
  resources: ["subjectaccessreviews"]
  verbs: ["create"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: meridio-2-network-sidecar-metrics-auth-<ns>
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: meridio-2-network-sidecar-metrics-auth
subjects:
- kind: ServiceAccount
  name: meridio-2-network-sidecar   # the sidecar's SA
  namespace: <ns>
```

The ClusterRole is shared/apply-once; the binding is per-namespace (name-suffixed
with `<ns>` so covering multiple namespaces doesn't clobber a binding). To cover
another namespace, add another ClusterRoleBinding (or append to this one's
`subjects`) — the ClusterRole is not duplicated.

### 3. Scraper-side identity (SA + token Secret + get /metrics)

The auth filter also requires the **scraper** to (1) authenticate with a bearer token
and (2) be authorized to GET `/metrics`. A PodMonitor's `authorization.credentials`
Secret must live in the **monitored namespace** (`<ns>`), so mint a self-contained
scrape identity there rather than referencing Prometheus's own SA token (which lives
in the `monitoring` namespace and can't be referenced cross-namespace):

```yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: sidecar-metrics-scraper
  namespace: <ns>
---
# Long-lived token Secret for that SA — the PodMonitor reads its "token" key.
apiVersion: v1
kind: Secret
metadata:
  name: sidecar-metrics-scraper-token
  namespace: <ns>
  annotations:
    kubernetes.io/service-account.name: sidecar-metrics-scraper
type: kubernetes.io/service-account-token
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: meridio-2-sidecar-metrics-reader
rules:
- nonResourceURLs: ["/metrics"]
  verbs: ["get"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: meridio-2-sidecar-metrics-reader-<ns>
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: meridio-2-sidecar-metrics-reader
subjects:
- kind: ServiceAccount
  name: sidecar-metrics-scraper
  namespace: <ns>
```

### 4. Apply a PodMonitor

```yaml
apiVersion: monitoring.coreos.com/v1
kind: PodMonitor
metadata:
  name: meridio-2-network-sidecar
  namespace: <ns>
  # If you did NOT set podMonitorSelectorNilUsesHelmValues=false in the shared setup,
  # add: labels: {release: prometheus}
spec:
  selector:
    matchLabels:
      app: <application-pod-label>
  podMetricsEndpoints:
    - port: metrics          # the sidecar container port named "metrics" (8443)
      scheme: https
      authorization:
        type: Bearer
        credentials:
          name: sidecar-metrics-scraper-token
          key: token
      tlsConfig:
        insecureSkipVerify: true
```

> **Why `insecureSkipVerify`.** The scrape is encrypted and bearer-authenticated, but
> the serving cert is not CA-verified. This is **structurally required** for per-Pod
> scraping, not a shortcut: the self-signed cert has SANs `localhost`/`127.0.0.1`,
> which can never match the ephemeral Pod IP Prometheus scrapes. Verified TLS would
> need per-Pod-IP certs (a CSI cert driver) or a service mesh — out of scope here.

### Expected series

Verify via the [shared setup](#verify-a-scrape-prometheus-port-9090). For an
application Deployment of 2 replicas attached to two gateways, expect (one set per
Pod, distinguished by the injected `pod`/`instance` labels):

- `meridio_2_sidecar_vips_configured{gateway="gw-a1"|"gw-a2"}` = 1 each
- `meridio_2_sidecar_nexthops{gateway=…, ip_family="IPv4"}` = 2 each
- `meridio_2_sidecar_config_errors_total{reason="link"|"address"|"route"}` = 0 (all
  three reasons present at 0 — pre-initialized — until a real netlink op fails)

> **Consuming the error counter across restarts.** `config_errors_total` is a counter
> in the sidecar's process memory; it resets to 0 on Pod/process restart. Consume it
> via `rate()`/`increase()` (both reset-aware). For restart-aware interpretation pair
> it with **`process_start_time_seconds`** (exposed for free by controller-runtime's
> process collector on the same endpoint) rather than container-restart metrics — the
> former reflects controller-process restarts even when a supervisor keeps the
> container alive.

### Troubleshooting (network-sidecar)

- **Target DOWN, HTTP 500, log "Authentication failed … tokenreviews … forbidden … at
  the cluster scope"** — the sidecar SA is missing the cluster-scoped auth grant from
  step 2. Verify with
  `kubectl auth can-i create tokenreviews --as=system:serviceaccount:<ns>:meridio-2-network-sidecar`.
- **No `network-sidecar` target in `/targets`** — the PodMonitor wasn't adopted:
  either set `podMonitorSelectorNilUsesHelmValues=false` on the stack, or label the
  PodMonitor `release: prometheus`. Confirm with
  `kubectl -n monitoring get prometheus -o jsonpath='{.items[0].spec.podMonitorSelector}'`.
- **`config_errors_total` absent while the gauges are present** — the running image
  predates the counter; rebuild/reload the sidecar image and roll the application
  Deployment.

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
    instead of a partial or blocking scrape — see `internal/metrics/util/cache_sync.go`
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
