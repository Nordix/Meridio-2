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

| VLAN ID | Suite | Gateway | External Subnet (IPv4) | External Subnet (IPv6) | Internal Subnet (IPv4) | Internal Subnet (IPv6) | VIP(s) | Local ASN | Remote ASN |
|---------|-------|---------|----------------------|----------------------|----------------------|----------------------|--------|-----------|------------|
| 100 | separate-appnetwork | gw-a1 | `169.254.10.0/24` | — | `169.111.10.0/24` | — | `10.0.0.1/32` | 64512 | 4200000000 |
| 100 | dual-stack | gw-ds | `169.254.10.0/24` | `fd00:cafe:10::/64` | `169.111.10.0/24` | `fd00:cafe:110::/64` | `10.0.0.1/32`, `fd00:cafe:1::1/128` | 64512 | 4200000000 |
| 200 | separate-appnetwork | gw-a2 | `169.254.11.0/24` | — | `169.111.10.0/24` | — | `10.0.0.2/32` | 64513 | 4200000000 |
| 300 | shared-appnetwork | gw-b1 | `169.254.20.0/24` | — | `169.111.20.0/24` | — | `20.0.0.1/32` | 64514 | 4200000000 |
| 400 | shared-appnetwork | gw-b2 | `169.254.21.0/24` | — | `169.111.20.0/24` | — | `20.0.0.2/32` | 64515 | 4200000000 |
| 500 | sctp-multihoming | sctp-gw1 | `169.254.30.0/24` | — | `169.111.30.0/24` | — | `30.0.0.1/32` | 64516 | 4200000000 |
| 600 | sctp-multihoming | sctp-gw2 | `169.254.31.0/24` | — | `169.111.30.0/24` | — | `30.0.0.2/32` | 64517 | 4200000000 |
| 700 | ipv4-simple | gw-m1 | `169.254.40.0/24` | — | `169.111.40.0/24` | — | `40.0.0.1/32` | 64518 | 4200000000 |
| 800 | pod-cache-label | gw-pcl | `169.254.50.0/24` | — | `169.111.50.0/24` | — | `50.0.0.1/32` | 64519 | 4200000000 |
| 900 | tcp-ao | gw-t1 | `169.254.60.0/24` | — | `169.111.60.0/24` | — | `60.0.0.1/32` | 64520 | 4200000000 |
| 1000 | tcp-ao | gw-t2 | `169.254.61.0/24` | — | `169.111.60.0/24` | — | `60.0.0.2/32` | 64521 | 4200000000 |
| 1100 | separate-static-appnetwork | gw-a1 | `169.254.110.0/24` | — | `169.111.110.0/24` | — | `110.0.0.1/32` | — (static+BFD) | — |
| 1200 | separate-static-appnetwork | gw-a2 | `169.254.111.0/24` | — | `169.111.110.0/24` | — | `110.0.0.2/32` | — (static+BFD) | — |

**Next available:** VLAN 1100, ASN 64522, external `169.254.70.0/24`, internal `169.111.70.0/24`, VIP `70.0.0.1/32`

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

Use `e2e-{suite-name}` (e.g., `e2e-dual-stack`, `e2e-separate-appnetwork`).

## Maintaining This Document

Update this README whenever you add, remove, or modify a test suite. Specifically:
- Add new rows to the VLAN & ASN Allocation Table
- Update the "Next available" line
- Document any new addressing patterns or conventions introduced

## Notes

- The VPN gateway always uses `.150` as its address on every VLAN
- The VPN gateway's remote ASN is always `4200000000`
- All BGP sessions use port `10179` (both local and remote)
- BFD is enabled on all sessions with 300ms intervals and multiplier 3 (or 5 for SCTP)
- The `separate-appnetwork` and `dual-stack` suites share VLAN 100 — they cannot run simultaneously
- Suites sharing the same VLAN are mutually exclusive (deploy only one at a time)
- The `separate-static-appnetwork` suite uses static routing with BFD. LB pod IPs are limited to `.1`-`.10` per VLAN (max 10 replicas per gateway) to match the gateway's pre-configured static routes.
