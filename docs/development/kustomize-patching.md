# Kustomize Patching Strategy

## Current Approach: JSON Patch (RFC 6902)

The controller-manager deployment uses JSON patches (`op: add/replace`) to compose features modularly. Each patch file targets a specific concern and can be independently enabled or disabled in `kustomization.yaml`.

### Patch files

| Patch | Purpose |
|-------|---------|
| `manager_metrics_patch.yaml` | Metrics endpoint (`--metrics-bind-address=:8443`) |
| `manager_webhook_patch.yaml` | Webhook certs volume, mount, args, and port |
| `manager_templates_patch.yaml` | LB deployment template ConfigMap mount |
| `manager_env_patch.yaml` | Downward API env vars (namespace, service account) |
| `cert_metrics_manager_patch.yaml` | Metrics server TLS certs volume, mount, and args |

### Container index dependency

All patches reference the manager container by index: `/spec/template/spec/containers/0/...`

This assumes the `manager` container is at index 0 in the Deployment spec. The assumption holds because the controller-manager Deployment has a single container.

### Why JSON Patch works for modular args

JSON Patch (RFC 6902) operates on the raw JSON document structure — it has no awareness of Kubernetes types, merge keys, or OpenAPI schemas. The `add` operation with the `/-` suffix appends to an array:

```yaml
- op: add
  path: /spec/template/spec/containers/0/args/-
  value: --webhook-cert-path=/tmp/certs
```

Each patch independently appends one arg without knowing what other args exist. This is what enables the modular composition — contrast with strategic merge patch, which would replace the entire `args` list (see below).

### Brittleness

- Reordering containers or inserting a sidecar before `manager` would silently patch the wrong container
- No validation that index 0 is actually the `manager` container
- Adding init containers does NOT break it (separate array: `initContainers`)

## Alternative Considered: Strategic Merge Patch

Kustomize supports strategic merge patches that target containers by `name` instead of index:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: controller-manager
spec:
  template:
    spec:
      containers:
      - name: manager
        volumeMounts:
        - mountPath: /templates
          name: lb-templates
          readOnly: true
      volumes:
      - name: lb-templates
        configMap:
          name: stateless-load-balancer-templates
```

Kubernetes strategic merge uses `name` as the merge key for `containers`, `volumeMounts`, `volumes`, and `env` — so these fields merge correctly without indexes.

### Why it doesn't work for us

The `args` field has **replace** semantics in strategic merge, not append. This is not a Kustomize limitation — it is baked into the Kubernetes API type definitions. Kustomize reads the OpenAPI schema to determine merge behavior per field.

Fields that support merge have a `patchStrategy:"merge"` tag with a `patchMergeKey` in the Kubernetes Go types (`k8s.io/api/core/v1/types.go`):

```go
// Merges by name — has patchStrategy tag
Env []EnvVar `json:"env,omitempty" patchStrategy:"merge" patchMergeKey:"name"`

// Replaces entirely — plain []string, no patch tags
Args []string `json:"args,omitempty"`
```

Because `args` is a plain `[]string` with no unique identifier per element, there is no merge key and strategic merge defaults to replacing the entire field. This means:

- A strategic merge patch that sets `args: [--webhook-cert-path=...]` would **replace** the entire args list, wiping out all other args
- To add a single arg, every patch touching args would need to duplicate the full args list
- This breaks the modular composition where each patch independently adds its own args

Since multiple patches add individual args (`--metrics-bind-address`, `--webhook-cert-path`, `--metrics-cert-path`), strategic merge would force either:
1. Consolidating all arg-adding patches into one (losing modularity)
2. Duplicating the full args list in every patch (fragile, violates DRY)

### Fields that would work with strategic merge

| Field | Merge key | Strategic merge works? |
|-------|-----------|:---:|
| `containers` | `name` | ✓ |
| `volumes` | `name` | ✓ |
| `volumeMounts` | `mountPath` | ✓ |
| `env` | `name` | ✓ |
| `ports` | `containerPort` | ✓ |
| `args` | *(none — list replace)* | ✗ |

## Decision

Keep JSON patches. The index brittleness is theoretical (single-container Deployment unlikely to change), while the modularity benefit is practical and actively used for toggling features independently.

If the container layout ever changes, the index assumption should be revisited. A comment in the base `manager.yaml` documenting this assumption would be a low-cost safeguard.
