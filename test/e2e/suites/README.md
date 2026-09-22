# E2E Test Suites — Addressing & Convention Guide

This document describes the addressing scheme, VLAN assignments, and conventions
used across all e2e test suites. Follow these conventions when creating new suites.

## Architecture Overview

Each test suite deploys in its own namespace and uses:

1. **External network** (VLAN subinterface) — BGP peering between LB pods and VPN gateway
2. **Internal network** (macvlan) — traffic distribution from LB pods to target pods
3. **VIPs** — virtual IPs advertised via BGP, attracting external traffic

```
                    ┌────────────────────────────┐
                    │       VPN Gateway          │
                    │   (Docker container)       │
                    │   ASN 4200000000           │
                    └─────────┬──────────────────┘
                              │  VLAN (external network)
                              │  BGP peering
                    ┌─────────┴──────────────────┐
                    │       LB Pods (SLLBR)      │
                    │   vlan-XXX: 169.254.X.Y    │
                    │   net-YYY:  169.111.Y.Z    │
                    └─────────┬──────────────────┘
                              │  Internal (app network)
                              │  Maglev hashing
                    ┌─────────┴──────────────────┐
                    │       Target Pods          │
                    │   net1: 169.111.Y.Z + VIP  │
                    └────────────────────────────┘
```

## Critical Design Rule

> **External and internal networks MUST use different subnets.**

The kernel resolves BGP next-hops by matching the destination address against
locally-connected subnets. If the external and internal interfaces share a subnet,
the kernel may pick the wrong interface, breaking return traffic.

IPv4 achieves this with `169.254.x` (external) vs `169.111.x` (internal).
IPv6 achieves this with `fd00:cafe:{X}::` (external) vs `fd00:cafe:1{X}0::` (internal).

## IPv4 Addressing Scheme

| Role | Pattern | Example |
|------|---------|---------|
| External (VLAN/BGP) | `169.254.{group}.0/24` | `169.254.10.0/24` |
| VPN gateway address | `169.254.{group}.150` | `169.254.10.150` |
| Internal (App net) | `169.111.{group}.0/24` | `169.111.10.0/24` |
| VIP | `{vip_prefix}.0.0.{N}/32` | `10.0.0.1/32` |

## IPv6 Addressing Scheme (Dual-Stack)

| Role | Pattern | Example |
|------|---------|---------|
| External (VLAN/BGP) | `fd00:cafe:{X}::/64` | `fd00:cafe:10::/64` |
| VPN gateway address | `fd00:cafe:{X}::150` | `fd00:cafe:10::150` |
| Internal (App net) | `fd00:cafe:1{X}0::/64` | `fd00:cafe:110::/64` |
| VIP | `fd00:cafe:{Z}::{N}/128` | `fd00:cafe:1::1/128` |

## VLAN & ASN Allocation Table

