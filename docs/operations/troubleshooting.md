# Troubleshooting Guide

This guide helps diagnose common issues with Meridio-2. To troubleshoot effectively, familiarize yourself with the system in a healthy state first — check logs, interfaces, and routing tables when everything works.

## Traffic Flow

Follow the traffic path when troubleshooting. At each hop, traffic can be traced with `tcpdump` and routing state inspected with standard Linux tools (`ip`, `nft`, `birdc`, `nfqlb`).

```
                        External Network
                              │
                              │ VIP traffic (dst: VIP)
                              ▼
                   ┌─────────────────────┐
                   │   Gateway Router    │  External BGP peer
                   │  (external router)  │  Learns VIPs via BGP
                   └─────────┬───────────┘
                             │
              ┌──────────────┼──────────────┐
              │              │              │  ext-net (e.g. vlan)
              ▼              ▼              ▼
     ┌────────────┐ ┌────────────┐ ┌────────────┐
     │  SLLB Pod  │ │  SLLB Pod  │ │  SLLB Pod  │  LB Deployment
     │            │ │            │ │            │  (per Gateway)
     │ ┌────────┐ │ │ ┌────────┐ │ │ ┌────────┐ │
     │ │ router │ │ │ │ router │ │ │ │ router │ │  BIRD3: BGP sessions,
     │ │ (BIRD) │ │ │ │ (BIRD) │ │ │ │ (BIRD) │ │  VIP advertisement
     │ └────────┘ │ │ └────────┘ │ │ └────────┘ │
     │ ┌────────┐ │ │ ┌────────┐ │ │ ┌────────┐ │
     │ │  LB    │ │ │ │  LB    │ │ │ │  LB    │ │  nftables → nfqueue
     │ │(nfqlb) │ │ │ │(nfqlb) │ │ │ │(nfqlb) │ │  → Maglev hash
     │ └────────┘ │ │ └────────┘ │ │ └────────┘ │  → fwmark → policy route
     └─────┬──────┘ └─────┬──────┘ └─────┬──────┘
           │              │              │  int-net (e.g. macvlan)
           │    fwmark routing to        │  target Pod IP
           │    selected target          │
           ▼              ▼              ▼
     ┌────────────┐ ┌────────────┐ ┌────────────┐
     │  App Pod   │ │  App Pod   │ │  App Pod   │  Application Pods
     │ ┌────────┐ │ │ ┌────────┐ │ │ ┌────────┐ │
     │ │sidecar │ │ │ │sidecar │ │ │ │sidecar │ │  VIPs on interface,
     │ └────────┘ │ │ └────────┘ │ │ └────────┘ │  source-based routing
     │ ┌────────┐ │ │ ┌────────┐ │ │ ┌────────┐ │  (return traffic via
     │ │  app   │ │ │ │  app   │ │ │ │  app   │ │   LB next-hops)
     │ └────────┘ │ │ └────────┘ │ │ └────────┘ │
     └────────────┘ └────────────┘ └────────────┘
```

**Inbound path:** Gateway Router → (BGP) → SLLB Pod router → nftables → nfqueue → NFQLB Maglev hash → fwmark → policy route → App Pod

**Return path:** App Pod → sidecar source-based routing (VIP → routing table → LB next-hop) → Gateway Router

## Pods

A good starting point is checking the health of all Pods.

```bash
kubectl get pods -n <namespace>
```

Expected Pods:
- **controller-manager** — 1 Pod, 1/1 Ready
- **sllb-\<gateway-name\>** — LB Pods (2 containers each: `loadbalancer` + `router`), count matches `GatewayConfiguration.spec.horizontalScaling.replicas` (default: 2) unless HPA is managing the Deployment (`enforceReplicas: false`)
- **Application Pods** — with network-sidecar container if using EndpointNetworkConfiguration

All Pods should be Running with all containers ready. Check for restarts.

### Controller-manager not starting

**Symptom:** `gateway API CRDs not found`
**Cause:** Gateway API CRDs are not installed in the cluster. The controller-manager checks for the `gateway.networking.k8s.io` API group at startup.
**Fix:** Install Gateway API CRDs:
```bash
kubectl apply -f https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.5.0/standard-install.yaml
```

