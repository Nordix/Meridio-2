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

## Deploy

Once the prerequisites are complete, the entire deployment and teardown can be done via Make:

```bash
# Trust the CRC registry CA and login (prompts for sudo)
make -C test/e2e crc-registry-login KUBECTL=oc

# Build images locally first (from project root)
make IMAGES="controller-manager stateless-load-balancer router network-sidecar example-target" BUILD_STEPS=build

# Deploy the full topology (depends on push-images-openshift-crc, so this also
# creates the namespace/ImageStreams and pushes all images)
make -C test/e2e/ deploy-openshift-crc KUBECTL=oc

# Teardown
make -C test/e2e undeploy-openshift-crc KUBECTL=oc
```

`deploy-openshift-crc` depends on `push-images-openshift-crc`, so a single call handles:
namespace creation, ImageStreams, image push (tag+push of locally-built images + vpn-gateway
build), cert-manager install (idempotent — skipped if already present), SCCs, RBAC,
controller-manager (via kustomize overlay with RBAC finalizer patches + LB template override),
VPN gateway, NADs, Gateway, routing, targets, and waits for all pods to become Ready.

### Alternative: pull images from registry.nordix.org

If you want to validate the OCP suite itself (network/RBAC/SCC/topology) against known-good
published images instead of your local working tree, set `OCP_USE_NORDIX=true`. This skips the
local build step; `deploy-openshift-crc` will only build/push `vpn-gateway` (which has no
published image):

```bash
# Still required: vpn-gateway has no published image and is always built/pushed locally
make -C test/e2e crc-registry-login KUBECTL=oc

# No local build step (`make IMAGES=... BUILD_STEPS=build`) needed for the other 5 images

# controller-manager, stateless-load-balancer, router, network-sidecar, example-target
# all resolve to registry.nordix.org/cloud-native/meridio-2/<name>:latest;
# only vpn-gateway is built and pushed to the CRC internal registry
make -C test/e2e deploy-openshift-crc KUBECTL=oc OCP_USE_NORDIX=true

# Teardown is unchanged
make -C test/e2e undeploy-openshift-crc KUBECTL=oc
```

**Use the default (build-and-push) flow when iterating on component code** — `OCP_USE_NORDIX=true`
deploys whatever is currently published on nordix, not your local changes.

### Validate

Wait at least 1-2 minutes after deployment completes before running validation commands, then:

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

> **Note**: Gateway API CRDs are pre-installed on OpenShift — no action needed.

### Automated test run

The manual validation steps above are also available as an automated Ginkgo suite:

```bash
# 1. Deploy the topology
make -C test/e2e deploy-openshift-crc KUBECTL=oc

# 2. Wait at least 1-2 minutes, then run the tests
make -C test/e2e test-openshift-crc KUBECTL=oc

# Teardown
make -C test/e2e undeploy-openshift-crc KUBECTL=oc
```

**Run `test-openshift-crc` as a separate step, with a gap after deployment finishes — do not
run it back-to-back with `deploy-openshift-crc` in the same invocation.** Waiting a minute or
two between deployment and testing has been observed to reduce intermittent IPv6 traffic
failures more consistently than retrying the test itself.

`test-openshift-crc` runs the `OpenShift CRC` Ginkgo suite (`--focus="OpenShift CRC"`), covering:
- Gateway Accepted/Programmed, status.addresses (dual-stack VIPs), LB Pods deployed
- DistributionGroup Ready, target Pods Running, ENC Ready, LB connectivity readiness gates
- ICMP reachability on both VIPs
- TCP and UDP load balancing across both target pods, for both IPv4 and IPv6

Unlike the Kind-based suites, the VPN gateway here is a Pod inside the cluster rather than a Docker
container on the host. `test-openshift-crc` accounts for this by setting `E2E_VPN_GATEWAY_EXEC` to
`$(KUBECTL) exec -n $(OCP_NAMESPACE) vpn-gateway --` so the shared traffic helpers
(`test/e2e/utils/traffic.go`) run against the Pod instead of `docker exec`.

`test-openshift-crc` is intentionally not part of the `ipv4`/`dual-stack` aggregate Makefile
targets (`test-ipv4`, `test-dual-stack`), since this suite requires a separate CRC cluster and
cannot run alongside the Kind-based suites in the same invocation.

---

## Makefile Targets Reference

| Target | Description |
|--------|-------------|
| `crc-registry-login` | Trust CRC registry CA + docker login (needs sudo) |
| `push-images-openshift-crc` | Create namespace + ImageStreams, build vpn-gateway, tag+push all 6 images |
| `deploy-openshift-crc` | Full deployment (cert-manager, SCCs, controller-manager, VPN gateway, topology, wait for Ready) |
| `test-openshift-crc` | Run the `OpenShift CRC` Ginkgo suite against an already-deployed topology |
| `undeploy-openshift-crc` | Delete webhook config, SCCs, and namespace (removes everything) |

All targets accept `KUBECTL=oc` and derive registry paths from:
- `OCP_REGISTRY_HOST` (default: `default-route-openshift-image-registry.apps-crc.testing`)
- `OCP_NAMESPACE` (default: `meridio-2`)
- `OCP_USE_NORDIX` (default: `false`) — if set to `true`, `controller-manager`,
  `stateless-load-balancer`, `router`, `network-sidecar`, and `example-target` are pulled
  directly from `registry.nordix.org` instead of being built and pushed to the CRC internal
  registry. `vpn-gateway` has no published image and is always built/pushed locally regardless
  of this flag:
  ```bash
  make -C test/e2e push-images-openshift-crc KUBECTL=oc OCP_USE_NORDIX=true
  make -C test/e2e deploy-openshift-crc KUBECTL=oc OCP_USE_NORDIX=true
  ```

