# Scraping the network-sidecar

Prerequisites: [shared-setup.md](shared-setup.md) and
[podmonitor-https.md](podmonitor-https.md).

The network-sidecar runs as a container in **every application Pod**, so each sidecar
is its own per-Pod metrics source with no metrics Service. It is scraped via a
`PodMonitor` following the shared per-Pod recipe.

Its metrics endpoint is **off by default** (`--metrics-bind-address` defaults to `0`).

Throughout, `NS` is the namespace the application Pods and their sidecar SA run in.

## Values for the shared commands

| Shared variable | Value |
| --- | --- |
| `POOL` | the PodMonitor's name from step 2 (matched as a substring of `podMonitor/<ns>/<name>/<index>`) |
| `METRIC` | any from the table below, e.g. `meridio_2_sidecar_vips_configured` |
| Recipe `COMPONENT` | `network-sidecar` |
| Recipe `SA` | the sidecar container's ServiceAccount, i.e. the application Pod's `spec.serviceAccountName` |

## Metrics

Pod identity is 1:1 with the scrape target, so these carry **no `pod` label**; the
scraper injects `pod` and `instance`.

| Metric | Labels | Source |
| --- | --- | --- |
| `sidecar_vips_configured` | `gateway` | gauge: VIPs in the ENC spec, summed across the gateway's domains |
| `sidecar_nexthops` | `gateway`, `ip_family` | gauge: next hops in the ENC spec, per gateway and family |
| `sidecar_config_errors_total` | `reason` | push counter of failed netlink ops; `reason` ∈ `link` / `address` / `route`, pre-initialized to 0 |

The two gauges read the Pod's EndpointNetworkConfiguration when scraped, so they
report **desired** configuration. The counter is the one that reflects whether
applying it succeeded.

## 1. Enable the endpoint

Add `--metrics-bind-address=:8443` to the `network-sidecar run` args and declare a
`metrics` container port. Secure serving is the binary default and the cert is
self-signed, so nothing else is needed in the Pod spec.

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

## 2. Apply the RBAC and PodMonitor

Follow [podmonitor-https.md](podmonitor-https.md) steps 1 to 3, substituting
`COMPONENT` → `network-sidecar` and `NS` → the application Pods' namespace.

For `SA`, use the ServiceAccount the application Pods run as, since the sidecar
shares it. Read it off a running Pod rather than hardcoding it:

```bash
NS=<application namespace>
POD=$(kubectl -n $NS get pod -l app=<application-pod-label> -o name | head -1)
kubectl -n $NS get $POD -o jsonpath='{.spec.serviceAccountName}{"\n"}'
```

For the PodMonitor's selector, use the label that identifies your application Pods.
Unlike the router's LB Pods, these are not created by a Meridio-2 controller, so there
is no controller-stamped label to rely on:

```yaml
spec:
  selector:
    matchLabels:
      app: <application-pod-label>
```

## 3. Verify

Follow [shared-setup.md](shared-setup.md#verify-a-scrape) with the `POOL` and `METRIC`
values above.

An illustrative example, not values to expect: an application Deployment of 2 replicas
attached to two gateways, one set per Pod, distinguished by the injected
`pod` / `instance` labels.

```
meridio_2_sidecar_vips_configured{gateway="gw-a1"} 1
meridio_2_sidecar_vips_configured{gateway="gw-a2"} 1
meridio_2_sidecar_nexthops{gateway="gw-a1",ip_family="IPv4"} 2
meridio_2_sidecar_config_errors_total{reason="address"} 0
meridio_2_sidecar_config_errors_total{reason="link"} 0
meridio_2_sidecar_config_errors_total{reason="route"} 0
```

The gauge values depend entirely on your topology. What generalizes is the shape:
one `vips_configured` series per gateway the Pod has joined, with the VIP count
**summed across that gateway's domains**, so a dual-stack gateway still yields a
single series. `nexthops` is broken out per gateway and IP family instead, so a
dual-stack gateway yields both `IPv4` and `IPv6`, including a `0` for a family that
is configured with no next hops.

Both gauges count entries in the Pod's EndpointNetworkConfiguration spec, so compare
them against `kubectl -n $NS get enc <pod-name> -o yaml` rather than against the
numbers above.

The three `config_errors_total` series are the one part that is not
topology-dependent: all three `reason` values are present at 0 before any netlink op
fails, because they are pre-initialized. They are registered under the same condition
as the gauges, so either all of these metrics are served or none are.

## Failures

The shared [common failures](podmonitor-https.md#common-failures) table covers auth,
TLS and adoption. Sidecar-specific:

| Symptom | Cause |
| --- | --- |
| HTTP 500 and "Authentication failed … tokenreviews … forbidden … at the cluster scope" | The sidecar's SA lacks the step 1 grant. Check with `kubectl auth can-i create tokenreviews --as=system:serviceaccount:$NS:$SA`, taking `SA` from the Pod as in step 2 |
| Target UP but no `sidecar_vips_configured` or `sidecar_nexthops` series | No EndpointNetworkConfiguration exists for this Pod yet, so the collector emits nothing rather than zeros. Check with `kubectl -n $NS get enc <pod-name>`. The `config_errors_total` series are still present |
| Gauge values disagree with what is configured on the interface | The gauges count entries in the ENC **spec**, so they report desired configuration rather than what netlink actually applied. A mismatch between the two shows up as a rising `config_errors_total`, not as a lower gauge |

## Consuming the error counter across restarts

`sidecar_config_errors_total` is a counter in the sidecar's process memory, so it
resets to 0 when the Pod or process restarts. Consume it with `rate()` or
`increase()`, which are reset-aware.

For restart-aware interpretation, pair it with **`process_start_time_seconds`**, which
controller-runtime's process collector exposes for free on the same endpoint. Prefer
that over container-restart metrics: it reflects controller-process restarts even when
a supervisor keeps the container alive.