**Symptom:** `LB deployment template not found`
**Cause:** The ConfigMap with the LB deployment template is not mounted at the expected path.
**Fix:** Verify `config/templates` is included in kustomization resources and the template patch is applied. Check the `--template-path` flag matches the mount path (default: `/templates`).

**Symptom:** CrashLoopBackOff with `open /tmp/k8s-webhook-server/serving-certs/tls.crt: no such file or directory`
**Cause:** cert-manager hasn't provisioned the webhook certificate yet.
**Fix:** Wait for cert-manager to be ready. The controller-manager will self-recover after a few restarts. Alternatively, ensure cert-manager is deployed and ready before the controller-manager.

### LB Pods not starting

**Symptom:** `operation not permitted` errors in loadbalancer or router container logs.
**Cause:** Missing required Linux capabilities or `setcap` not applied to the binary in the container image.
**Fix:** Verify capabilities are set in the deployment template's `securityContext.capabilities.add` and that the container image has matching file capabilities.

#### Check required capabilities

Both containers drop all capabilities and add only what's needed:

| Capability | loadbalancer | router | Purpose |
|-----------|:---:|:---:|---------|
| `NET_ADMIN` | ✓ | ✓ | nftables, netlink routing rules/routes, NFQLB nfqueue access |
| `IPC_LOCK` | ✓ | | NFQLB shared memory (`mmap MAP_LOCKED`) |
| `IPC_OWNER` | ✓ | | NFQLB shared memory lifecycle |
| `NET_BIND_SERVICE` | | ✓ | BIRD binding to BGP port 179 |
| `NET_RAW` | | ✓ | BIRD `SO_BINDTODEVICE` socket option |

Verify file capabilities on the binaries:
```bash
kubectl exec -n <ns> <sllb-pod> -c loadbalancer -- getcap /app/stateless-load-balancer
# Expected: /app/stateless-load-balancer cap_net_admin,cap_ipc_lock,cap_ipc_owner=ep

kubectl exec -n <ns> <sllb-pod> -c router -- getcap /app/router
# Expected: /app/router cap_net_admin,cap_net_bind_service,cap_net_raw=ep
```

**Symptom:** `mkdir /var/run/meridio: read-only file system`
**Cause:** Missing `lb-run` emptyDir volume mount for the readiness directory.
**Fix:** Ensure the LB deployment template includes the `lb-run` volume and mount at `/var/run/meridio`.

#### Check required volume mounts

Both containers run with `readOnlyRootFilesystem: true` and require emptyDir volumes for writable paths:

| Volume | Mount path | Container | Purpose |
|--------|-----------|-----------|---------|
| `lb-run` | `/var/run/meridio` | loadbalancer | Readiness files for router |
| `bird-run` | `/var/run/bird` | router | BIRD control socket (`bird.ctl`) |
| `bird-etc` | `/etc/bird` | router | Generated `bird.conf` |
| `bird-log` | `/var/log/bird` | router | BIRD log file |

Verify mounts:
```bash
kubectl get pod -n <ns> <sllb-pod> -o jsonpath='{.spec.containers[*].volumeMounts}' | jq .
```

## Logs

Check logs with:
```bash
# Controller-manager
kubectl logs -n <ns> <controller-manager-pod>

# LB Pod containers
kubectl logs -n <ns> <sllb-pod> -c loadbalancer
kubectl logs -n <ns> <sllb-pod> -c router
```

Meridio-2 uses structured JSON logging via controller-runtime's zap logger.

BIRD logs are not sent to stdout — they are written to a file inside the router container. To view BIRD logs:
```bash
kubectl exec -n <ns> <sllb-pod> -c router -- cat /var/log/bird/bird.log
```
The log file path and size limit are configurable via `MERIDIO_BIRD_LOG_FILE` and `MERIDIO_BIRD_LOG_FILE_SIZE`.

## Enter Containers

```bash
# Interactive shell
kubectl exec -it -n <ns> <pod> -c <container> -- sh

# Single command
kubectl exec -n <ns> <pod> -c <container> -- ip rule
```

LB Pods run as non-root with `readOnlyRootFilesystem: true`. The Alpine-based images include tools like `iproute2` (`ip`), `tcpdump`, `nftables` (`nft`), and `nfqlb`. However, most tools requiring capabilities (e.g., `nft`, `tcpdump`, `nfqlb`) are not usable from an interactive shell by default.

