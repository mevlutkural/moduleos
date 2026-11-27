# 0004: Single-node networking and external ingress

Status: Accepted

## Context

Genesis manages local image workloads. Multi-node placement, federation,
certificate issuance and proxy lifecycle are separate product capabilities.

## Decision

Support one rootful Linux Swarm manager through a local Unix socket. Use project
overlays and explicit Project Links for shared-network/DNS reachability.
Exposed applications join the ingress overlay with labels consumed by external
Traefik. Operators supply DNS, TLS, proxy and host firewall policy.

## Consequences

There is no remote Docker transport or multi-node control-plane claim. A
Project Link is bidirectional network access, not a tenant firewall. Published
ports can bypass ingress. Readiness checks ingress only when needed, not public
DNS, certificate validity or every application's HTTP health.

## Alternatives considered

Remote Docker and multi-node placement require additional trust, lifecycle and
failure policies. Bundled proxy/DNS/TLS management expands ownership beyond the
implemented control plane. A network attachment cannot enforce one-way access.

## References

[Official stack](../../deploy/swarm/stack.yml),
[runtime spec](../../apps/control-plane/internal/swarm/spec.go),
[public contract](../../packages/api-contract/openapi.yaml),
[reconciliation ownership](0002-reconciliation-and-ownership.md).
