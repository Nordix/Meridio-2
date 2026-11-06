# BGP MD5 Authentication (DEPRECATED)

> **Deprecated.** BGP MD5 ([RFC 2385](https://datatracker.ietf.org/doc/html/rfc2385))
> is a weak, legacy mechanism with known cryptographic shortcomings and **no key
> rotation**. It is superseded by TCP-AO ([RFC 5925](https://datatracker.ietf.org/doc/html/rfc5925)).
> Use MD5 only when a remote BGP peer cannot support TCP-AO. For everything
> else, use [TCP-AO](tcp-ao-key-rotation.md).

## When to use MD5

Only when the remote BGP peer (the external router) does not support TCP-AO.
If both ends can do TCP-AO, use TCP-AO: it has stronger algorithms and supports
hitless key rotation, which MD5 does not.

## Configuration

MD5 is the `type: MD5` member of `spec.bgp.authentication`. It takes a single
Secret reference and nothing else — no keychain, no key IDs, no algorithm
selection:

```yaml
spec:
  bgp:
    authentication:
      type: MD5
      md5:
        secretName: bgp-md5-key
        secretKey: key
```

The referenced Secret holds the shared key:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: bgp-md5-key
  namespace: meridio
type: Opaque
stringData:
  key: "the-shared-md5-key"
```

The same key must be configured on the remote BGP peer.

## No key rotation

Unlike TCP-AO, MD5 has **no hitless key rotation**. There is only one key, and
changing it necessarily tears down and re-establishes the BGP session. To change
an MD5 key:

1. Schedule a maintenance window (the session will bounce).
2. Update the key on the remote peer and in the Kubernetes Secret as close
   together in time as possible.
3. The BGP session drops while the two ends disagree and re-establishes once
   both sides share the new key.

If you need to rotate keys without dropping the session, migrate to TCP-AO.

## Operational notes

- **Secret handling, delays and failure behaviour are the same as TCP-AO.** If
  the Secret is missing or unreadable, the router controller retains the
  previous BIRD configuration unchanged and logs the unresolved reference at
  info level, relying on its Secret/GatewayRouter watches to re-reconcile once
  the key appears. See the equivalent notes in
  [TCP-AO key rotation](tcp-ao-key-rotation.md#partial-resolution-failures).
- **No status feedback.** As with TCP-AO, the controller writes no GatewayRouter
  status condition about authentication. Infer success from BIRD (`birdc show
  protocols`) or BGP session state.
- **Mutual exclusivity.** A GatewayRouter selects exactly one mechanism. The CRD
  rejects mixing `type: MD5` with a `tcpAo` block (or vice versa), and requires
  the field that matches the declared `type`.

## References

- [Router controller documentation](../controllers/router.md#bgp-authentication-md5--deprecated)
- [TCP-AO key rotation](tcp-ao-key-rotation.md) — the recommended mechanism
- [RFC 2385 — Protection of BGP Sessions via the TCP MD5 Signature Option](https://datatracker.ietf.org/doc/html/rfc2385)
- [RFC 5925 — The TCP Authentication Option](https://datatracker.ietf.org/doc/html/rfc5925)