The `ip` command works for read operations (`ip addr`, `ip rule`, `ip route show`) without special capabilities. `birdc` also works without capabilities in the router container.

For tools requiring capabilities, two restrictions apply:

1. **Container capabilities must include the required capability** — the default deployment template only adds the minimum capabilities needed for each container's main process. Additional capabilities (e.g., `NET_RAW` for `tcpdump`) must be added to `securityContext.capabilities.add`.

2. **`no_new_privs` blocks file capabilities in interactive shells** — when `allowPrivilegeEscalation: false` is set (recommended security default), the kernel sets `no_new_privs=1`. This prevents processes spawned from a shell from gaining capabilities via `setcap`, even if the binary has file capabilities and the capability is in the bounding set. The container's main entrypoint is not affected because the container runtime sets up capabilities before `no_new_privs` takes effect. To use privileged tools interactively, `allowPrivilegeEscalation` must be temporarily set to `true`.

| Tool | Capability needed | loadbalancer | router |
|------|------------------|:---:|:---:|
| `ip` (read: addr, rule, route show) | none | ✓ | ✓ |
| `birdc` | none | n/a | ✓ |
| `nft list ruleset` | `NET_ADMIN` | ✗ | ✗ |
| `tcpdump` | `NET_RAW` | ✗ | ✗ |
| `nfqlb show/flow-list` | `NET_ADMIN` + `IPC_LOCK` | ✗ | n/a |
| `ping` | `NET_RAW` | ✗ | ✗ |
| `ss -p` | `SYS_PTRACE` | ✗ | ✗ |

### Ephemeral containers

For Pods without a shell or when additional tools are needed. Since LB Pods have `runAsNonRoot: true` at the Pod level, a custom profile is required to set a non-root UID:

```bash
cat > /tmp/debug-profile.json << 'EOF'
{
  "securityContext": {
    "runAsUser": 65534,
    "runAsNonRoot": true
  }
}
EOF
kubectl debug -ti -n <ns> <pod> --image=alpine --target=<container> --custom=/tmp/debug-profile.json -- sh
```

Without the custom profile, the ephemeral container will fail with `CreateContainerConfigError: container has runAsNonRoot and image will run as root`.

Note: When using `--target`, the ephemeral container inherits the Pod's security constraints — no additional capabilities can be granted. This is useful for read-only inspection (`ip addr`, `ip rule`, `ip route show`, `cat /proc/...`) and running unprivileged tools not present in the LB images.

For tools requiring capabilities (`nft`, `tcpdump`, `nfqlb`), omit `--target` and use a custom profile with root and explicit capabilities. This gives the ephemeral container its own PID namespace (no process visibility into other containers) but shares the network namespace — which is what matters for network troubleshooting:
```bash
cat > /tmp/debug-privileged.json << 'EOF'
{
  "securityContext": {
    "runAsUser": 0,
    "runAsNonRoot": false,
    "capabilities": {
      "add": ["NET_ADMIN", "NET_RAW"]
    }
  }
}
EOF
kubectl debug -it -n <ns> <pod> --image=alpine --custom=/tmp/debug-privileged.json -- sh
# Inside: apk add --no-cache nftables tcpdump && nft list ruleset && tcpdump -ni any
```

Both `runAsUser: 0` and explicit `capabilities.add` are required — the container runtime drops all capabilities by default even for root.

### nsenter from the node

For full access to a Pod's network namespace with root privileges (bypassing capability and `no_new_privs` restrictions), use `nsenter` from the host node. 

```bash

# Enter the network namespace with full privileges
nsenter -t <pid> -n -u -- bash
nsenter -t <pid> -n -u -- ip rule
nsenter -t <pid> -n -u -- nft list ruleset
nsenter -t <pid> -n -u -- tcpdump -ni any
```

This is the most reliable way to run privileged networking tools (`nft`, `tcpdump`, `nfqlb`) against a container's network namespace without modifying the Pod's security context.

## Router (BIRD)

The router container runs BIRD 3.x for BGP session management.

### Check BIRD is running

```bash
kubectl exec -n <ns> <sllb-pod> -c router -- birdc -s /var/run/bird/bird.ctl show status
```

