# Kind Cluster Setup for E2E Tests

## Create the Cluster

\```bash
kind create cluster --config test/e2e/kind-cluster/kind-basic.yaml
\```

This creates a cluster with:
- 1 control-plane + 4 worker nodes (needed for pod anti-affinity across LB replicas)
- `InPlacePodVerticalScaling` feature gate enabled
- IPVS kube-proxy mode
- Kubernetes v1.31.0

## Next Steps

Install cluster dependencies and deploy test suites:

\```bash
make -C test/e2e cluster
make -C test/e2e deploy-common-appnetwork
\```

## Note: inotify limits

Kind nodes share the host kernel. The default `fs.inotify.max_user_instances=128` is too low
for clusters running Multus, Whereabouts, cert-manager, and the controller-manager simultaneously.
Whereabouts pods will crash with `error creating configuration watcher: too many open files`.

This only needs to be done once per machine:

\```bash
cat <<SYSCTL | sudo tee /etc/sysctl.d/99-kind-inotify.conf
fs.inotify.max_user_instances = 1024
fs.inotify.max_user_watches = 524288
SYSCTL
sudo sysctl --system
\```
