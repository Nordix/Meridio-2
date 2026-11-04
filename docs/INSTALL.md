# Installing Meridio-2

This guide gets you from a clean machine to a running Meridio-2 with live,
load-balanced traffic. It is written for two readers:

- a **new user** who wants to see what the project actually does, and
- a **new contributor** who needs a local cluster to reproduce and fix a bug.

For what Meridio-2 is and the resource model it uses, see the
[README](../README.md).

## The short version

Meridio-2 attracts *external* traffic over BGP onto a *secondary* network. A
realistic demo therefore needs more than the controllers: it needs a secondary
network (Multus), an IPAM (Whereabouts), and an external BGP peer to advertise
the VIPs to. The project's end-to-end (e2e) test harness already wires all of
this up inside [kind](https://kind.sigs.k8s.io/) -- including a containerised
"data center gateway" (DCGW) that peers BGP with the load balancers. **Reusing
that harness is by far the fastest way to a working Meridio-2.**

```bash
# 1. One-time host tuning (see "inotify limits" below).

# 2. Create the kind cluster.
kind create cluster --config test/e2e/kind-cluster/kind.yaml

# 3. Install cluster dependencies (Multus, CNI plugins, Whereabouts,
#    cert-manager, Gateway API CRDs, the kind breakout network, and the
#    simulated DCGW/BGP peer).
make -C test/e2e cluster

# 4. Deploy a self-contained example topology and run a test against it.
make -C test/e2e bgp
```

The `bgp` target deploys the `ipv4-simple` topology and runs the BGP VIP
advertisement test. On success you have a working Meridio-2: a `Gateway`, a load
balancer advertising a VIP (`40.0.0.1`) over BGP to the simulated DCGW, and a
target Deployment receiving the attracted traffic. The test exercises the real
data path -- it withdraws the VIP when the target pods go NotReady and
re-advertises it (with ECMP next-hops) when they are Ready again.

To stand up the topology *without* running a test (for poking at it by hand):

```bash
make -C test/e2e deploy-ipv4-simple
```

## Prerequisites (host)

| Tool | Notes |
|------|-------|
| [Docker](https://docs.docker.com/engine/install/) | kind runs nodes as containers; the DCGW runs as a container |
| [kind](https://kind.sigs.k8s.io/docs/user/quick-start/#installation) | local Kubernetes in Docker |
| [kubectl](https://kubernetes.io/docs/tasks/tools/) | matching Kubernetes 1.31 |
| `make`, `go` | to drive the harness and build the DCGW image |

The in-cluster dependencies (Multus v4.1.2, CNI plugins, Whereabouts v0.8.0,
cert-manager v1.16.2, Gateway API v1.5.0 CRDs) are installed for you by
`make -C test/e2e cluster`; you do not install them by hand.

### inotify limits (one-time, per machine)

kind nodes share the host kernel. The default
`fs.inotify.max_user_instances=128` is too low for a cluster running Multus,
Whereabouts, cert-manager, and the controller-manager at once; Whereabouts pods
crash with `too many open files`. Raise it once:

```bash
cat <<'SYSCTL' | sudo tee /etc/sysctl.d/99-kind-inotify.conf
fs.inotify.max_user_instances = 1024
fs.inotify.max_user_watches = 524288
SYSCTL
sudo sysctl --system
```

## What the kind cluster looks like

`test/e2e/kind-cluster/kind.yaml` creates a **dual-stack**, 1 control-plane +
4 worker cluster on **Kubernetes v1.31.0** with IPVS kube-proxy and the
`InPlacePodVerticalScaling` feature gate. Four workers are needed so load
balancer replicas can spread across nodes via pod anti-affinity. A single
dual-stack cluster can host every test suite (IPv4, IPv6, dual-stack), because
the attracted traffic flows over secondary Multus interfaces independent of the
cluster's primary CNI.

The default kind cluster name is `kind`. If you want to keep it separate from
other clusters, override `CLUSTER_NAME` consistently on the `kind` and `make`
commands.

## Exploring the example

The `ipv4-simple` suite (`test/e2e/suites/ipv4-simple/`) is the smallest
end-to-end example and a good thing to read first. It contains exactly the
resources described in the README's resource model:

| File | Resource(s) |
|------|-------------|
| `nad.yaml` | Multus `NetworkAttachmentDefinition`s (sysctl tuning, VLAN, app network) |
| `gateway.yaml` | `Gateway` + `GatewayConfiguration` |
| `routing.yaml` | `GatewayRouter` (BGP peer config) + `L34Route` |
| `dg.yaml` | `DistributionGroup` |
| `targets.yaml` | the target Deployment (app Pod + network sidecar) |

Useful commands once it is deployed (namespace `e2e-ipv4-simple`):

```bash
# Gateway should be PROGRAMMED=True with the VIP as its ADDRESS.
kubectl -n e2e-ipv4-simple get gateway,gatewayconfiguration,l34route,distributiongroup

kubectl -n e2e-ipv4-simple get pods          # controller, LB (sllbr-gw-m1), targets

# One EndpointNetworkConfiguration per backend Pod (named after the Pod).
kubectl -n e2e-ipv4-simple get endpointnetworkconfiguration
```

Tear the topology down with:

```bash
make -C test/e2e undeploy-ipv4-simple
```

The full set of topologies and their addressing/BGP conventions is documented in
[test/e2e/suites/README.md](../test/e2e/suites/README.md).

## For contributors: iterating on the controllers

To test a local code change, build the images, load them into kind, and
redeploy the suite. The e2e harness pulls published images by default; point
`REGISTRY`/`VERSION` at your own images when iterating locally.

```bash
make generate manifests        # regenerate CRDs/DeepCopy if you touched types or markers
make docker-build              # build controller-manager + data-plane images
# load the built images into the kind nodes (e.g. `kind load docker-image`), then:
make -C test/e2e deploy-ipv4-simple
```

See [AGENTS.md](../AGENTS.md) and [docs/development/](development/) for the
build/test workflow (`make check`, `make test`, git hooks, webhook testing).

> **Control-plane-only install.** `make deploy` (root Makefile) installs just
> the CRDs, webhooks, and controller-manager against your current kubeconfig
> context. This is enough to develop and unit-test the controllers, but it does
> **not** give you a working data plane on its own: there is no secondary
> network, no IPAM, and no BGP peer to advertise VIPs to. For end-to-end traffic
> use the e2e harness above.

## Troubleshooting

- **Whereabouts pods crashlooping with `too many open files`** -- raise the
  inotify limits (see above).
- **LB Pod not becoming Ready / no BGP session** -- confirm `make -C test/e2e
  cluster` finished, including the `vpn-gateway` container and the
  `kind-breakout` docker network; the load balancer's readiness gate depends on
  an established BGP session with the simulated DCGW.
- More in [docs/operations/troubleshooting.md](operations/troubleshooting.md).
