# Testing

Meridio-2 has three test layers: unit, integration, and end-to-end (e2e). This
document lists what each covers, how to run them locally, and which run in the
GitHub CI and on what trigger.

## Test layers

| Layer | Where | What it covers | Needs |
|-------|-------|----------------|-------|
| **Unit** | `*_test.go` across the tree (excluding `test/e2e`) | Controller reconciliation, webhooks, and helper logic. Uses [envtest](https://book.kubebuilder.io/reference/envtest) (a real API server + etcd, no kubelet) and Ginkgo/Gomega. | Go toolchain; envtest binaries (downloaded by the make target). |
| **Integration** | `internal/**/‌*_integration_test.go` (build tag `//go:build integration`) | Data-plane plumbing that touches the kernel: nftables programming (`internal/nftables`) and NFQLB policy-route stats (`internal/nfqlb`). | `CAP_NET_ADMIN` (creates network namespaces, programs nftables). |
| **End-to-end** | `test/e2e/` | The whole system in [kind](https://kind.sigs.k8s.io/): controllers + load balancer + sidecar + a simulated external BGP peer (DCGW), driving real attracted traffic over secondary networks. Uses Ginkgo. | kind, Docker, the cluster dependencies (see below). |

## Running tests locally

### Unit tests

```bash
make test
```

Runs `go test -race` over all packages except `test/e2e`, with a coverage
profile written to `cover.out`. Envtest binaries for the pinned Kubernetes
version are downloaded automatically on first run.

### Integration tests

```bash
make test-integration
```

Runs the `//go:build integration` tests in `internal/nftables` and
`internal/nfqlb`. These create network namespaces and program nftables, so they
need `CAP_NET_ADMIN`. **Without that capability the netns tests SKIP rather than
fail**, so a plain local run is non-fatal. To require them to actually run (as
CI does), set `MERIDIO_REQUIRE_NETNS=1` and grant the capability, e.g.:

```bash
sudo --preserve-env=PATH,HOME,GOPATH,GOCACHE,GOMODCACHE \
  MERIDIO_REQUIRE_NETNS=1 "$(command -v make)" test-integration
```

### End-to-end tests

The e2e suites need a kind cluster with all dependencies (Multus, Whereabouts,
cert-manager, Gateway API CRDs) and the simulated DCGW. See
[docs/INSTALL.md](../INSTALL.md) for the full setup. In short:

```bash
kind create cluster --config test/e2e/kind-cluster/kind.yaml
make -C test/e2e cluster          # install dependencies + DCGW
make -C test/e2e bgp              # deploy a topology and run a suite against it
```

Each e2e suite has `deploy-<suite>`, `test-<suite>`, and `undeploy-<suite>`
targets; several also have a combined `<suite>` target that deploys and tests in
one step (e.g. `bgp`, `shared-appnetwork`). The suites, their topologies, and
the VLAN/BGP addressing conventions are documented in
[test/e2e/suites/README.md](../../test/e2e/suites/README.md).

The tests carry Ginkgo labels (`ipv4`, `dual-stack`, `ipv6`) so they can be run
as families. `make e2e` (root Makefile) runs the full IPv4 set and cleans up
afterwards.

### Everything at once

```bash
make check      # lint + fmt + vet + unit tests
```

`make check` is also what the pre-commit hook runs; install it with
`make install-hooks` (see [git-hooks.md](git-hooks.md)).

## GitHub CI

Three workflows live in `.github/workflows/`.

### `ci.yaml` — on every pull request, and on push to `master` / `release/**`

Static checks and the fast test layers, each as its own job:

- `generated-code` — `make generate` then `git diff --exit-code` (CRDs/DeepCopy
  must be up to date).
- `golangci-lint` — `make lint`.
- `go-fmt` — `make fmt` then `git diff --exit-code`.
- `go-vet` — `make vet`.
- `go-mod` — `go mod tidy` then `git diff --exit-code go.mod go.sum`.
- `unit-tests` — `make test` (after the checks above pass).
- `integration-tests` — `make test-integration` under `sudo` with
  `MERIDIO_REQUIRE_NETNS=1`, so a missing `CAP_NET_ADMIN` is a hard failure.
- `build` — builds each image (`controller-manager`,
  `stateless-load-balancer`, `router`, `network-sidecar`, `example-target`) with
  `BUILD_STEPS=build`.

The `e2e` job is gated on `unit-tests`, `integration-tests`, and `build`, and
**only runs on `push`** (`if: github.event_name == 'push'`) — not on pull
requests. It calls `e2e.yaml`.

### `e2e.yaml` — called by `ci.yaml` (on push) or via manual `workflow_dispatch`

Builds and pushes the images to the registry, then runs the e2e suites as a
matrix:

- **Suites:** `ipv4`, `dual-stack`, `ipv6`.
- **Kubernetes versions:** `v1.31.6` and `v1.32.2` (each suite runs on both).

IPv4 and dual-stack use `kind.yaml`; IPv6 uses `kind-ipv6.yaml`. Each matrix
cell sets up the cluster dependencies, pre-pulls the images into the kind nodes,
deploys the suite, runs `test-<suite>`, and uploads/publishes a JUnit report.

### `release.yaml` — after a successful `ci` run on a `release/**` branch

Triggered by the `ci` workflow completing successfully on a `release/**` branch
(`workflow_run`). It derives the tag from the branch name (`release/X` -> `X`),
generates release notes, pushes the tag, and creates the GitHub release.

## Summary: what runs on what trigger

| Trigger | Runs |
|---------|------|
| Pull request | Static checks + unit + integration + image build (no e2e) |
| Push to `master` / `release/**` | The above **plus** the full e2e matrix |
| Manual (`workflow_dispatch` on `e2e.yaml`) | The e2e matrix |
| Successful `ci` on `release/**` | Tag + GitHub release (`release.yaml`) |
