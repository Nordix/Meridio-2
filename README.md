# Meridio-2

Meridio 2 is an evolution of [Nordix/Meridio](https://github.com/Nordix/Meridio) designed to facilitate attraction and distribution of external traffic within Kubernetes.

### Features

### Prerequisites
- Kubernetes 1.28+
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
