# ADR-003: Defer CRD status population, keep status subresources additive

## Status

Accepted

## Date

2026-10-09

## Context

Several v1alpha1 CRDs advertised a `status.conditions` list in their schema that no
controller populates (GatewayRouter, GatewayConfiguration), and L34Route embedded the
Gateway API `RouteStatus` shape without a controller writing it. Issue #272 flagged this:
advertising status that nothing sets implies a contract the implementation does not honour.

The Kubernetes API conventions
([sig-architecture/api-conventions.md](https://github.com/kubernetes/community/blob/main/contributors/devel/sig-architecture/api-conventions.md),
"Spec and Status" / "Typical status properties") treat status as controller-written
observed state and advise controllers to populate conditions once they reconcile a
resource. They do not call for advertising conditions ahead of a writer.

This ADR records the status-shape decision made in PR #269. It does not take a position on
top-level vs per-condition `observedGeneration`; that was not decided in #269 and is out of
scope here.

## Decision

- **GatewayRouter, GatewayConfiguration, L34Route**: leave `status` empty for now, since no
  controller writes it yet. The `/status` subresource stays enabled on each so a status
  shape can be reintroduced later as an additive, optional change within v1alpha1 —
  alongside the controller that populates it — with no API version bump and no impact on
  existing stored objects.
- **L34Route**: the intended direction, when route status is first reported, is to adopt the
  Gateway API `RouteStatus` shape (per-parent status, like HTTPRoute/GRPCRoute), keeping it
  aligned with the upstream Route model it is based on.
- **LoadBalancerEndpointSlice**: intentionally has no `/status` subresource, mirroring
  upstream `discovery/v1.EndpointSlice`. A single writer (the DG controller) produces the
  whole object and many, potentially ephemeral, consumers read it; per-endpoint state is
  carried inline on each endpoint rather than in an object-level status. This rationale is
  captured in the type's doc comment.

## Consequences

### Positive

- Status schema reflects what controllers actually write; no unhonoured contract advertised.
- Reintroducing status later is additive and backward-compatible: existing stored objects
  stay valid, no API version bump required, and the status subresource is already enabled.
- L34Route's future status direction is recorded, keeping it aligned with Gateway API.

### Negative

- Clients inspecting these resources see no status conditions until the respective
  controllers begin writing them.

## References

- Issue #272
- PR #269
- Kubernetes API conventions: https://github.com/kubernetes/community/blob/main/contributors/devel/sig-architecture/api-conventions.md