| VLAN ID | Suite | IP Family | Gateway | External Subnet (IPv4) | External Subnet (IPv6) | Internal Subnet (IPv4) | Internal Subnet (IPv6) | VIP(s) | Local ASN | Remote ASN |
|---------|-------|-----------|---------|----------------------|----------------------|----------------------|----------------------|--------|-----------|------------|
| 100 | separate-appnetwork-v4 | IPv4 | gw-a1 | `169.254.10.0/24` | — | `169.111.10.0/24` | — | `10.0.0.1/32` | 64512 | 4200000000 |
| 200 | separate-appnetwork-v4 | IPv4 | gw-a2 | `169.254.11.0/24` | — | `169.111.10.0/24` | — | `10.0.0.2/32` | 64513 | 4200000000 |
| 300 | shared-appnetwork | IPv4 | gw-b1 | `169.254.20.0/24` | — | `169.111.20.0/24` | — | `20.0.0.1/32` | 64514 | 4200000000 |
| 300 | shared-appnetwork-ds | dual-stack | gw-bds1 | `169.254.20.0/24` | `fd00:cafe:20::/64` | `169.111.20.0/24` | `fd00:cafe:120::/64` | `20.0.0.1/32`, `fd00:cafe:2::1/128` | 64514 | 4200000000 |
| 400 | shared-appnetwork | IPv4 | gw-b2 | `169.254.21.0/24` | — | `169.111.20.0/24` | — | `20.0.0.2/32` | 64515 | 4200000000 |
| 400 | shared-appnetwork-ds | dual-stack | gw-bds2 | `169.254.21.0/24` | `fd00:cafe:21::/64` | `169.111.20.0/24` | `fd00:cafe:120::/64` | `20.0.0.2/32`, `fd00:cafe:2::2/128` | 64515 | 4200000000 |
| 500 | sctp-multihoming | IPv4 | sctp-gw1 | `169.254.30.0/24` | — | `169.111.30.0/24` | — | `30.0.0.1/32` | 64516 | 4200000000 |
| 600 | sctp-multihoming | IPv4 | sctp-gw2 | `169.254.31.0/24` | — | `169.111.30.0/24` | — | `30.0.0.2/32` | 64517 | 4200000000 |
| 700 | ipv4-simple | IPv4 | gw-m1 | `169.254.40.0/24` | — | `169.111.40.0/24` | — | `40.0.0.1/32` | 64518 | 4200000000 |
| 700 | dual-stack-simple | dual-stack | gw-ds | `169.254.40.0/24` | `fd00:cafe:40::/64` | `169.111.40.0/24` | `fd00:cafe:140::/64` | `40.0.0.1/32`, `fd00:cafe:4::1/128` | 64518 | 4200000000 |
| 800 | pod-cache-label | IPv4 | gw-pcl | `169.254.50.0/24` | — | `169.111.50.0/24` | — | `50.0.0.1/32` | 64519 | 4200000000 |
| 900 | tcp-ao | IPv4 | gw-t1 | `169.254.60.0/24` | — | `169.111.60.0/24` | — | `60.0.0.1/32` | 64520 | 4200000000 |
| 1000 | tcp-ao | IPv4 | gw-t2 | `169.254.61.0/24` | — | `169.111.60.0/24` | — | `60.0.0.2/32` | 64521 | 4200000000 |
| 1100 | separate-static-appnetwork | IPv4 | gw-a1 | `169.254.110.0/24` | — | `169.111.110.0/24` | — | `110.0.0.1/32` | — (static+BFD) | — |
| 1200 | separate-static-appnetwork | IPv4 | gw-a2 | `169.254.111.0/24` | — | `169.111.110.0/24` | — | `110.0.0.2/32` | — (static+BFD) | — |
| 1300 | separate-appnetwork-v6 | IPv6 | gw-v6a1 | — | `fd00:cafe:70::/64` | — | `fd00:cafe:170::/64` | `fd00:cafe:7::1/128` | 64522 | 4200000000 |
| 1400 | separate-appnetwork-v6 | IPv6 | gw-v6a2 | — | `fd00:cafe:71::/64` | — | `fd00:cafe:170::/64` | `fd00:cafe:7::2/128` | 64523 | 4200000000 |

**Next available:** VLAN 1500, ASN 64524, external `169.254.80.0/24` / `fd00:cafe:80::/64`, internal `169.111.80.0/24` / `fd00:cafe:180::/64`, VIP `80.0.0.1/32` / `fd00:cafe:8::1/128`

## Adding a New Suite

### 1. Choose identifiers

Pick the next available VLAN ID, ASN, and address group from the table above.
Follow the numeric progression:

- VLAN: increment by 100
- ASN: increment by 1 from 64519
- External IPv4: `169.254.{next_group}.0/24` (next_group = 60, 70, ...)
- Internal IPv4: `169.111.{next_group}.0/24`
- VIP IPv4: `{next_group}.0.0.{N}/32`

For dual-stack suites, additionally:
- External IPv6: `fd00:cafe:{X}::/64` where X is a new unique identifier
- Internal IPv6: `fd00:cafe:1{X}0::/64` (MUST differ from external)
- VIP IPv6: `fd00:cafe:{Z}::{N}/128`

Also record the suite's **IP Family** in the allocation table — one of `IPv4`,
`IPv6`, or `dual-stack` — matching which address families the suite exercises.

**VLAN IDs may be reused across suites of a different IP Family.** A VLAN ID is
only unique per IP family, so the same VLAN can host one IPv4 suite, one IPv6
suite, and one dual-stack suite. For example, VLAN 100 is shared by
`separate-appnetwork-v4` (IPv4) and `dual-stack` (dual-stack); a pure-IPv6 suite
could reuse VLAN 100 as well. Suites that share a VLAN ID are mutually exclusive
and cannot be deployed simultaneously (see Notes).

### 2. Register on the VPN gateway

Add the VLAN interface and BGP protocol to `hack/vpn-gateway/`:

**`init.sh`** — add VLAN interface:
```bash
# VLAN {ID} — {suite-name} {gw-name}
ip link add link eth0 name vlan{N} type vlan id {ID}
ip link set vlan{N} up
ip addr add 169.254.{group}.150/24 dev vlan{N}
# For dual-stack, also:
ip addr add fd00:cafe:{X}::150/64 dev vlan{N}
```

**`bird-gw.conf`** — add BGP protocol:
```
protocol bgp GW4_{NAME} from LINK {
    local 169.254.{group}.150 port 10179 as 4200000000;
    neighbor range 0.0.0.0/0 port 10179 as {local_asn};
    dynamic name "GW4_{NAME}_";
    ipv4 {
        import all;
        export filter bgp_announce;
    };
}
```

