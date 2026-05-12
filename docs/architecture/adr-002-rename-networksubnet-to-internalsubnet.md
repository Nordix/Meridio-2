# ADR-002: Rename NetworkSubnet to InternalSubnet

## Status

Accepted

## Date

2026-05-05

## Context

Early customer feedback revealed that the name `NetworkSubnet` (JSON: `networkSubnets`) in `GatewayConfiguration` was misleading. Customers confused it with the external network attachments and configured external NAD subnets under this field, when it is strictly meant to identify the **internal** network segments where application endpoint IPs reside (LB-to-endpoint connectivity).

The field's purpose is to declare the subnet(s) on the internal secondary network that the ENC controller uses to:
- Match secondary interfaces in application Pods
- Determine IP family (IPv4/IPv6) for VIP and next-hop assignment

It has no relation to external-facing networks (e.g., the router's BGP peering interface configured via `networkAttachments`).

## Decision

Rename `NetworkSubnet` → `InternalSubnet` and `networkSubnets` → `internalSubnets` to clearly communicate that this field describes the internal application-facing network, not external connectivity.

Additionally, simplify the structure from `CIDRs []string` to `CIDR string` — each entry represents exactly one subnet (one CIDR, one IP family). Dual-stack requires two entries.

## Consequences

### Positive

- **Reduced misconfiguration**: The name `InternalSubnet` clearly distinguishes from external network attachments
- **Simpler model**: One CIDR per entry eliminates ambiguity about mixed IP families within a single entry
- **CEL validation**: Enforces that two entries cannot share the same IP family (prevents duplicate IPv4 entries)

### Negative

- **Breaking API change**: Existing `GatewayConfiguration` resources using `networkSubnets` must be updated to `internalSubnets` with the new single-CIDR structure
- Acceptable at v1alpha1 stage

### Migration

Before:
```yaml
spec:
  networkSubnets:
    - attachmentType: NAD
      cidrs:
        - "192.168.100.0/24"
        - "2001:db8:100::/64"
```

After:
```yaml
spec:
  internalSubnets:
    - attachmentType: NAD
      cidr: "192.168.100.0/24"
    - attachmentType: NAD
      cidr: "2001:db8:100::/64"
```

## References

- gh-120