If BIRD is not running, `birdc` will fail with `Cannot connect to BIRD control socket`. Check the router container logs for startup errors. See also limitation about BIRD error propagation missing — the router controller does not crash if BIRD fails.

### Check BGP session status

```bash
kubectl exec -n <ns> <sllb-pod> -c router -- birdc -s /var/run/bird/bird.ctl show protocols
```

Expected output shows BGP sessions in `Established` state:
```
BIRD 3.1.5 ready.
Name       Proto      Table      State  Since         Info
VIP4       Static     master4    up     2026-03-29    
VIP6       Static     master6    up     2026-03-29    
NBR-gateway-router-v4 BGP        ---        up     09:02:51.101  Established   
NBR-gateway-router-v6 BGP        ---        up     09:02:40.874  Established   
device1    Device     ---        up     2026-03-29    
kernel1    Kernel     master4    up     2026-03-29    
kernel2    Kernel     master6    up     2026-03-29    
bfd1       BFD        ---        up     2026-03-29    
```

If sessions are `DOWN` or `Active`, check:
- GatewayRouter configuration (address, ASN, ports)
- External gateway router configuration
- Network connectivity on the external interface

### Check advertised VIP routes

```bash
kubectl exec -n <ns> <sllb-pod> -c router -- birdc -s /var/run/bird/bird.ctl show route protocol VIP4
kubectl exec -n <ns> <sllb-pod> -c router -- birdc -s /var/run/bird/bird.ctl show route protocol VIP6
```

VIPs should appear as static routes on `lo` exported to BGP peers.

### Check routes learned from BGP peers

```bash
kubectl exec -n <ns> <sllb-pod> -c router -- birdc -s /var/run/bird/bird.ctl show protocols
```

Use the BGP protocol names from the output (e.g., `NBR-gateway-router-v4`) to check learned routes:
```bash
kubectl exec -n <ns> <sllb-pod> -c router -- birdc -s /var/run/bird/bird.ctl 'show route where proto = "NBR-gateway-router-v4"'
```

### Check kernel routing table

BGP-learned routes (default routes from external gateways) should appear in kernel table 4096:
```bash
kubectl exec -n <ns> <sllb-pod> -c router -- ip route show table 4096
# Expected: default via <gateway-ip> dev <ext-interface> proto bird
```

If routes are missing or delayed (up to 60 seconds), this may be the BIRD 3.x scan time issue — see limitations document.

### Check policy routing rules

```bash
kubectl exec -n <ns> <sllb-pod> -c router -- ip rule
```

Expected rules for VIP source-based routing:
```
0:      from all lookup local
100:    from <vip> lookup 4096
101:    from <vip> lookup 4097
32766:  from all lookup main
32767:  from all lookup default
```

## Load Balancer (NFQLB)

### Check IP forwarding

IP forwarding must be enabled in the LB Pod's network namespace for traffic to be forwarded to/from targets:
```bash
kubectl exec -n <ns> <sllb-pod> -c loadbalancer -- sysctl net.ipv4.conf.all.forwarding net.ipv6.conf.all.forwarding
# Expected: net.ipv4.conf.all.forwarding = 1
#           net.ipv6.conf.all.forwarding = 1
```

### Check nftables

```bash
kubectl exec -n <ns> <sllb-pod> -c loadbalancer -- nft list ruleset
```

Example output:
```
table inet meridio {
	chain prerouting {
		type filter hook prerouting priority filter; policy accept;
	}
}
table inet meridio-lb {
	set ipv4-vips {
		type ipv4_addr
		flags interval
		elements = { 20.0.0.1, 20.0.0.2,
			     40.0.0.1, 50.0.0.1 }
	}

	set ipv6-vips {
		type ipv6_addr
		flags interval
		elements = { 2000::1,
			     3000::1 }
	}

	chain prerouting {
		type filter hook prerouting priority filter; policy accept;
		ip daddr @ipv4-vips counter packets 4781 bytes 2062892 queue to 0-3
		ip6 daddr @ipv6-vips counter packets 0 bytes 0 queue to 0-3
	}

	chain output {
		type filter hook output priority filter; policy accept;
		meta l4proto icmp ip daddr @ipv4-vips counter packets 0 bytes 0 queue to 0-3
		meta l4proto ipv6-icmp ip6 daddr @ipv6-vips counter packets 0 bytes 0 queue to 0-3
	}
}
```

