# TCP-AO Key Rotation

## Overview

Meridio-2 supports TCP Authentication Option ([RFC 5925](https://datatracker.ietf.org/doc/html/rfc5925)) for securing BGP sessions between LB Pods and external routers. TCP-AO supersedes the deprecated TCP MD5 option (RFC 2385) with stronger cryptographic algorithms and built-in support for hitless key rotation.

This document describes operational procedures for rotating TCP-AO keys without disrupting BGP sessions.

## Concepts

TCP-AO uses key IDs to coordinate which key is active between peers:

- **SendId / RecvId**: Each key has a send ID and receive ID. The send ID on one side must match the receive ID on the other side.
- **CurrentKeyId**: The key currently preferred for sending. Maps to BIRD's `preferred` key attribute.
- **NextKeyId**: The key the peer should transition to next. Maps to BIRD's `rnext id`. This signals the remote end which key it should start using for sending.

The GatewayRouter CRD exposes these via `spec.bgp.authentication`:

```yaml
spec:
  bgp:
    authentication:
      keychain:
        - sendId: <0-255>
          recvId: <0-255>
          algorithm: <mac-algorithm>
          secretName: <secret-name>
          secretKey: <key-in-secret>
      currentKeyId: <sendId of the active key>
      nextKeyId: <sendId the peer should transition to>
```

## Secret Management

Master keys are stored in Kubernetes Secrets. Each keychain entry references a Secret by name and a key within that Secret's `data` section.

**Recommendation: Co-locate keys in a single Secret per GatewayRouter (or a small number of Secrets).** Scattering keys across many Secrets increases the risk of partial resolution failures during reconciliation. When the router controller cannot resolve any Secret referenced by a GatewayRouter's keychain, it retains the previous BIRD configuration unchanged (see [Partial Resolution Failures](#partial-resolution-failures) below). Keeping all keys in one Secret ensures they appear atomically to the controller.

Example Secret:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: bgp-auth-keys
  namespace: meridio
type: Opaque
stringData:
  key1: "first-master-key-value"
  key2: "second-master-key-value"
  key3: "third-master-key-value"
```

## Key Rotation Procedure

A hitless key rotation requires coordination between Meridio and the remote BGP peer. The exact commands for the remote end depend on your router vendor/software — the steps below describe only what needs to happen logically on each side.

### Prerequisites

- The current BGP session is established with key ID 1.
- A new key (ID 2) has been agreed upon with the network operations team.

### Step 1: Add the New Key to Both Sides

Add the new key material to the Kubernetes Secret:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: bgp-auth-keys
  namespace: meridio
type: Opaque
stringData:
  key1: "current-master-key"
  key2: "new-master-key"
```

Add key ID 2 to the GatewayRouter keychain (keep the current key active):

```yaml
spec:
  bgp:
    authentication:
      keychain:
        - sendId: 1
          recvId: 1
          algorithm: hmac sha256
          secretName: bgp-auth-keys
          secretKey: key1
        - sendId: 2
          recvId: 2
          algorithm: hmac sha256
          secretName: bgp-auth-keys
          secretKey: key2
      currentKeyId: 1
```

On the remote BGP peer, add key ID 2 with the same master key value. Both sides must accept the new key for receiving before either side starts sending with it.

### Step 2: Signal Key Transition

Once both sides have the new key installed, signal that the peer should transition to key 2:

```yaml
spec:
  bgp:
    authentication:
      keychain:
        - sendId: 1
          recvId: 1
          algorithm: hmac sha256
          secretName: bgp-auth-keys
          secretKey: key1
        - sendId: 2
          recvId: 2
          algorithm: hmac sha256
          secretName: bgp-auth-keys
          secretKey: key2
      currentKeyId: 1
      nextKeyId: 2
```

This sets `rnext id 2` in the BIRD configuration, signaling the remote peer that it should start sending with key 2.

### Step 3: Switch the Active Sending Key

After confirming the remote peer has acknowledged the transition (e.g., by observing it sending with key 2), switch the active sending key:

```yaml
spec:
  bgp:
    authentication:
      keychain:
        - sendId: 1
          recvId: 1
          algorithm: hmac sha256
          secretName: bgp-auth-keys
          secretKey: key1
        - sendId: 2
          recvId: 2
          algorithm: hmac sha256
          secretName: bgp-auth-keys
          secretKey: key2
      currentKeyId: 2
```

Coordinate with the remote peer to also switch its active sending key to ID 2.

### Step 4: Remove the Old Key

Once both sides are sending and receiving exclusively with key 2, remove the old key:

```yaml
spec:
  bgp:
    authentication:
      keychain:
        - sendId: 2
          recvId: 2
          algorithm: hmac sha256
          secretName: bgp-auth-keys
          secretKey: key2
      currentKeyId: 2
```

Remove key 1 from the Secret as well:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: bgp-auth-keys
  namespace: meridio
type: Opaque
stringData:
  key2: "new-master-key"
```

Remove key ID 1 from the remote peer.

## Partial Resolution Failures

When the router controller cannot read a Secret referenced by a GatewayRouter's keychain (Secret does not exist, key not found in Secret data, or RBAC denies access), it **retains the previous BIRD configuration unchanged** and logs the unresolved reference at info level. It does not return an error or requeue explicitly — the controller relies on its watches on Secrets and GatewayRouters to re-trigger reconciliation once the missing data appears.

This means:

- There can be a **delay** between updating authentication configuration and the new config actually taking effect in BIRD.
- If you create a GatewayRouter referencing a Secret that does not yet exist, BGP authentication will not be configured until the Secret appears.
- If you delete a Secret that is still referenced, the **last successfully applied** BIRD configuration remains active. The BGP session continues working with the old keys.

### No Status Feedback

The router controller currently provides **no status field or condition** to indicate whether the requested authentication configuration has been successfully applied to BIRD. Multiple LB Pods each run independent router controller instances watching the same GatewayRouter — writing status from multiple writers would cause condition flapping and race conflicts.

Operators must infer success from:

- BIRD's control socket: `birdc show protocols all "NBR-<router-name>"` (look for `Established` state)
- Router controller logs: look for `"Reconciling router"` log entries (indicates config was applied) vs. `"TCP-AO secret resolution incomplete"` (indicates config was retained)
- BGP session state on the remote peer

### Avoiding Resolution Problems

To minimize the risk of partial resolution failures during key rotation:

1. **Keep keys in a single Secret** (or few Secrets) per GatewayRouter rather than one Secret per key. Kubernetes guarantees atomic updates within a single Secret — all keys appear simultaneously.
2. **Create Secrets before referencing them** in the GatewayRouter keychain. Apply the Secret first, then update the GatewayRouter.
3. **Do not delete Secrets** that are still referenced by any GatewayRouter keychain. Remove the keychain entry first, then delete the Secret.

## Supported Algorithms

The `algorithm` field accepts any value supported by BIRD (version 3.3.1+):

- `hmac md5`
- `hmac sha1`
- `hmac sha224`
- `hmac sha256`
- `hmac sha384`
- `hmac sha512`
- `cmac aes128`

Unknown values are passed through to BIRD and rejected at config load time.

## See Also

- [Router controller documentation](../controllers/router.md#bgp-authentication-tcp-ao) — implementation details, RBAC, watch strategy
- [RFC 5925 — The TCP Authentication Option](https://datatracker.ietf.org/doc/html/rfc5925)
- [BIRD 3 documentation — BGP authentication](https://bird.network.cz/?get_doc&v=30&f=bird-6.html#ss6.3)
