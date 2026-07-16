# OpenShift CRC Validation Suite (Dual-Stack)

This suite validates Meridio-2 end-to-end on OpenShift (CRC single-node) with dual-stack (IPv4 + IPv6).

It is designed for **occasional manual validation** of OpenShift compatibility. Follow it top to
bottom on a fresh CRC instance, or use the Makefile targets for the deployment steps.

All commands are written to be run from the **project root** (`Meridio-2/`).

---

## Architecture

All components run inside the cluster using bridge CNI networks:

```
┌──────────────────────────────────────────────────────────────────────────┐
│  CRC Node (single)                                                       │
│                                                                          │
│  ┌───────────────────┐  bridge br-meridio (VLAN 100) ┌──────────────────┐│
│  │  VPN Gateway Pod  │◄──── BGP peering ──────────►  │  LB Pod (SLLBR)  ││
│  │  (BIRD, ctraffic) │   169.254.100.0/24            │  router + nfqlb  ││
│  │  169.254.100.150  │   fd00:cafe:100::/64          │  169.254.100.X   ││
│  └───────────────────┘                               └────────┬─────────┘│
│                                                               │          │
│                                                 bridge br-meridio-app    │
│                                                 169.111.100.0/24         │
│                                                 fd00:cafe:1100::/64      │
│                                                               │          │
│                                                 ┌─────────────┴────────┐ │
│                                                 │  Target Pods (x2)    │ │
│                                                 │  + network-sidecar   │ │
│                                                 │  169.111.100.X + VIP │ │
│                                                 └──────────────────────┘ │
│                                                                          │
└──────────────────────────────────────────────────────────────────────────┘
```

## Addressing (Dual-Stack)

| Role | IPv4 | IPv6 | Notes |
|------|------|------|-------|
| BGP peering (external) | 169.254.100.0/24 | fd00:cafe:100::/64 | VPN gateway at .150 / ::150 |
| App network (internal) | 169.111.100.0/24 | fd00:cafe:1100::/64 | LB → target traffic |
| VIP | 100.0.0.1/32 | fd00:cafe:1::1/128 | Advertised via BGP |
| LB local ASN | 64512 | 64512 | Same for both families |
| VPN gateway ASN | 4200000000 | 4200000000 | Same for both families |
| BGP port | 10179 | 10179 | Both sides |

**Critical**: External and internal subnets MUST differ. The kernel resolves BGP next-hops
by matching against locally-connected subnets. Shared subnets would pick the wrong interface.

---

## Prerequisites (one-time per CRC instance)

These steps configure the CRC node for Meridio-2's kernel and security requirements.
They persist across `crc stop`/`crc start` cycles but require a node reboot on first apply.

### 1. Start CRC and log in

```bash
# Recommended: set disk to 50GB before first 'crc start' to avoid disk pressure
crc config set disk-size 50

crc start
eval $(crc oc-env)
oc login -u kubeadmin -p $(cat ~/.crc/machines/crc/kubeadmin-password) https://api.crc.testing:6443
```

### 2. Load nfnetlink_queue kernel module

NFQLB requires the `nfnetlink_queue` module which is not auto-loaded on CoreOS/OpenShift.
Make it persistent across reboots:

```bash
ssh -o StrictHostKeyChecking=no -i ~/.crc/machines/crc/id_ed25519 -p 2222 core@127.0.0.1 \
  "echo nfnetlink_queue | sudo tee /etc/modules-load.d/meridio.conf && sudo modprobe nfnetlink_queue"
```

### 3. Allowlist unsafe sysctls via KubeletConfig

OpenShift's kubelet rejects Pod-level unsafe sysctls unless explicitly allowlisted.
This is a two-layer requirement:
1. The **SCC** allows the Pod to declare the sysctls (admission control) — handled by the deploy step.
2. The **KubeletConfig** allows the kubelet to actually apply them (runtime enforcement) — done here.

```bash
oc apply -f test/e2e/suites/openshift-crc/kubeletconfig.yaml
```

> **⚠️ This triggers a MachineConfig rollout which reboots the CRC node.**
>
> The most reliable recovery on CRC single-node is `crc stop && crc start`:
> ```bash
> # Wait ~2 min for the rollout to start, then:
> crc stop
> crc start
>
> # Re-login after restart:
> eval $(crc oc-env)
> oc login -u kubeadmin -p $(cat ~/.crc/machines/crc/kubeadmin-password) https://api.crc.testing:6443
> ```
>
> Verify the kubelet accepted the sysctl allowlist:
> ```bash
> ssh -o StrictHostKeyChecking=no -i ~/.crc/machines/crc/id_ed25519 -p 2222 core@127.0.0.1 \
>   "sudo cat /etc/kubernetes/kubelet.conf | grep -A12 allowedUnsafe"
> ```

