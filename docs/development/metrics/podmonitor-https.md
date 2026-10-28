# Per-Pod HTTPS scraping recipe

The shared pattern behind the [network-sidecar](network-sidecar.md) and
[router](router.md) chapters. Both run as a container in many Pods with no metrics
Service, so both are scraped per-Pod via a `PodMonitor` over a self-signed HTTPS
endpoint. Only the names, ports, selectors and metric lists differ.

Read this once, then follow the component chapter, which supplies the values marked
`COMPONENT` / `SA` / `NS` below.

## What the endpoint requires

Secure serving is the binary default (`--metrics-secure=true`), and
controller-runtime's metrics server **self-signs** a serving cert when none is
mounted. That means no cert plumbing into the Pod, but it does mean two RBAC grants
and an unverified TLS client.

The endpoint runs controller-runtime's `WithAuthenticationAndAuthorization` filter,
which checks every scrape twice:

| Check | Performed by | Needs |
| --- | --- | --- |
| Authentication (TokenReview) | the **component** process, under its own SA | `create` on `tokenreviews` + `subjectaccessreviews`, cluster-scoped |
| Authorization (`get /metrics`) | the **scraper's** identity | `get` on the `/metrics` nonResourceURL |

Both are cluster-scoped, so each needs a ClusterRole plus a binding. Missing the
first produces an HTTP 500 and a "forbidden … at the cluster scope" log line on the
component; missing the second produces a 403.

## 1. Component-side auth RBAC

Lets the component validate incoming scrape tokens. Substitute the component's name
and its ServiceAccount.

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: meridio-2-COMPONENT-metrics-auth
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
  name: meridio-2-COMPONENT-metrics-auth-NS
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: meridio-2-COMPONENT-metrics-auth
subjects:
- kind: ServiceAccount
  name: SA
  namespace: NS
```

The ClusterRole is apply-once and shared. The binding is per-namespace, name-suffixed
with the namespace so covering a second namespace adds a binding rather than
clobbering the first. Appending to an existing binding's `subjects` works equally
well; the ClusterRole is never duplicated.

## 2. Scraper identity

The scraper must authenticate with a bearer token and be authorized to read
`/metrics`. A `PodMonitor`'s `authorization.credentials` Secret resolves **only in the
monitored namespace**, and there is no namespace field on that ref, so Prometheus's
own SA token (living in `monitoring`) cannot be referenced. Mint a self-contained
scrape identity in the monitored namespace instead.

```yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: COMPONENT-metrics-scraper
  namespace: NS
---
# Long-lived token Secret for that SA. The PodMonitor reads its "token" key.
apiVersion: v1
kind: Secret
metadata:
  name: COMPONENT-metrics-scraper-token
  namespace: NS
  annotations:
    kubernetes.io/service-account.name: COMPONENT-metrics-scraper
type: kubernetes.io/service-account-token
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: meridio-2-COMPONENT-metrics-reader
rules:
- nonResourceURLs: ["/metrics"]
  verbs: ["get"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: meridio-2-COMPONENT-metrics-reader-NS
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: meridio-2-COMPONENT-metrics-reader
subjects:
- kind: ServiceAccount
  name: COMPONENT-metrics-scraper
  namespace: NS
```

## 3. PodMonitor

```yaml
apiVersion: monitoring.coreos.com/v1
kind: PodMonitor
metadata:
  name: meridio-2-COMPONENT
  namespace: NS
  # If you did NOT set podMonitorSelectorNilUsesHelmValues=false in the shared setup,
  # add: labels: {release: prometheus}
spec:
  selector:
    matchLabels: {}        # component chapter supplies the Pod selector
  podMetricsEndpoints:
  - port: metrics          # the container port named "metrics" (8443)
    scheme: https
    authorization:
      type: Bearer
      credentials:
        name: COMPONENT-metrics-scraper-token
        key: token
    tlsConfig:
      insecureSkipVerify: true
```

`port` matches by **name**, and discovery evaluates every container in each selected
Pod. Containers without a port of that name are dropped, so in a multi-container Pod
only the metrics-serving container becomes a target. The flip side is that if a second
container in the same Pod ever exposes a port also named `metrics`, this one
PodMonitor will scrape both.

## Why `insecureSkipVerify`

The scrape is encrypted and bearer-authenticated, but the serving cert is not
CA-verified. This is structurally required for per-Pod scraping rather than a
shortcut: the self-signed cert's SANs are `localhost` and `127.0.0.1`, which can never
match the ephemeral Pod IP that Prometheus connects to. Verified TLS would need
per-Pod-IP certs (a CSI cert driver) or a service mesh.

The same cert is why a manual check from *inside* the container works over
`https://127.0.0.1:8443` with only the self-signed CA in the way: on loopback the SAN
does match.

## Common failures

| Symptom | Cause |
| --- | --- |
| Target DOWN, HTTP 500, component logs "Authentication failed … tokenreviews … forbidden … at the cluster scope" | step 1 binding missing for the component's SA |
| Target DOWN, 403 | step 2 reader binding missing for the scraper SA |
| Target DOWN, x509 / certificate error | `insecureSkipVerify: true` not set, or `scheme` is not `https` |
| No target at all in `/targets` | PodMonitor not adopted: set `podMonitorSelectorNilUsesHelmValues=false` or label it `release: prometheus`. Confirm with `kubectl -n monitoring get prometheus -o jsonpath='{.items[0].spec.podMonitorSelector}'` |
| Fewer targets than expected | the endpoint is not enabled on every selected Pod, so those Pods have no `metrics` port and are dropped at discovery (see below) |

Two discovery behaviors worth knowing, because both look like a broken scrape but are
not:

- A PodMonitor matching nothing yields **no** target rather than a DOWN one. An empty
  `/targets` result means discovery did not match, not that the scrape failed.
- Discovery evaluates **every container** in each selected Pod and silently drops
  those without a port named `metrics`. In a multi-container Pod only the
  metrics-serving container becomes a target, which is normal and not an error.

Add `&state=any` to the targets API and inspect `droppedTargets` to see what was
discarded and why:

```bash
curl -s 'http://localhost:9090/api/v1/targets?state=any' \
 | jq -r '.data.droppedTargets[].discoveredLabels
     | select(.__meta_kubernetes_namespace == "NS")
     | "\(.__meta_kubernetes_pod_name)\t\(.__meta_kubernetes_pod_container_name)\tport=\(.__meta_kubernetes_pod_container_port_name // "none")"'
```