For dual-stack, add a separate IPv6 BGP protocol:
```
protocol bgp GW6_{NAME} from LINK {
    local fd00:cafe:{X}::150 port 10179 as 4200000000;
    neighbor range ::/0 port 10179 as {local_asn};
    dynamic name "GW6_{NAME}_";
    ipv6 {
        import all;
        export filter bgp_announce6;
    };
}
```

### 3. Create suite resources

Each suite needs these files in `test/e2e/suites/{suite-name}/`:

| File | Purpose |
|------|---------|
| `nad.yaml` | NetworkAttachmentDefinitions (sysctl-tuning, VLAN, app-net) |
| `gateway.yaml` | Gateway + GatewayConfiguration |
| `routing.yaml` | GatewayRouter(s) + L34Route(s) |
| `dg.yaml` | DistributionGroup(s) |
| `targets.yaml` | Target Pod Deployment |
| `rbac.yaml` | ServiceAccount + RoleBinding for network-sidecar |
| `kustomization.yaml` | Kustomize resource list |

### 4. NAD conventions

**Sysctl tuning** — always included, always `interface: dummy`:
```yaml
networkAttachments:
- type: NAD
  nad:
    name: sysctl-tuning
    interface: dummy
```

**VLAN NAD** — external peering, always excludes `.150` (VPN gateway):
```yaml
ipRanges:
- { "range": "169.254.{group}.0/24", "exclude": ["169.254.{group}.150/32"] }
# Dual-stack adds:
- { "range": "fd00:cafe:{X}::/64", "exclude": ["fd00:cafe:{X}::150/128"] }
```

**App network NAD** — internal, uses macvlan on eth0:
```yaml
ipRanges:
- { "range": "169.111.{group}.0/24" }
# Dual-stack adds:
- { "range": "fd00:cafe:1{X}0::/64" }
```

### 5. Namespace naming

Use `e2e-{suite-name}` (e.g., `e2e-dual-stack-simple`, `e2e-separate-appnetwork-v4`).

## Maintaining This Document

Update this README whenever you add, remove, or modify a test suite. Specifically:
- Add new rows to the VLAN & ASN Allocation Table (including the suite's IP Family)
- Update the "Next available" line
- Document any new addressing patterns or conventions introduced

## Notes

- The VPN gateway always uses `.150` as its address on every VLAN
- The VPN gateway's remote ASN is always `4200000000`
- All BGP sessions use port `10179` (both local and remote)
- BFD is enabled on all sessions with 300ms intervals and multiplier 3 (or 5 for SCTP)
- A VLAN ID is unique only per IP Family: the same VLAN ID may be reused by suites of a different IP Family (e.g. one IPv4, one IPv6, and one dual-stack suite could all use VLAN 100)
- Suites sharing the same VLAN ID are mutually exclusive (deploy only one at a time), regardless of IP Family
- The `separate-static-appnetwork` suite uses static routing with BFD. LB pod IPs are limited to `.1`-`.10` per VLAN (max 10 replicas per gateway) to match the gateway's pre-configured static routes.

## Shared `ipv4-simple` Topology — Single-Suite Execution

Three Go test suites are pinned to the **same** `ipv4-simple` topology (namespace
`e2e-ipv4-simple`, Deployment `target-m`, gateway `gw-m1`, VIP `40.0.0.1`):

| Suite (Ginkgo `Describe`) | Test file | What it does |
|---------------------------|-----------|--------------|
| `Low MTU` | `e2e_suite_lowmtu_test.go` | PMTU discovery (ICMP Frag Needed) |
| `Resiliency` | `e2e_suite_resiliency_test.go` | kills the NFQLB process, verifies recovery |
| `E2E BGP VIP Advertisement` | `e2e_suite_bgp_test.go` | scales `target-m` to 0 / flips pod readiness, verifies VIP withdraw/re-advertise on the DCGW |

Because Resiliency and BGP are **disruptive** (process kills, scale-to-0, readiness
flips) against this shared Deployment/gateway/VIP, these suites **must run one at a
time against a freshly deployed `ipv4-simple` topology**. This is enforced/relied on by:

- **`Serial` decorator** on all three `Describe`s — under `ginkgo -p` they never run
  concurrently with each other or any other spec (they run isolated after the parallel
  pool drains). **Do not remove `Serial` from any of them.**
- **Makefile isolation** — each has its own deploy+test target
  (`make low-mtu`, `make resiliency`, `make bgp`), all depending on `deploy-ipv4-simple`;
  they are not deployed/run together.
- **State restoration** — each suite restores the topology to a healthy baseline
  (`DeferCleanup` / recovery waits) so the next suite starts clean.

To reduce the risk of a future cross-suite coupling bug, the topology identifiers
(`target-m` / `40.0.0.1` / `gw-m1` / `e2e-ipv4-simple`) should ideally be shared
constants across the three files rather than duplicated literals — if the manifest
changes, all three suites must be updated together.
