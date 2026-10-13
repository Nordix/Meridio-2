# Meridio-2 metrics

Two different activities, two different entry points:

| You want to | Go to |
| --- | --- |
| Exercise the controller-manager's collectors locally (`go run`, plain HTTP, `curl`, no data plane) | [controller-manager-local.md](controller-manager-local.md) |
| Scrape a **deployed** component with a real in-cluster Prometheus/Grafana | the chapters below |

Local testing is controller-manager-only by nature: it needs nothing but an API
server. The router needs BIRD and its control socket, and the network-sidecar needs
netlink, so neither runs meaningfully as a local process.

## In-cluster scraping

Start with the [shared setup](shared-setup.md) (install kube-prometheus-stack,
port-forward, verify a scrape, Grafana), then follow the chapter for your component:

| Component | Monitor | Chapter |
| --- | --- | --- |
| Controller-manager | `ServiceMonitor` | [controller-manager.md](controller-manager.md) |
| Network-sidecar | `PodMonitor` | [network-sidecar.md](network-sidecar.md) |
| Router | `PodMonitor` | [router.md](router.md) |

Note the two controller-manager files: `controller-manager.md` is the in-cluster
scrape covered here, while [controller-manager-local.md](controller-manager-local.md)
is the local `go run` workflow.

The two `PodMonitor` components share an auth and TLS pattern, factored out into
[podmonitor-https.md](podmonitor-https.md). Read it once; the component chapters
supply only their own values.

> The LB (`stateless-load-balancer`) container exposes no metrics yet. When it does,
> its collection model (nfqlb, nftables) may not fit the pattern above, so revisit
> this structure rather than assuming a fourth chapter drops in.

## Metric index

All names take the `--metrics-prefix` prefix, default `meridio_2`.

| Metric | Labels | Component |
| --- | --- | --- |
| `gateway_count` | (none) | controller-manager |
| `gateway_programmed` | `gateway`, `namespace` | controller-manager |
| `distributiongroup_available` | `dg`, `namespace` | controller-manager |
| `distributiongroup_endpoints` | `gateway`, `gateway_namespace`, `dg`, `namespace` | controller-manager |
| `distributiongroup_max_endpoints` | `gateway`, `gateway_namespace`, `dg`, `namespace` | controller-manager |
| `sidecar_vips_configured` | `gateway` | network-sidecar |
| `sidecar_nexthops` | `gateway`, `ip_family` | network-sidecar |
| `sidecar_config_errors_total` | `reason` | network-sidecar |
| `bgp_session_up` | `gateway`, `gateway_router` | router |
| `bgp_routes_imported` | `gateway`, `gateway_router` | router |
| `bgp_prefixes_announced` | `gateway`, `gateway_router` | router |
| `bgp_session_uptime_seconds` | `gateway`, `gateway_router` | router |
| `bfd_session_up` | `gateway`, `neighbor`, `interface` | router |
| `router_metrics_collect_success` | `gateway`, `source` | router |
| `router_route_config_errors_total` | `gateway`, `reason` | router |

`pod` and `instance` are injected by the scraper, so no per-Pod component
instruments them.

## Three ports, three roles

These flows touch three different HTTP endpoints. Keeping them straight avoids most
of the confusion:

| Port | Belongs to | Who talks to it | Used for |
| --- | --- | --- | --- |
| `8443` | a component's metrics endpoint | Prometheus (the scraper) | serving `/metrics` over HTTPS |
| `9090` | the Prometheus server's web UI / HTTP API | you, the operator | checking scrape targets and running queries |
| `3000` | the Grafana server's web UI | you, the operator | dashboards / Explore |

You never scrape `8443` by hand (Prometheus does), except for the deliberate
component-side check each chapter describes. You port-forward `9090` and `3000` to
look at the result.

`8443` is fixed only for the controller-manager, whose shipped metrics Service targets
it. For the per-Pod components it is the value you pass to `--metrics-bind-address`,
which defaults to `0` (endpoint disabled); the chapters use `:8443` for consistency.
What the PodMonitor actually matches on is the **port name** `metrics`, not the number.

## ServiceMonitor or PodMonitor?

Both CRDs make Prometheus scrape **every matching Pod individually**. The Operator
expands either into one target per Pod: a ServiceMonitor discovers Pods via a
Service's Endpoints, a PodMonitor selects Pods by label directly. So the choice is
**not** "ServiceMonitor for Deployments, PodMonitor for Pods". That shorthand is
misleading, since a replicated Deployment is scraped per-Pod under both. The real
deciding factors:

1. **Is there already a metrics Service?** If yes, a ServiceMonitor reuses it. If not,
   a PodMonitor selects Pods by label without inventing one.
2. **Do you need verified TLS?** A serving certificate's SAN must match what
   Prometheus connects to. A Service provides a **stable DNS name** to put in the SAN.
   Ephemeral per-Pod IPs cannot be pre-covered, forcing `insecureSkipVerify`
   (encrypted but unverified).

That is why the three components differ despite all being Deployments:

- **Controller-manager**: kubebuilder scaffolds a metrics Service and its
  cert-manager cert keys its SAN off that Service's DNS name. A Service exists *and*
  verifiable TLS is possible, so ServiceMonitor.
- **Network-sidecar**: runs in arbitrary application Pods, no metrics Service, no
  stable per-Pod SAN. PodMonitor with a self-signed cert.
- **Router**: runs in the LB Pods the gateway controller creates, likewise no metrics
  Service. PodMonitor, same cert mechanics as the sidecar.

> Note for repackagers: if you deploy the controller-manager **without** the
> kubebuilder Service (e.g. a custom Helm chart that omits it), the ServiceMonitor
> recipe no longer applies as-is. Either add an equivalent metrics Service, or scrape
> the manager Pods with a PodMonitor, accepting `insecureSkipVerify` since you would
> then lack the stable-SAN cert the Service enables. The choice follows the two
> factors above, not the workload type.

## Related

- [otel-migration.md](otel-migration.md): what an eventual OpenTelemetry migration
  would entail, and which current behavior does not port cleanly.
- [../../design/router-metrics-source-of-truth.md](../../design/router-metrics-source-of-truth.md):
  why the router's metrics read BIRD directly instead of deriving from CRD state.
