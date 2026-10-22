# Scraping the router

Prerequisites: [shared-setup.md](shared-setup.md) and
[podmonitor-https.md](podmonitor-https.md).

The router runs as a container in **every LB Pod** the gateway controller creates, so
like the network-sidecar it is a per-Pod metrics source scraped via a `PodMonitor`.
Its metrics reflect BIRD state read with `birdc` at scrape time, independent of the
router's own connectivity monitoring. See
[the design doc](../../design/router-metrics-source-of-truth.md) for why each metric
is keyed by its own BIRD object rather than correlated to a GatewayRouter CR.

Its metrics endpoint is **off by default** (`--metrics-bind-address` defaults to `0`).

Throughout, `NS` is the namespace the Gateway and its LB Deployment run in.

## Values for the shared commands

| Shared variable | Value |
| --- | --- |
| `POOL` | the PodMonitor's name from step 4 below (matched as a substring of `podMonitor/<ns>/<name>/<index>`) |
| `METRIC` | any from the table below, e.g. `meridio_2_bgp_session_up` |
| Recipe `COMPONENT` | `router` |
| Recipe `SA` | the LB Pod's SA, shared by both containers (see step 2 below) |

## Identifying LB Pods

Several commands below need one LB Pod. The gateway controller stamps three labels on
every LB Pod it creates:

| Label | Value |
| --- | --- |
| `app.kubernetes.io/managed-by` | `gateway-controller.meridio-2.nordix.org` (constant) |
| `gateway.networking.k8s.io/gateway-name` | the Gateway's name |
| `app` | the LB Deployment's name, which the controller derives internally |

Select on the first two. They are stable, documented values, whereas `app` is built
from an internal naming constant and the controller overwrites whatever the template
declared, so constructing it by hand couples your commands to an implementation
detail.

```bash
NS=<gateway namespace>
GW=<gateway name>

POD=$(kubectl -n $NS get pod \
  -l app.kubernetes.io/managed-by=gateway-controller.meridio-2.nordix.org,gateway.networking.k8s.io/gateway-name=$GW \
  -o name | head -1)
```

Dropping the `gateway-name` selector returns LB Pods for every Gateway in the
namespace. Any single Pod works for these checks; each reports only its own BIRD.

## Metrics

Pod identity is 1:1 with the scrape target, so these carry **no `pod` label**; the
scraper injects `pod` and `instance`. They do carry `gateway`, since one router serves
exactly one Gateway.

| Metric | Labels | Notes |
| --- | --- | --- |
| `bgp_session_up` | `gateway`, `gateway_router` | 1 when established |
| `bgp_routes_imported` | `gateway`, `gateway_router` | routes accepted from the peer |
| `bgp_prefixes_announced` | `gateway`, `gateway_router` | prefixes exported to the peer |
| `bgp_session_uptime_seconds` | `gateway`, `gateway_router` | seconds established, 0 while down |
| `bfd_session_up` | `gateway`, `neighbor`, `interface` | keyed by BFD session, not by GatewayRouter |
| `router_metrics_collect_success` | `gateway`, `source` | whether this scrape could read BIRD; `source` ∈ `bgp_sessions` / `bfd_sessions` |
| `router_route_config_errors_total` | `gateway`, `reason` | counter of failed netlink ops in the VIP routing path; `reason` ∈ `rule` / `route`, pre-initialized to 0 |

The first five are read from BIRD at scrape time. `router_route_config_errors_total` is
the exception: a failed netlink op is a point-in-time event with no durable source to
read later, so it is counted where it happens. Being a counter it resets on process
restart, so consume it with `rate()` or `increase()`.

`gateway_router` is the BIRD protocol name with its `NBR-` prefix stripped, which
equals the GatewayRouter name. A **Static** GatewayRouter produces no `bgp_*` series
at all, since it has no BGP session. If it configures BFD, its liveness shows up in
`bfd_session_up`; a Static router without BFD produces no session series in either
family, so a Static-only Gateway can legitimately report nothing beyond
`router_metrics_collect_success`.

## 1. Enable the endpoint

Add `--metrics-bind-address=:8443` to the `router run` args and declare a `metrics`
container port. Secure serving is the binary default and the cert is self-signed, so
nothing else is needed in the Pod spec.

The LB Deployment is rendered by the gateway controller from a ConfigMap-mounted
template, so edit the **template**, not the generated Deployment (which the controller
reconciles back). Either patch `config/templates/lb-deployment.yaml` before deploying,
or override it with a Kustomize `configMapGenerator` using `behavior: replace`.

