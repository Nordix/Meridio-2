# Meridio-2

Meridio-2 attracts external, non-HTTP traffic into a Kubernetes cluster and
load-balances it across a group of application Pods — while giving you control
over *which* traffic is attracted (by destination IP, port, protocol, and even
raw L4 header bytes) and *how* it is distributed.

It is aimed at workloads that sit at the edge of a cluster and terminate traffic
that ordinary Kubernetes ingress is not designed for: arbitrary TCP, UDP, and
SCTP flows addressed to externally-routable virtual IPs (VIPs), rather than
HTTP(S) routed through an Ingress controller or a cloud `LoadBalancer` Service.

Meridio-2 advertises the VIPs to the external network over BGP, pulls the
matching packets in, and spreads them across healthy application Pods using
[Maglev](https://research.google/pubs/pub44824/) consistent hashing so that
existing connections stay pinned to the same backend as the backend set changes.

> **Historical note.** Meridio-2 is the successor to
> [Nordix/Meridio](https://github.com/Nordix/Meridio). It keeps the same problem
> domain — secondary-network external traffic attraction and distribution — but
> is a ground-up reimplementation built on the Kubernetes Gateway API. Prior
> familiarity with Meridio is **not** required to use Meridio-2.

## What problem does it solve?

Standard Kubernetes north-south traffic handling assumes HTTP-like, Service- and
Ingress-shaped traffic on the primary Pod network. A class of workloads does not
fit that model:

- They terminate **raw L3/L4 traffic** (TCP/UDP/SCTP), not HTTP.
- They need traffic delivered on a **secondary network interface** (e.g. a Multus
  MACVLAN/VLAN attachment), separate from `eth0`, often for isolation or for
  participating in a provider/telco data plane.
- They need **externally-routable VIPs advertised by the cluster via BGP**, and
  stable, connection-preserving distribution across replicas.

Meridio-2 provides exactly this: declare the VIPs and the traffic filter, label
the Pods that should receive the traffic, and Meridio-2 builds and operates the
per-Gateway stateless load balancers that attract and distribute it.

## How it uses the Kubernetes Gateway API

Meridio-2 is implemented as a [Gateway API](https://gateway-api.sigs.k8s.io/)
controller. It follows the standard Gateway API control-plane conventions:

- A **`GatewayClass`** selects Meridio-2 via its
  `spec.controllerName` (`meridio-2.nordix.org/gateway-controller`).
- A **`Gateway`** of that class represents one logical L3/L4 load balancer.
  Accepting a `Gateway` causes Meridio-2 to create and manage a dedicated load
  balancer `Deployment` for it, reported back through the usual
  `Accepted`/`Programmed` status conditions.
- Implementation-specific tuning is supplied the standard way, via
  `Gateway.spec.infrastructure.parametersRef`, pointing at a Meridio-2
  `GatewayConfiguration`.

Where it **departs from typical Gateway API usage** — all intentional, because
the Gateway API's built-in route types target HTTP/TLS/TCP on the primary
network:

- **Custom L3/L4 route kind.** Instead of `HTTPRoute`/`TCPRoute`, traffic is
  selected with a Meridio-2 **`L34Route`**. It is modeled on the Gateway API
  route structure (it has `parentRefs` to a `Gateway` and `backendRefs` to a
  backend) but matches on `destinationCIDRs` (the VIPs, each a `/32` or `/128`),
  plus optional `sourceCIDRs`, `sourcePorts`/`destinationPorts`, `protocols`
  (TCP/UDP/SCTP), and even `byteMatches` against raw L4 header bytes. There is no
  L7 (HTTP) matching.
- **Custom backend kind.** A route's `backendRef` is not a Kubernetes `Service`
  but a Meridio-2 **`DistributionGroup`** — a group of Pods selected by label,
  with a distribution strategy (Maglev). This mirrors the Service/kube-proxy
  split: `DistributionGroup` is the abstract load balancer, and Meridio-2's
  per-Gateway LB is the data-plane agent, analogous to kube-proxy.
- **Stricter route attachment.** An `L34Route` requires exactly one `parentRef`
  and one `backendRef`; a VIP therefore belongs to exactly one `Gateway`. This is
  stricter than upstream, which allows parentless routes and surfaces the
  unattached state via status.
- **Secondary-network data plane.** The attracted traffic is delivered on a
  secondary (Multus) network, not the primary Pod network, and VIP reachability
  is established with BGP rather than cluster `Service` VIPs.
- **Routing-oriented listeners.** Because matching is driven by the `L34Route`,
  the `Gateway` listener is used as a routing anchor (e.g. `protocol: ALL`)
  rather than to bind a specific L7 port.

See the [controller documentation](#documentation) for the full resource model
(`Gateway`, `GatewayConfiguration`, `L34Route`, `DistributionGroup`,
`GatewayRouter`, `EndpointNetworkConfiguration`).

## What an application must do to receive traffic

An application does not call any Meridio-2 API. It opts in by meeting a small
networking contract; Meridio-2 and its per-Pod sidecar handle the rest.

1. **Attach to the internal secondary network.** The application Pod must have a
   secondary interface (via a Multus `NetworkAttachmentDefinition`) on the same
   L2 segment the Gateway's load balancer uses to reach endpoints — the subnet
   declared in `GatewayConfiguration.spec.internalSubnets`. The load balancer
   forwards to the Pod's secondary-network IP as an ECMP next hop.
2. **Carry the selector label.** The Pod must match the label selector of a
   `DistributionGroup` that is bound (directly or via an `L34Route`) to the
   Gateway. That is how a Pod becomes a backend for a set of VIPs.
3. **Run the Meridio-2 network sidecar.** The sidecar container, injected into
   the application Pod, watches a per-Pod `EndpointNetworkConfiguration` resource
   that Meridio-2 produces, and programs the Pod's network namespace to satisfy
   the return-path contract:
   - it **assigns the VIPs locally** on the correct secondary interface so the
     application can accept and source traffic for them, and
   - it installs **source-based (policy) routing** so that replies sent *from* a
     VIP are steered back through the owning Gateway's load balancers, avoiding
     asymmetric routing out of `eth0`.

   The application itself just binds/listens on the VIPs as if they were local
   addresses; it needs no Gateway awareness.

In short: the app needs a secondary network attachment, the right label, and the
sidecar. Everything else — VIP attraction, BGP advertisement, backend selection,
and return-path routing — is managed for it.

## Constraints on the kind of traffic / application

Meridio-2 is an **L3/L4** system. The following constraints shape what workloads
it fits:

- **Protocols:** TCP, UDP, and SCTP only. No HTTP/L7 awareness, no TLS
  termination, no content-based routing.
- **Addressing:** traffic is matched to **VIPs** (`/32` or `/128` host
  addresses) carried in `L34Route.spec.destinationCIDRs`. Each VIP belongs to a
  single Gateway and cannot be shared across Gateways.
- **Secondary network, flat L2.** Traffic is delivered on a secondary interface,
  and the load balancer and the application Pods must share the same L2 broadcast
  domain on that internal network (the LB routes to Pod IPs via a directly-reachable
  next hop). Routed (L3) separation between the LB and the application Pods is not
  supported.
- **Stateless, per-packet distribution.** Distribution is Maglev consistent
  hashing over the healthy backend set; it preserves existing flows across
  backend changes but does not do application-level session affinity beyond the
  hash.
- **Externally advertised via BGP.** VIPs are announced to an upstream router as
  individual host routes; the practical ceiling on total VIPs per Gateway is the
  upstream BGP peer's max-prefix limit. Per-`L34Route` there are also data-plane
  limits (at most 32 CIDRs per field, and a bounded port-list size). See
  [Constraints and limitations](docs/operations/constraints-and-limitations.md).
- **Dual-stack** is supported: a Gateway may carry one IPv4 and one IPv6 internal
  subnet, and a backend Pod keeps a consistent identity across both families.

### Prerequisites
- Kubernetes 1.31+ (CEL CIDR/IP validation used in CRD rules requires 1.31+)
- cert-manager (for webhook certificates)
- Multus CNI for secondary networking
- CNI plugins (MACVLAN, SBR, etc.)
- Whereabouts IPAM for IP allocation
  
## Documentation

### Controllers
* [Gateway](docs/controllers/gateway.md)
* [DistributionGroup](docs/controllers/distributiongroup.md)
* [LoadBalancer](docs/controllers/loadbalancer.md)
* [Router](docs/controllers/router.md)
* [Sidecar](docs/controllers/sidecar.md)
* [EndpointNetworkConfiguration](docs/controllers/endpointnetworkconfiguration.md)

### Operations
* [Constraints and limitations](docs/operations/constraints-and-limitations.md)
* [Troubleshooting](docs/operations/troubleshooting.md)
* [Scale tuning](docs/operations/scale-tuning.md)
* [Pod cache filtering](docs/operations/pod-cache-filtering.md)
* [Runtime profiling with pprof](docs/operations/profiling.md)
* [TCP-AO key rotation](docs/operations/tcp-ao-key-rotation.md)

### Development
* [Metrics](docs/development/metrics/README.md)
* [Webhook testing](docs/development/webhook-testing.md)
* [Kustomize patching](docs/development/kustomize-patching.md)
* [Git hooks](docs/development/git-hooks.md)

### Design and architecture
* [Endpoint producer/consumer architecture](docs/design/meridio2-endpoint-producer-consumer-architecture.md)
* [Maglev ID shared allocation](docs/design/maglev-id-shared-allocation.md)
* [NFQLB target recovery invariants](docs/design/nfqlb-target-recovery-invariants.md)
* [Router metrics source of truth](docs/design/router-metrics-source-of-truth.md)
* [ADR-001: DistributionGroup as primary resource](docs/architecture/adr-001-distributiongroup-primary-resource.md)
* [ADR-002: Rename NetworkSubnet to InternalSubnet](docs/architecture/adr-002-rename-networksubnet-to-internalsubnet.md)
* [ADR-003: Defer CRD status population](docs/architecture/adr-003-defer-crd-status-population.md)

# Quick start guide

## Installing cert-manager

The operator uses webhooks for validation, which require TLS certificates managed by cert-manager:

```bash
kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.16.2/cert-manager.yaml

# Wait for cert-manager to be ready
kubectl wait --for=condition=Available --timeout=300s -n cert-manager deployment/cert-manager
kubectl wait --for=condition=Available --timeout=300s -n cert-manager deployment/cert-manager-webhook
kubectl wait --for=condition=Available --timeout=300s -n cert-manager deployment/cert-manager-cainjector
```

## Install in kind
## Demo
