# Metrics Testing Guide

How to exercise the controller-manager's custom Prometheus collectors locally,
without needing a full data-plane environment (Multus, real LB Pods, or a
metrics-scraping Prometheus).

The controller-manager exposes these custom metrics (all prefixed by
`--metrics-prefix`, default `meridio_2`):

| Metric | Labels | Meaning |
| --- | --- | --- |
| `<prefix>_gateway_count` | (none) | Number of Gateways with `Accepted=True` managed by this controller. |
| `<prefix>_gateway_programmed` | `gateway`, `namespace` | `Programmed` status condition (0/1) per managed Gateway. |
| `<prefix>_distributiongroup_ready` | `dg`, `namespace` | DistributionGroup `Ready` status condition (0/1). DG-wide, no Gateway dimension. |
| `<prefix>_distributiongroup_endpoints` | `gateway`, `gateway_namespace`, `dg`, `namespace` | Current endpoint count for the DG under a given Gateway. |
| `<prefix>_distributiongroup_max_endpoints` | `gateway`, `gateway_namespace`, `dg`, `namespace` | Upper bound on endpoint count per the DG's distribution strategy (Maglev capacity; `+Inf` for strategies without a bounded capacity). |

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
- `gateway_programmed` series appearing per Gateway. Note `Programmed` is set by
  the Gateway reconciler based on observing the LB Deployment, not on its Pods
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

- **Only `gateway_count 0`, DG series show `gateway=""`** — the Gateway is not
  `Accepted=True`. Check why:
  ```bash
  kubectl get gateway sllb-sample -n default -o jsonpath='{.status.conditions}' | jq .
  ```
  The `Accepted=False` message names the rejected field (most commonly a missing
  or invalid `GatewayConfiguration` reference).

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