- `ipv4-vips` / `ipv6-vips` sets contain VIPs from L34Route `destinationCIDRs`
- `prerouting` chain queues VIP-destined traffic to nfqueue 0-3 for NFQLB processing
- `output` chain queues locally-originated ICMP/ICMPv6 to VIPs — primarily needed for generating ICMP Fragmentation Needed / Packet Too Big replies back to endpoints when forwarded packets hit MTU limits on the external network
- Packet/byte counters help verify traffic is reaching the LB

Note: `nft` requires `NET_ADMIN` and `allowPrivilegeEscalation: true` as explained. Alternatively, use nsenter instead.

### Check nfqueue status

Verify that NFQLB is listening on the expected queues:
```bash
kubectl exec -n <ns> <sllb-pod> -c loadbalancer -- cat /proc/net/netfilter/nfnetlink_queue
```

Example output:
```
    0     17     0 2  1500     0     0        0  1
    1 4026592312     0 2  1500     0     0      783  1
    2 3582758986     0 2  1500     0     0     3998  1
    3 2599662713     0 2  1500     0     0        0  1
```

The first column is the queue number (0-3 matching the nftables `queue to 0-3` rule). If no queues are listed, NFQLB is not running or failed to bind to the queues.

### Check NFQLB shared memory instances

```bash
kubectl exec -n <ns> <sllb-pod> -c loadbalancer -- nfqlb show --shm=tshm-<dg-name>
```

Example output:
```
Shm: tshm-test-backend
  Fw: own=0
  Maglev: M=3191, N=32
   Lookup: 1 2 0 0 3 2 3 2 2 1 1 3 0 3 2 2 0 0 2 0 0 1 1 3 2...
   Active: 5000(0) 5001(1) 5002(2) 5003(3)
```

```
Shm: tshm-test-backend-indirect
  Fw: own=0
  Maglev: M=3191, N=32
   Lookup: 1 2 0 0 3 2 3 2 2 1 1 3 0 3 2 2 0 0 2 0 0 1 1 3 2...
   Active: 6024(0) 6025(1) 6026(2) 6027(3)
```

- `M` is the Maglev hash table size (prime near `maxEndpoints × 100`), `N` is the max endpoints capacity
- `Lookup` shows the Maglev hash table entries (target indices)
- `Active` lists active targets as `fwmark(index)` — each fwmark maps to a policy routing rule that forwards traffic to the target Pod IP. Different DGs use different fwmark ranges (e.g., 5000+ vs 6024+) based on their DG ID offset

### Check NFQLB flows

```bash
kubectl exec -n <ns> <sllb-pod> -c loadbalancer -- nfqlb flow-list
```

Example output:
```json
[{
  "name": "tshm-test-backend-indirect-test-route-indirect",
  "priority": 2,
  "protocols": [ "udp" ],
  "dests": [
    "::ffff:50.0.0.1/128"
  ],
  "matches_count": 289,
  "user_ref": "tshm-test-backend-indirect"
},
{
  "name": "tshm-test-backend-test-route",
  "priority": 1,
  "protocols": [ "tcp" ],
  "dests": [
    "::ffff:20.0.0.1/128",
    "::ffff:20.0.0.2/128",
    "::ffff:40.0.0.1/128",
    "2000::1/128",
    "3000::1/128"
  ],
  "matches_count": 1923,
  "user_ref": "tshm-test-backend"
}]
```

Each flow corresponds to an L34Route. The `name` is `tshm-<dg-name>-<route-name>`, `user_ref` is the NFQLB shared memory instance (`tshm-<dg-name>`), `dests` are the VIPs from `destinationCIDRs` (IPv4 shown as IPv4-mapped IPv6), and `matches_count` tracks how many packets matched this flow.

### Check fwmark routing to targets

```bash
kubectl exec -n <ns> <sllb-pod> -c loadbalancer -- ip rule
kubectl exec -n <ns> <sllb-pod> -c loadbalancer -- ip route show table <fwmark>
```

