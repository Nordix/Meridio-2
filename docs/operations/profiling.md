# Runtime profiling with pprof

The controller-manager, router, stateless-load-balancer and network-sidecar binaries can expose a Go [`net/http/pprof`](https://pkg.go.dev/net/http/pprof) endpoint for collecting runtime profiles (CPU, heap, goroutine, block, mutex) when diagnosing memory growth, goroutine leaks, or CPU hotspots — without redeploying an instrumented build.

**Disabled by default.** Set `--pprof-bind-address` / `MERIDIO_PPROF_ADDR` to a loopback address (e.g., `127.0.0.1:6060`) to enable it. Empty (the default) disables the feature.

**Loopback-only security model.** pprof is unauthenticated and lets any caller dump process memory and trigger expensive profiles, so it is treated as sensitive and follows the same model as the dynamic log-level endpoint:

- The address **must** be a loopback literal (`127.0.0.1` or `[::1]`). A non-loopback or malformed value is logged and the endpoint is left disabled (fail-safe); it never prevents the process from starting.
- Access is via `kubectl port-forward` only — loopback binding plus port-forward is the access control, so only someone who can already reach into the Pod gets profiles.
- No RBAC changes are required (loopback needs none), and the endpoint is intentionally **not** exposed via a Service, Ingress, or in-cluster scraping (pprof has no built-in auth).

**Usage.** With a binary started using, for example, `--pprof-bind-address=127.0.0.1:6060`:

```bash
# Forward the loopback pprof port from the Pod to your machine
kubectl port-forward <pod> 6060:6060

# Then collect profiles with the standard Go tooling
go tool pprof http://127.0.0.1:6060/debug/pprof/heap
go tool pprof http://127.0.0.1:6060/debug/pprof/profile?seconds=30 # 30s CPU profile
curl http://127.0.0.1:6060/debug/pprof/goroutine?debug=2 # goroutine dump
```

For a Pod with multiple Meridio-2 containers (e.g., the LB Pod runs the stateless-load-balancer and router side by side), give each container a distinct pprof port to avoid conflicts.
