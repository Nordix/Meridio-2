# Scraping the stateless-load-balancer

Prerequisites: [shared-setup.md](shared-setup.md) and
[podmonitor-https.md](podmonitor-https.md).

The `loadbalancer` container runs in **every LB Pod** the gateway controller creates,
alongside the [router](router.md) container, so like the router it is a per-Pod metrics
source scraped via a `PodMonitor`. Its metrics are read from nfqlb, the nftables table,
the kernel policy rules, and `/proc/net/netfilter/nfnetlink_queue` at scrape time. See
[the LB controller doc](../../controllers/loadbalancer.md#metrics) for the full catalog,
how each metric is produced, and the counter-reset notes.

Its metrics endpoint is **off by default** (`--metrics-bind-address` defaults to `0`).

Throughout, `NS` is the namespace the Gateway and its LB Deployment run in.

## Values for the shared commands

| Shared variable | Value |
| --- | --- |
| `POOL` | the PodMonitor's name from step 4 (matched as a substring of `podMonitor/<ns>/<name>/<index>`) |
| `METRIC` | any from the table below, e.g. `meridio_2_lb_flow_matches_total` |
| Recipe `COMPONENT` | `loadbalancer` |
| Recipe `SA` | the LB Pod's SA, shared by both containers (see step 2) |

## Identifying LB Pods

See [the router chapter](router.md#identifying-lb-pods) — the LB Pod is the same Pod,
identified by the same gateway-controller labels:

```bash
NS=<gateway namespace>
GW=<gateway name>

POD=$(kubectl -n $NS get pod \
  -l app.kubernetes.io/managed-by=gateway-controller.meridio-2.nordix.org,gateway.networking.k8s.io/gateway-name=$GW \
  -o name | head -1)
```

## Metrics

One LB container serves one Gateway, so every series carries `gateway`; the scraper
injects `pod` and `instance`. Full descriptions, sources, and the collector-health and
counter-reset behavior are in
[the LB controller doc](../../controllers/loadbalancer.md#metrics); the summary:

| Metric | Labels | Notes |
| --- | --- | --- |
| `lb_flow_matches_total` | `gateway`, `l34route` | packets matched per L34Route flow |
| `lb_active_targets` | `gateway`, `dg` | targets currently serving (Maglev `Active:` from `nfqlb show`) |
| `lb_nftables_vip_set_size` | `gateway` | size of the nftables VIP sets (IPv4+IPv6) |
| `lb_drops_total` | `gateway`, `reason` (`no_flow`, `no_targets`) | drop-accounting chain |
| `lb_policy_routes` | `gateway`, `ip_family` (`IPv4`, `IPv6`) | kernel policy routing rules owned by this LB |
| `lb_nfqueue_items` | `gateway` | netfilter-queue backlog (gauge; normally 0) |
| `lb_nfqueue_dropped_total` | `gateway`, `reason` (`queue`, `user`) | queue-full (saturation) vs verdict-delivery (error) drops |
| `lb_collector_errors_total` | `gateway`, `collector` | a scrape-time read failed; the series it would read is absent for that scrape |
| `lb_route_config_errors_total` | `gateway`, `reason` (`route_config`, `vip_config`) | failed data-plane config op in the reconcile path |

Most are read fresh at scrape time; the two `*_errors_total` are counted where the
failure happens. Counters reset on process restart (and some on object churn), so
consume them with `rate()` / `increase()`.

## 1. Enable the endpoint

Add `--metrics-bind-address` and a metrics container port to the **`loadbalancer`**
container. As with the router, edit the ConfigMap-mounted template
(`config/templates/lb-deployment.yaml`), not the generated Deployment (the controller
reconciles it back) — either patch it before deploying or override it with a Kustomize
`configMapGenerator` using `behavior: replace`.

The `router` and `loadbalancer` containers share the Pod's network namespace, so they
**cannot bind the same port number**. If you also enabled router metrics on `:8443`,
give the LB a different number, e.g. `:8444`:

```yaml
# in the loadbalancer container of the LB deployment template:
        args:
          - run
          - --metrics-bind-address=:8444
        ports:
        - name: metrics
          containerPort: 8444
          protocol: TCP
```

A `PodMonitor` matches its port **by name** across all containers in the Pod. So if the
router and LB container ports are **both** named `metrics` (on their distinct numbers),
one PodMonitor scrapes both — the `bgp_*`/`bfd_*` and `_lb_*` series arrive on the same
target, told apart by metric name. If you instead want a separate PodMonitor per
container, give the LB port a distinct name (e.g. `lb-metrics`) and select it in step 4.

> The LB process runs as non-root with `supplementalGroups: [0]` (already in the
> template): the nfqueue collector reads `/proc/net/netfilter/nfnetlink_queue`, which is
> `0440 root:root`. Without that group the `lb_nfqueue_*` series are absent and
> `lb_collector_errors_total{collector="nfqueue"}` climbs. Do not strip it from a custom
> template, and on OpenShift allow it in the SCC.

No controller-manager restart is needed: the reconciler re-reads the template on every
reconcile, and kubelet refreshes the mounted ConfigMap on its own (up to a minute).
Touch the Gateway to trigger a reconcile once the mounted file shows the change.

## 2. Apply the RBAC

The LB shares the router's ServiceAccount, so the metrics-auth grant is the same one —
if you already enabled router scraping in this namespace, **no new RBAC is needed**.
Otherwise follow [podmonitor-https.md](podmonitor-https.md) steps 1 and 2, with
`NS` → the gateway's namespace, binding the metrics-auth role to the LB Pod's SA. Read
the SA off a running Pod rather than hardcoding it (it is whatever the manager's
`--lb-service-account` was set to):

```bash
kubectl -n $NS get $POD -o jsonpath='{.spec.serviceAccountName}{"\n"}'
```

## 3. Verify the endpoint before involving Prometheus

As in the [router chapter](router.md#3-verify-the-endpoint-before-involving-prometheus),
confirm the flag reached the container and the SA may create TokenReviews, then scrape
the endpoint from inside the **`loadbalancer`** container (note `-c loadbalancer` and the
port you chose):

```bash
# a) the flag reached the loadbalancer container
kubectl -n $NS get $POD \
  -o jsonpath='{.spec.containers[?(@.name=="loadbalancer")].args}{"\n"}'

# b) the LB SA may create TokenReviews, without which every scrape 403s
SA=$(kubectl -n $NS get $POD -o jsonpath='{.spec.serviceAccountName}')
kubectl auth can-i create tokenreviews --as=system:serviceaccount:$NS:$SA

# c) scrape from inside the loadbalancer container (self-signed cert, bearer token)
SCRAPER_SA=<the scraper ServiceAccount from podmonitor-https.md step 2>
SECRET=$(kubectl -n $NS get secret \
  -o jsonpath="{.items[?(@.metadata.annotations['kubernetes\.io/service-account\.name']=='$SCRAPER_SA')].metadata.name}")
TOKEN=$(kubectl -n $NS get secret $SECRET -o jsonpath='{.data.token}' | base64 -d)
kubectl -n $NS exec $POD -c loadbalancer -- \
  wget -qO- --no-check-certificate --header="Authorization: Bearer $TOKEN" \
  https://127.0.0.1:8444/metrics | grep -E 'lb_'
```

A `401`/`403` is the auth chain, not the metrics (Prometheus would fail identically);
`connection refused` means `--metrics-bind-address` never took effect (check (a));
metrics served here but the Prometheus target DOWN points at the PodMonitor, its
adoption label, or `insecureSkipVerify` — nothing about the LB.

## 4. Apply a PodMonitor

Follow [podmonitor-https.md](podmonitor-https.md) step 3. If you named the LB port
`metrics` (shared with the router) the router's PodMonitor already scrapes it — nothing
more to do. For a dedicated LB PodMonitor, select the distinct port name:

```yaml
spec:
  selector:
    matchLabels:
      app.kubernetes.io/managed-by: gateway-controller.meridio-2.nordix.org
  podMetricsEndpoints:
  - port: lb-metrics        # the LB container's port name (see step 1)
    scheme: https
    # …authorization and tlsConfig per the recipe…
```

The `managed-by` selector covers all Gateways in the namespace; scope to one by adding
`gateway.networking.k8s.io/gateway-name: <gw>`. Verify with
[shared-setup.md](shared-setup.md#verify-a-scrape), setting `POOL` to the PodMonitor's
name.

## Failures

The shared [common failures](podmonitor-https.md#common-failures) table covers auth,
TLS and adoption. LB-specific:

| Symptom | Cause |
| --- | --- |
| `lb_nfqueue_*` absent, `lb_collector_errors_total{collector="nfqueue"}` rising | the LB cannot read `/proc/net/netfilter/nfnetlink_queue` — the Pod is missing `supplementalGroups: [0]` (custom template, or an OpenShift SCC that strips it). See step 1 |
| a data series absent with `lb_collector_errors_total{collector="..."}` rising | that collector's scrape-time read failed; the `collector` label says which source (`nfqlb`, `nftables`, `route`, `nfqueue`). `nfqlb_busy` is a benign concurrent-scrape skip, not a read failure — do not alert on it |
| `lb_route_config_errors_total` rising | a data-plane config op failed in the reconcile path; check the `reason` (`route_config` / `vip_config`) and the loadbalancer container's NET_ADMIN capability |
| both LB and router metrics DOWN together | if one PodMonitor scrapes both containers (shared `metrics` port name), a single endpoint failure marks both down. Separate PodMonitors (distinct port names) decouple them |

## Why a failed read does not fail the scrape

A failed collector read emits **no series** for its source and increments
`lb_collector_errors_total{collector="..."}`, rather than failing the whole scrape:
Prometheus treats a failed scrape as empty and stales **every** series on the endpoint,
including the LB's controller-runtime and process metrics. So a missing data series plus
a rising `collector_errors` rate means "could not read", not "value is zero".