Each active target per DistributionGroup should have:
- An `ip rule` entry: `from all fwmark <fwmark> lookup <table>`
- A route in the corresponding table: `default via <target-ip> dev <interface>`

Verify target IPs are reachable:
```bash
kubectl exec -n <ns> <sllb-pod> -c loadbalancer -- ping -c1 -W1 <target-ip>
```
Note: `ping` requires `NET_RAW` and `allowPrivilegeEscalation: true` as explained. Alternatively, use nsenter instead.

## Network Sidecar

The sidecar runs in application Pods and configures VIPs and source-based routing based on the EndpointNetworkConfiguration (ENC) CR.

### Prerequisites

Application Pods must be attached to the same secondary network(s) as the LB Pods (e.g., via Multus NAD annotations). Meridio-2 does not provision or manage secondary network connectivity — it assumes the network is already in place. If a Pod is not attached to the expected network, the sidecar will fail to find a matching interface and the DistributionGroup controller will not include the Pod in EndpointSlices.

The sidecar container requires `NET_ADMIN` capability for netlink operations (adding VIPs, configuring routing rules and tables). This must be set in the application Pod's sidecar container spec:
```yaml
securityContext:
  capabilities:
    add:
    - NET_ADMIN
```
Without this capability, the sidecar container will fail to start.

### Check ENC exists for the Pod

```bash
kubectl get enc -n <ns> <pod-name> -o yaml
```

The ENC should exist with the same name as the Pod and contain `gateways` with domains, VIPs, and next-hops.

If the ENC is missing:
- Check the Pod matches a DistributionGroup selector
- Check the DG references a Gateway (via parentRefs or L34Route)
- Check the Gateway has `Accepted=True` condition

### Check VIPs on the interface

```bash
kubectl exec -n <ns> <pod> -c <sidecar> -- ip addr show
```

VIPs should appear as /32 (IPv4) or /128 (IPv6) addresses on the secondary interface.

### Check source-based routing rules

```bash
kubectl exec -n <ns> <pod> -c <sidecar> -- ip rule
```

Expected: rules for each VIP pointing to a routing table in the 50000–55000 range.

### Check ECMP routes to LB Pods

```bash
kubectl exec -n <ns> <pod> -c <sidecar> -- ip route show table <table-id>
```

Expected: default route with next-hops pointing to SLLBR Pod IPs (ECMP if multiple LB Pods).

### Sidecar in backoff

If the sidecar logs show repeated `InterfaceNotFoundError`, the ENC contains a domain for a subnet where the Pod has no matching interface. This triggers exponential backoff retries. Check:
- Pod's Multus network-status annotation matches the GatewayConfiguration networkSubnets
- The secondary interface exists and has an IP in the expected subnet

## Gateway and CRD Status

### Check Gateway status

```bash
kubectl get gateway -n <ns> <name> -o yaml
```

Inspect conditions:
```bash
kubectl get gateway -n <ns> <name> -o jsonpath='{.status.conditions}' | jq .
```

Two condition types are used (per Gateway API GEP-1364):

**`Accepted`** — indicates whether the Gateway configuration is valid:
- `Accepted=True`, reason `Accepted` — GatewayClass matches the controller and GatewayConfiguration is valid. Message: `"Gateway accepted by <controller-name>"`.
- `Accepted=False`, reason `InvalidParameters` — validation failed. The message describes the specific issue (e.g., missing GatewayConfiguration, invalid template, bad network attachment config).
- `Accepted=Unknown`, reason `Pending` — the Gateway is not currently managed by this controller. Message: `"Waiting for controller"`. This is set when the controller releases a Gateway (e.g., GatewayClass changed).

**`Programmed`** — indicates whether the LB Deployment has been reconciled:
- `Programmed=True`, reason `Programmed` — LB Deployment has been created or updated successfully. Message: `"LB Deployment reconciled"`.
- `Programmed=False`, reason `Invalid` — a permanent error prevented Deployment creation (e.g., name collision with an existing Deployment not owned by this Gateway). The message describes the error.

`status.addresses` should list VIPs from L34Routes. If empty, check that L34Routes exist with `parentRefs` pointing to this Gateway and that `destinationCIDRs` are set.

### Check DistributionGroup status

```bash
kubectl get distg -n <ns> -o wide
```