---

## Files

| File | Purpose |
|------|---------|
| `namespace.yaml` | Namespace with PSA=privileged labels |
| `kubeletconfig.yaml` | KubeletConfig to allowlist unsafe sysctls (triggers node reboot) |
| `scc.yaml` | Custom SCCs: `meridio-lb` (LB pods) and `meridio-sidecar` (target+sidecar pods) |
| `kustomization.yaml` | Deploys controller-manager with OpenShift patches (RBAC finalizers + webhook/cert naming) |
| `nad.yaml` | NetworkAttachmentDefinitions (bridge CNI: bgp-net with VLAN 100, app-net) |
| `gateway.yaml` | Gateway + GatewayConfiguration |
| `routing.yaml` | GatewayRouter (IPv4 + IPv6, protocol: BGP) + L34Route |
| `dg.yaml` | DistributionGroup |
| `targets.yaml` | Target Pod Deployment (2 replicas + network-sidecar) |
| `rbac.yaml` | ServiceAccounts + Role + RoleBinding |
| `vpn-gateway.yaml` | VPN gateway ConfigMap (BIRD config, passive BGP) + Pod |
| `lb-deployment.yaml` | OpenShift-adapted LB template, injected via the `deploy-suite` override mechanism (`MERIDIO_TEMPLATE_PATH`) |

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

### OpenShift Compatibility Notes

The differences above stem from OpenShift's stricter default security posture compared to vanilla
Kubernetes. This section explains the underlying cause for each requirement.

- **Seccomp (`Unconfined` on loadbalancer container)**: The `RuntimeDefault` seccomp profile blocks
  `NETLINK_NETFILTER` messages — specifically nftables set management (`NFT_MSG_NEWSET`) and nfqueue
  binding (`NFQNL_CFG_CMD_BIND`). Both are required by NFQLB/nftables in the loadbalancer container.
  Standard netlink operations (interface addresses, routes, policy rules) used by the router and
  network-sidecar containers are **not** affected by `RuntimeDefault` — only the loadbalancer
  container needs `Unconfined`.

- **SELinux (`spc_t` on loadbalancer container)**: SELinux enforces at a separate kernel security
  module layer, independent of seccomp. Even when seccomp allows a syscall, `container_t`'s SELinux
  policy still lacks permissions for nfqueue bind and nftables set operations. Both seccomp **and**
  SELinux restrictions must be relaxed together — fixing only one still results in denial from the
  other. `spc_t` (super-privileged container) is scoped to the loadbalancer container only; the
  router container works fine under the default SELinux type.

- **Custom SCCs**: OpenShift's default `restricted-v2` SCC blocks the capabilities (`NET_ADMIN`,
  `IPC_LOCK`, `IPC_OWNER`, `NET_BIND_SERVICE`, `NET_RAW`), unsafe sysctls, and seccomp/SELinux
  settings above. `meridio-lb` and `meridio-sidecar` SCCs grant the minimum needed per Pod type.

- **KubeletConfig for unsafe sysctls**: Declaring unsafe sysctls in a Pod spec is a two-layer gate.
  The SCC's `allowedUnsafeSysctls` only satisfies *admission control* (whether the Pod spec is
  accepted). The kubelet separately enforces its own allowlist at *runtime* — without a matching
  `KubeletConfig`, the kubelet refuses to apply the sysctls even if the Pod was admitted.

- **Pod-level sysctls instead of tuning CNI NAD**: OpenShift's Multus blocks the `tuning` CNI plugin
  from setting *global* sysctls (e.g. `net.ipv4.conf.all.forwarding`) via NetworkAttachmentDefinition.
  Per-interface sysctls via NAD are unaffected, but this suite's requirements are global, so they're
  set via Pod `securityContext.sysctls` instead.

- **Bridge CNI instead of macvlan/VLAN-subinterfaces**: The macvlan-on-host-interface and raw VLAN
  subinterface approaches used by the Kind suites are Docker/host-networking conveniences not
  applicable inside a single OpenShift node. Bridge CNI with a VLAN-aware bridge (`br-meridio`,
  `br-meridio-app`) reproduces equivalent L2 adjacency using constructs OpenShift/Multus supports.

- **RBAC finalizer permissions**: OpenShift enables the `OwnerReferencesPermissionEnforcement`
  admission controller by default (optional on vanilla Kubernetes). `ctrl.SetControllerReference()`
  sets `blockOwnerDeletion: true`, which requires explicit `/finalizers` sub-resource RBAC permissions
  on the owner resources — `gateways/finalizers`, `distributiongroups/finalizers`, and
  `pods/finalizers` — otherwise the controller-manager cannot set owner references on Deployments,
  LoadBalancerEndpointSlices, and EndpointNetworkConfigurations respectively. This is patched in here
  via `kustomization.yaml` rather than the base `config/rbac/manager-role.yaml` because vanilla
  Kubernetes doesn't enforce the check by default; moving these rules into the base role would be a
  cleaner long-term fix since the underlying `SetControllerReference()` calls are unconditional.

- **nfnetlink_queue persistence**: Unlike some other distros, this module is not auto-loaded on
  CoreOS/OpenShift nodes and must be explicitly persisted via `/etc/modules-load.d/`.

- **Pod anti-affinity removed**: The LB Deployment template's default anti-affinity (spread across
  nodes) is meaningless — and blocks scheduling entirely — on a single-node cluster. Since Bridge CNI
  supports multiple LB Pods sharing the same bridge on one node, anti-affinity is safely dropped here.

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
