# Scraping the controller-manager

> For exercising these collectors **locally** (`go run`, plain HTTP, no cluster
> deploy), see [controller-manager-local.md](controller-manager-local.md)
> instead. This chapter covers a deployed controller-manager scraped by an in-cluster
> Prometheus.

Prerequisite: [shared-setup.md](shared-setup.md).

The controller-manager is the one component scraped via a `ServiceMonitor`, because
`config/default` already renders a metrics Service and a cert-manager cert keyed to
that Service's DNS name. See
[README.md](README.md#servicemonitor-or-podmonitor) for why the other components
differ.

The ServiceMonitor is applied separately rather than included in `config/default`, so
the Kustomize base does not depend on the Prometheus Operator CRDs:
`kubectl apply -k config/default` must work on a cluster without the Operator.

## Values for the shared commands

| Shared variable | Value |
| --- | --- |
| `POOL` | the ServiceMonitor's name from step 2 (matched as a substring of `serviceMonitor/<ns>/<name>/<index>`) |
| `METRIC` | any from the table below, e.g. `meridio_2_gateway_count` |

## Metrics

All pull-based from the informer cache, prefixed by `--metrics-prefix` (default
`meridio_2`).

| Metric | Labels | Meaning |
| --- | --- | --- |
| `gateway_count` | (none) | Gateways with `Accepted=True` managed by this controller |
| `gateway_programmed` | `gateway`, `namespace` | `Programmed` condition (0/1) per Gateway destined for this controller by its GatewayClass, independent of `Accepted` |
| `distributiongroup_available` | `dg`, `namespace` | DG `Available` condition (0/1), DG-wide with no Gateway dimension |
| `distributiongroup_endpoints` | `gateway`, `gateway_namespace`, `dg`, `namespace` | current endpoint count for the DG under a given Gateway |
| `distributiongroup_max_endpoints` | `gateway`, `gateway_namespace`, `dg`, `namespace` | capacity per the DG's distribution strategy (`+Inf` for unbounded strategies) |

## 1. What the base deploy already provides

`make deploy` (i.e. `config/default`) renders everything on the serving side, so
there is no component-side step here:

- Service `meridio-2-controller-manager-metrics-service`, labels
  `control-plane: controller-manager` and `app.kubernetes.io/name: meridio-2`, port
  name `https`, `targetPort: 8443`. This is what the ServiceMonitor selects.
- Certificate `meridio-2-metrics-certs` producing secret `metrics-server-cert`,
  mounted at `--metrics-cert-path`.
- Auth via controller-runtime's `WithAuthenticationAndAuthorization` filter, so a
  scraper must present a bearer token allowed to GET `/metrics`.

A `namePrefix` / `namespace` / `nameSuffix` in the deploy renames the Service, for
example the e2e `separate-appnetwork-v4` suite deploys it as
`meridio-2-controller-manager-metrics-service-separate-appnet-v4`. Renaming does not
change the labels, so the selector below still matches.

## 2. Apply a ServiceMonitor

Apply it in the **same namespace** as the controller-manager. The
`selector.matchLabels` are the metrics Service's labels, matched as a subset, so extra
labels on the Service (such as an e2e-suite label) are fine.

```bash
NS=<controller-manager namespace>
kubectl apply -f - <<EOF
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: meridio-2-controller-manager-metrics-monitor
  namespace: $NS
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
        insecureSkipVerify: true
EOF
```

`bearerTokenFile` is Prometheus's own automounted ServiceAccount token. That SA must
be authorized to GET `/metrics`; see the failure table below.

## 3. Verify

Follow [shared-setup.md](shared-setup.md#verify-a-scrape) with the `POOL` and `METRIC`
values above. Expected series track the deployed topology. For a two-Gateway suite:
`gateway_count` = 2, `gateway_programmed` = 1 for each Gateway, and one
`distributiongroup_available` / `_endpoints` / `_max_endpoints` set per DG.

> **The `namespace` label collides.** The collectors emit a `namespace` label, and the
> scrape target injects one too (the target Pod's namespace). Prometheus keeps the
> target's as `namespace` and renames the collector's to **`exported_namespace`**, so
> both appear on the series. Filter the DG/Gateway namespace with
> `exported_namespace="..."`, since `namespace` reflects the scraped Pod. Set
> `honorLabels: true` on the endpoint if you want the collector's label to win the name
> instead.
>
> Only `namespace` is affected. `gateway_namespace` comes through unchanged, and
> metrics without a `namespace` label of their own, such as `gateway_count`, simply
> carry the injected one.

## Failures

| Symptom | Cause |
| --- | --- |
| Target missing from `/targets` | Usually discovery lag; wait 30 to 60s. If it never appears, confirm `selector.matchLabels` is a subset of the Service's labels and that the Service has endpoints (`kubectl -n $NS get endpoints -l control-plane=controller-manager` should show the manager Pod IP on `:8443`). Check the Operator regenerated its config: `kubectl -n monitoring logs deploy/prometheus-kube-prometheus-operator` |
| Target DOWN with **401** | Missing or invalid bearer token on the scrape. The manager validates it via TokenReview/SubjectAccessReview, and the base already grants the manager's own SA that ability (`config/rbac/metrics_auth_role.yaml` plus its binding), so this is normally not the RBAC side |
| Target DOWN with **403** | Prometheus's SA is not authorized to GET `/metrics`. The base ships a `metrics-reader` ClusterRole (`config/rbac/metrics_reader_role.yaml`) but **no binding for Prometheus's SA**, which lives in the Operator's namespace. kube-prometheus-stack's own ClusterRole usually already grants its SA `get /metrics` cluster-wide, which is why this often works unwired. A 403 means it does not, so bind `metrics-reader` (or equivalent) to the scraping SA yourself |
| Target DOWN with x509 / TLS error | `scheme: https` or `insecureSkipVerify: true` missing. The Service serves a cert-manager cert whose CA Prometheus does not trust by default |

## Why `insecureSkipVerify` here

Unlike the per-Pod components, a stable Service DNS name *could* support verified TLS.
It is still skipped because a ServiceMonitor's `tlsConfig` secret refs resolve only in
Prometheus's own namespace, with no namespace field available, while
`metrics-server-cert` lives in the controller-manager's namespace. Verified TLS would
mean copying or re-issuing that CA into the Prometheus namespace.

## Why `bearerTokenFile` rather than `authorization`

`bearerTokenFile` is deprecated on ServiceMonitor, though still functional, in favor
of `authorization`. It is kept because it reads Prometheus's automounted SA token
directly, which is the simplest working form. Switching would require minting a token
Secret in this namespace, as the PodMonitor components must since PodMonitor dropped
`bearerTokenFile` entirely, for no functional gain here.
