# Shared setup: Prometheus and Grafana

Common to every component chapter. Do this once per cluster.

## Install kube-prometheus-stack

Installs the Prometheus Operator, a Prometheus server, and Grafana into a
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
PodMonitors in any namespace regardless of labels. Without them the chart selects
only monitors carrying its own release label, and the monitors in the component
chapters are silently ignored. The alternative is to label each monitor
`release: prometheus`.

Nothing here depends on Meridio-2 being deployed first, and nothing in Meridio-2's
Kustomize base depends on the Prometheus Operator CRDs. The two can be installed in
either order.

## Verify a scrape

Port-forward the Prometheus server's UI/API port and leave it running:

```bash
kubectl -n monitoring port-forward svc/prometheus-kube-prometheus-prometheus 9090:9090
```

The commands below take two values from whichever component chapter you are
following. Assign them first so each command is safe to paste unchanged:

```bash
POOL=<scrape pool name from your component chapter>
METRIC=<a metric name your component exports>
```

Both are listed per component: see the chapter you are following, or
[README.md](README.md#metric-index) for the full metric index. Nothing below is
specific to one component.

### Targets

Browse `http://localhost:9090/targets`, or query the API. A default stack installs
several hundred targets (node-exporter, kube-state-metrics, cAdvisor), so filter by
scrape pool rather than reading the whole list:

```bash
curl -s 'http://localhost:9090/api/v1/targets?state=active' \
 | jq -r --arg pool "$POOL" '.data.activeTargets[]
     | select(.scrapePool | test($pool))
     | "\(.health)\t\(.scrapeUrl)\t\(.labels.pod // .labels.instance)\terr=\(.lastError // "none")"'
```

Expect `up` and an empty error for each target. Discovery can lag a few tens of
seconds after applying a monitor, because the Operator must regenerate the Prometheus
config and Prometheus must reload it. A missing target immediately after applying is
usually just that lag.

An empty result means discovery matched nothing, which is different from a failing
scrape: a monitor that selects no Pods produces no target at all rather than a DOWN
one. Use `state=any` and look at `droppedTargets` to distinguish them.

### Query

```bash
curl -s "http://localhost:9090/api/v1/query?query=$METRIC" | jq .
```

To see just the label sets:

```bash
curl -s "http://localhost:9090/api/v1/query?query=$METRIC" | jq '.data.result[].metric'
```

Two results that look like failures but are not:

- `parse error: unexpected <op:<>` means a literal `<placeholder>` reached the query,
  so `METRIC` was never assigned.
- An empty `result` array with `"status":"success"` means the query was valid but
  nothing matched. Usually the metric belongs to a component you have not deployed or
  not yet enabled, or its target is not UP. Check targets before the query.

> The port-forward dies when its terminal closes or the Prometheus Pod restarts. An
> empty query result together with a failed `curl` to `:9090` usually means the
> forward dropped, so restart it before concluding the scrape broke.

## Grafana

```bash
kubectl -n monitoring port-forward svc/prometheus-grafana 3000:80
```

Browse `http://localhost:3000`. Default credentials from the chart are `admin` /
`prom-operator`, or read them from the Secret:

```bash
kubectl -n monitoring get secret prometheus-grafana \
  -o jsonpath='{.data.admin-password}' | base64 -d; echo
```

The stack preconfigures Prometheus as a data source, so Explore works immediately.
Use it to confirm a metric exists and carries the labels you expect before building
panels on it.