```yaml
# in the router container of the LB deployment template:
        args:
          - run
          - --metrics-bind-address=:8443
        ports:
        - name: metrics
          containerPort: 8443
          protocol: TCP
```

No controller-manager restart is needed: the reconciler re-reads the template from
disk on every reconcile, and kubelet refreshes the mounted ConfigMap on its own (which
can take up to a minute). Touch the Gateway to trigger a reconcile once the mounted
file shows the change.

> The `router` and `loadbalancer` containers share the Pod and its network namespace,
> so they cannot both bind the same metrics port. If both expose metrics, give them
> distinct ports. A PodMonitor matches its port by name across all containers in the
> Pod, so a port name shared by both containers would make one PodMonitor scrape both.

## 2. Apply the RBAC

Follow [podmonitor-https.md](podmonitor-https.md) steps 1 and 2, substituting
`COMPONENT` → `router` and `NS` → the gateway's namespace. You choose the scraper's
ServiceAccount and Secret names there; the commands below read them back rather than
assuming a spelling.

For `SA`, use the **LB Pod's** ServiceAccount, which both containers share. Read it
off a running Pod rather than hardcoding it, since it is whatever the manager's
`--lb-service-account` (`MERIDIO_LB_SERVICE_ACCOUNT`) was set to:

```bash
kubectl -n $NS get $POD -o jsonpath='{.spec.serviceAccountName}{"\n"}'
```

## 3. Verify the endpoint before involving Prometheus

Worth doing before applying the PodMonitor, because it separates "are the metrics
working" from "is the scrape configured". A DOWN target cannot tell you which broke.

First confirm the two prerequisites that otherwise fail every scrape with no clue as
to why. Both read off a running Pod, so they hold regardless of naming:

```bash
# a) the flag reached the router container
kubectl -n $NS get $POD \
  -o jsonpath='{.spec.containers[?(@.name=="router")].args}{"\n"}'

# b) the router's SA may create TokenReviews (podmonitor-https.md step 1), without which every scrape 403s
SA=$(kubectl -n $NS get $POD -o jsonpath='{.spec.serviceAccountName}')
kubectl auth can-i create tokenreviews --as=system:serviceaccount:$NS:$SA
```

Expect the args to include `--metrics-bind-address` and `can-i` to print `yes`. The
`can-i` call warns that `tokenreviews` is not namespaced, which is expected.

Then scrape the endpoint from inside the router container. The check needs an HTTP
client that can speak HTTPS, send an `Authorization` header, and skip certificate
verification. Running it in the container reaches the endpoint on loopback, where the
cert's SAN matches. Any client meeting those requirements works; the command below
uses busybox `wget`, which the current image provides.

```bash
SCRAPER_SA=<the scraper ServiceAccount you created in podmonitor-https.md step 2>

# Find that SA's token Secret rather than assuming its name.
SECRET=$(kubectl -n $NS get secret \
  -o jsonpath="{.items[?(@.metadata.annotations['kubernetes\.io/service-account\.name']=='$SCRAPER_SA')].metadata.name}")
TOKEN=$(kubectl -n $NS get secret $SECRET -o jsonpath='{.data.token}' | base64 -d)

kubectl -n $NS exec $POD -c router -- \
  wget -qO- --no-check-certificate --header="Authorization: Bearer $TOKEN" \
  https://127.0.0.1:8443/metrics | grep -E 'bgp_|bfd_|collect_success|config_errors'
```

If `SECRET` comes back empty, the token Secret was never created or its
`kubernetes.io/service-account.name` annotation does not name that SA. An alternative
that bypasses the Secret entirely is `kubectl create token $SCRAPER_SA -n $NS`, which
mints a short-lived token; the Secret still has to exist for the PodMonitor, which
cannot call that API.

The variable parts:

| Part | Where it comes from |
| --- | --- |
| `-c router` | the LB Pod has two containers and only `router` serves these metrics. Without it `kubectl` picks the first container |
| `SCRAPER_SA` / `SECRET` | the identity from podmonitor-https.md step 2. The request needs **both** halves of the filter: a token that authenticates and a subject authorized to GET `/metrics` |
| `--no-check-certificate` | the skip-verification requirement. The cert is self-signed, so a verifying client refuses the connection (busybox reports `ssl_client: SSL_connect`). Busybox supports this flag without listing it in `wget --help`; another client spells it differently, e.g. `curl -k` |

Expected output for one BGP GatewayRouter with BFD:

```
meridio_2_bfd_session_up{gateway="gw-a1",interface="vlan-100",neighbor="169.254.10.150"} 1
meridio_2_bgp_prefixes_announced{gateway="gw-a1",gateway_router="gw-a1-router-v4"} 1
meridio_2_bgp_routes_imported{gateway="gw-a1",gateway_router="gw-a1-router-v4"} 1
meridio_2_bgp_session_up{gateway="gw-a1",gateway_router="gw-a1-router-v4"} 1
meridio_2_bgp_session_uptime_seconds{gateway="gw-a1",gateway_router="gw-a1-router-v4"} 2572
meridio_2_router_metrics_collect_success{gateway="gw-a1",source="bfd_sessions"} 1
meridio_2_router_metrics_collect_success{gateway="gw-a1",source="bgp_sessions"} 1
meridio_2_router_route_config_errors_total{gateway="gw-a1",reason="route"} 0
meridio_2_router_route_config_errors_total{gateway="gw-a1",reason="rule"} 0
```

Reading it: both `collect_success` series at 1 means BIRD was queried successfully, so
a session reported down is a real connectivity problem rather than a collection
failure. Asymmetric route counts are normal (here 4 VIPs announced, 1 route imported).
Both `config_errors_total` series at 0 means VIP policy routing was installed cleanly.

The uptime measures the BGP session, not the process, so it only approaches the Pod's
age when the GatewayRouter existed and the peer was reachable from startup. A much
lower value is equally consistent with a GatewayRouter created later, a peer that took
time to come up, or a session that reset. What is diagnostic is the value **dropping**
between scrapes, which means the session restarted in between.

What the outcomes tell you:

- **401 or 403** means the auth chain is wrong, not the metrics. Prometheus would fail
  identically.
- **Connection refused** means `--metrics-bind-address` never took effect, which check
  (a) would have caught.
- **Metrics served here but the Prometheus target DOWN** points at the PodMonitor, its
  adoption label, or `insecureSkipVerify`. Nothing about the router.

## 4. Apply a PodMonitor

Follow [podmonitor-https.md](podmonitor-https.md) step 3. The selector uses the
`managed-by` label, so **one PodMonitor covers all Gateways** in the namespace; the
per-Gateway distinction comes from the `gateway` metric label.

```yaml
spec:
  selector:
    matchLabels:
      app.kubernetes.io/managed-by: gateway-controller.meridio-2.nordix.org
  podMetricsEndpoints:
  - port: metrics
    interval: 5s           # short interval makes uptime changes easy to watch
    scheme: https
    # …authorization and tlsConfig per the recipe…
```

To scope to one Gateway, add `gateway.networking.k8s.io/gateway-name: <gw>` to
`matchLabels`.

Then verify with [shared-setup.md](shared-setup.md#verify-a-scrape). Set `POOL` to the
PodMonitor's name: the Operator builds the full pool as
`podMonitor/<namespace>/<name>/<endpoint-index>`, and the shared command matches it as
a substring.

## Failures

The shared [common failures](podmonitor-https.md#common-failures) table covers auth,
TLS and adoption. Router-specific:

| Symptom | Cause |
| --- | --- |
| `collect_success` = 0 | `birdc` could not be reached. Exec into the router container and run `birdc -s /var/run/bird/bird.ctl show protocols all "NBR-*"`. The failure itself logs at V(1), so raise the router's log level to debug to see it |
| `collect_success` = 1 but `bgp_session_uptime_seconds` missing while `bgp_session_up` = 1 | The protocol's `Since` field did not parse as an integer, meaning the generated `bird.conf` lacks `timeformat protocol "%s"`. Check `/etc/bird/bird.conf` in the container and the `Since` column in `birdc show protocols` |
| No `bgp_*` series at all | Expected if every GatewayRouter uses Static routing. Confirm with `kubectl get gatewayrouters -o wide` |
| Uptime identical across replicas, or differing by a couple of seconds | Normal. Each replica holds its own BGP session, and the small offset is the staggered scrape schedule |
| Uptime much lower than the Pod's age | Not an error by itself. The session may have been established late (GatewayRouter created after startup, or peer initially unreachable) rather than reset. Only a drop between scrapes proves a restart |
| `router_route_config_errors_total` rising | Netlink rejected a rule or route op. The routing path is best-effort and the reconcile is requeued, so a counter that rose once and stopped was transient and self-healed. One that keeps climbing means a failure retried every reconcile: check the `reason` label, the router container's NET_ADMIN capability, and whether `--table-id`/`--rule-priority` collide with something else on the node |

## Why a failed read does not fail the scrape

A failed `birdc` call emits **no session series** for that source and sets its
`collect_success` to 0, rather than reporting every session as down. It deliberately
does not fail the whole scrape: Prometheus treats a failed scrape as an empty one and
stales **every** series on the endpoint, including the router's controller-runtime and
process metrics.