---

## Deploy (Makefile — recommended)

Once the prerequisites are complete, the entire deployment and teardown can be done via Make:

```bash
# Trust the CRC registry CA and login (prompts for sudo)
make -C test/e2e crc-registry-login KUBECTL=oc

# Build images locally first (from project root)
make IMAGES="controller-manager stateless-load-balancer router network-sidecar example-target" BUILD_STEPS=build

# Push images to the local OpenShift registry
make -C test/e2e/ push-images-openshift-crc KUBECTL=oc

# Deploy the full topology
make -C test/e2e/ deploy-openshift-crc KUBECTL=oc

# Teardown
make -C test/e2e undeploy-openshift-crc KUBECTL=oc
```

The `openshift-crc` target handles: namespace creation, ImageStreams, image push (tag+push of
locally-built images + vpn-gateway build), cert-manager install, SCCs, RBAC, controller-manager
(via kustomize overlay with RBAC finalizer patches + LB template override), VPN gateway, NADs,
Gateway, routing, targets, and waits for all pods to become Ready.

---

## Deploy (manual steps)

If you prefer to run each step individually (e.g., for debugging):

### Step 1 — Install cert-manager

```bash
oc apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.16.2/cert-manager.yaml

oc wait --for=condition=Available --timeout=300s -n cert-manager \
  deployment/cert-manager \
  deployment/cert-manager-webhook \
  deployment/cert-manager-cainjector
```

> **Note**: Gateway API CRDs are pre-installed on OpenShift — no action needed.

### Step 2 — Registry login and image push

```bash
# Trust CRC registry CA (one-time per machine)
make -C test/e2e crc-registry-login KUBECTL=oc

# Build all component images locally
make IMAGES="controller-manager stateless-load-balancer router network-sidecar example-target" BUILD_STEPS=build

# Push to CRC internal registry (creates namespace + ImageStreams automatically)
make -C test/e2e push-images-openshift-crc KUBECTL=oc
```

### Step 3 — Deploy

```bash
make -C test/e2e deploy-openshift-crc KUBECTL=oc
```

This single command deploys everything: namespace (with PSA labels), SCCs, RBAC, SCC bindings,
NADs, VPN gateway (waits for Ready), cert-manager (idempotent), controller-manager (with
OpenShift patches), GatewayClass, Gateway, routing, targets, and waits for all pods Running.

### Step 4 — Validate

Wait ~30 seconds after deployment for BGP convergence and nfqlb flow programming, then:

```bash
NS=meridio-2

# All pods should be Running
oc get pods -n $NS

# Check BGP sessions — expect GW4_OCP_1 and GW6_OCP_1 both Established
oc exec vpn-gateway -n $NS -- birdc show protocols

# Check VIP routes learned via BGP
oc exec vpn-gateway -n $NS -- birdc show route

# Ping VIPs
oc exec vpn-gateway -n $NS -- ping -c 3 100.0.0.1
oc exec vpn-gateway -n $NS -- ping6 -c 3 fd00:cafe:1::1

# TCP load balancing — IPv4
oc exec vpn-gateway -n $NS -- ctraffic -address 100.0.0.1:5000 -nconn 100 -timeout 10s -stats all

# TCP load balancing — IPv6
oc exec vpn-gateway -n $NS -- ctraffic -address '[fd00:cafe:1::1]:5000' -nconn 100 -timeout 10s -stats all

# UDP load balancing — IPv4
oc exec vpn-gateway -n $NS -- ctraffic -udp -address 100.0.0.1:5001 -nconn 100 -timeout 10s -stats all

# UDP load balancing — IPv6
oc exec vpn-gateway -n $NS -- ctraffic -udp -address '[fd00:cafe:1::1]:5001' -nconn 100 -timeout 10s -stats all
```

Expected results:
- `birdc show protocols`: `GW4_OCP_1` and `GW6_OCP_1` both `Established`
- `birdc show route`: `100.0.0.1/32` and `fd00:cafe:1::1/128` learned via BGP
- `ctraffic`: 0 failed connections, traffic distributed across 2 target pods

### Step 5 — Teardown

```bash
make -C test/e2e undeploy-openshift-crc KUBECTL=oc
```

---

## Makefile Targets Reference

