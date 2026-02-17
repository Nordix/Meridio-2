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

* [Getting Started]()
* [System Architecture]()
* [Deployment]()
* [Troubleshooting]()
* [Contributing]()

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