Inspect conditions:
```bash
kubectl get distg -n <ns> <dg-name> -o jsonpath='{.status.conditions}' | jq .
```

Two condition types are used:

**`Ready`** — indicates whether the DG has active endpoints:
- `Ready=True`, reason `EndpointsAvailable` — EndpointSlices have been reconciled with at least one endpoint.
- `Ready=False`, reason `NoEndpoints` — no endpoints are available. The `message` field explains why:
  - `"No Pods match selector"` — no Pods match the DG's label selector.
  - `"No Gateways reference this DistributionGroup"` — no L34Route links this DG to a Gateway (check `parentRefs` or L34Route `backendRefs`).
  - `"No accepted Gateways found"` — referenced Gateways don't have `Accepted=True` (Gateway may not exist or GatewayConfiguration is invalid).
  - `"No network context available"` — the GatewayConfiguration has no `networkSubnets` configured.
  - `"No endpoints available"` — Pods exist but none have an IP matching the network subnets.

**`CapacityExceeded`** (Maglev only) — present only when the number of matching Pods exceeds `maxEndpoints`:
- `CapacityExceeded=True`, reason `MaglevCapacityExceeded` — some Pods were excluded from EndpointSlices because the Maglev table is full. The message lists affected networks with counts (e.g., `"10.0.0.0/24: 5/37 pods excluded (32 capacity)"`).
- Absent — capacity is sufficient; all matching Pods have Maglev IDs assigned.

### Check EndpointSlices

List EndpointSlices for a DistributionGroup:
```bash
kubectl get endpointslice -n <ns> -l meridio-2.nordix.org/distribution-group=<dg-name>
```

Inspect details (a DG may own multiple slices — one per network subnet, and additional slices when exceeding 100 endpoints per slice):
```bash
kubectl get endpointslice -n <ns> -l meridio-2.nordix.org/distribution-group=<dg-name> -o yaml
```

Each EndpointSlice has three identifying labels:
- `meridio-2.nordix.org/distribution-group` — the owning DistributionGroup name
- `meridio-2.nordix.org/network-subnet` — the network subnet CIDR encoded for label compatibility: `/` replaced with `-`, `:` replaced with `_` (e.g., `192.168.100.0-24` for `192.168.100.0/24`, `fd00__1-64` for `fd00::1/64`)
- `endpointslice.kubernetes.io/managed-by` — must be `distributiongroup-controller.meridio-2.nordix.org`

There is one set of EndpointSlices per network subnet (e.g., separate slices for IPv4 and IPv6 in dual-stack). The `addressType` field reflects the IP family (`IPv4` or `IPv6`).

Each endpoint should have:
- `addresses` — the Pod's secondary network IP (matching the subnet CIDR)
- `conditions.ready` — `true` if the Pod is Ready
- `targetRef` — reference to the source Pod
- `zone` — Maglev ID in the format `maglev:<N>` (e.g., `maglev:5`). This is the identifier used by the LB controller to activate targets in the NFQLB shared memory instance

If EndpointSlices are missing:
- Check the DistributionGroup status conditions for the specific reason (see "Check DistributionGroup status" above)
- Verify Pods have the expected secondary network IP via Multus annotation: `kubectl get pod <name> -o jsonpath='{.metadata.annotations.k8s\.v1\.cni\.cncf\.io/network-status}'`
- Verify the Pod IP falls within a `GatewayConfiguration.spec.networkSubnets` CIDR

### Verify GatewayConfiguration network setup

If traffic is not flowing, verify that both external and internal networks are correctly configured in the GatewayConfiguration:

```bash
kubectl get gatewayconfiguration -n <ns> <name> -o yaml
```

Check:
- `spec.networkAttachments` — lists the secondary network interfaces (NADs) attached to LB Pods. Must include both the external interface (towards the gateway router) and the internal interface(s) (towards application Pods). Network attachments may also be defined in the LB deployment template; the GatewayConfiguration entries are merged on top.
- `spec.networkSubnets` — lists the CIDRs of the internal network(s) where application Pod IPs reside. Used by the DistributionGroup controller to select the correct Pod IP when building EndpointSlices. Must cover all application Pod secondary IPs.

Missing or misconfigured entries here are a common cause of traffic issues even when all resources show healthy status.