| Target | Description |
|--------|-------------|
| `crc-registry-login` | Trust CRC registry CA + docker login (needs sudo) |
| `push-images-openshift-crc` | Create namespace + ImageStreams, build vpn-gateway, tag+push all 6 images |
| `deploy-openshift-crc` | Full deployment (cert-manager, SCCs, controller-manager, VPN gateway, topology, wait for Ready) |
| `undeploy-openshift-crc` | Delete webhook config, SCCs, and namespace (removes everything) |
| `openshift-crc` | Umbrella: push-images + deploy |

All targets accept `KUBECTL=oc` and derive registry paths from:
- `OCP_REGISTRY_HOST` (default: `default-route-openshift-image-registry.apps-crc.testing`)
- `OCP_NAMESPACE` (default: `meridio-2`)

---

## Files

| File | Purpose |
|------|---------|
| `namespace.yaml` | Namespace with PSA=privileged labels |
| `kubeletconfig.yaml` | KubeletConfig to allowlist unsafe sysctls (triggers node reboot) |
| `scc.yaml` | Custom SCCs: `meridio-lb` (LB pods) and `meridio-sidecar` (target+sidecar pods) |
| `kustomization.yaml` | Deploys controller-manager with OpenShift patches (RBAC finalizers + LB template) |
| `nad.yaml` | NetworkAttachmentDefinitions (bridge CNI: bgp-net with VLAN 100, app-net) |
| `gateway.yaml` | Gateway + GatewayConfiguration |
| `routing.yaml` | GatewayRouter (IPv4 + IPv6, protocol: BGP) + L34Route |
| `dg.yaml` | DistributionGroup |
| `targets.yaml` | Target Pod Deployment (2 replicas + network-sidecar) |
| `rbac.yaml` | ServiceAccounts + Role + RoleBinding |
| `vpn-gateway.yaml` | VPN gateway ConfigMap (BIRD config, passive BGP) + Pod |
| `lb-deployment.yaml` | OpenShift-adapted LB template (reference; embedded in kustomization.yaml) |

---

## Key Differences from Kind-based Suites

| Aspect | Kind suites | This suite |
|--------|-------------|------------|
| VPN gateway | Docker container on host | Pod inside cluster |
| External network | VLAN subinterfaces on Docker bridge | Bridge CNI (`br-meridio`) with VLAN 100 |
| Internal network | macvlan on node eth0 | Bridge CNI (`br-meridio-app`) |
| Pod anti-affinity | Required (4 workers) | Removed (single node) |
| SCC | N/A (vanilla K8s) | Custom `meridio-lb` + `meridio-sidecar` SCCs |
| LB security context | runAsNonRoot, RuntimeDefault | sysctls in pod spec, `spc_t` on loadbalancer container only, Unconfined seccomp |
| Sysctl tuning | Via tuning CNI NAD | Pod `securityContext.sysctls` (requires KubeletConfig allowlist) |
| nfnetlink_queue | Auto-loaded | Persisted via `/etc/modules-load.d/meridio.conf` |
| Image registry | localhost:5001 (Kind) | Internal OpenShift registry |
| IPv6 convergence | Immediate | ~30s after deployment (nfqlb flow programming) |

---

## Known Issues / Notes

- **KubeletConfig reboot**: Applying `kubeletconfig.yaml` triggers a MachineConfig rollout that
  reboots the CRC node. The single-node kubelet does not reliably auto-restart after reboot.
  **Recommended recovery**: `crc stop && crc start` (not SSH kubelet nudge). One-time operation —
  the sysctl allowlist and module load persist across subsequent `crc stop`/`crc start` cycles.

- **Disk pressure**: Default CRC disk is 32GB which is too small for all Meridio-2 images.
  Set `crc config set disk-size 50` before creating the instance. If disk pressure occurs,
  prune images: `ssh ... core@127.0.0.1 "sudo crictl rmi --prune"` and remove the taint:
  `oc adm taint nodes crc node.kubernetes.io/disk-pressure:NoSchedule-`

- **IPv6 convergence**: After fresh deployment, IPv6 traffic may fail for ~30 seconds while
  nfqlb programs its flows and IPv6 NDP completes. Wait and retry — this is not a bug.

- **Ghost pods after CRC restart**: Force-delete with
  `oc delete pod <name> -n meridio-2 --force --grace-period=0`

- **cert-manager webhook timing**: If `deploy-openshift-crc` fails with webhook errors on the
  first run, wait 30s and re-run (cert-manager webhook takes time to inject CA certificates).

- **SELinux AVC denials**: If LB pods crash with `Operation not permitted` despite correct SCCs:
  ```bash
  ssh -o StrictHostKeyChecking=no -i ~/.crc/machines/crc/id_ed25519 -p 2222 core@127.0.0.1 \
    "sudo ausearch -m AVC -ts recent"
  ```
