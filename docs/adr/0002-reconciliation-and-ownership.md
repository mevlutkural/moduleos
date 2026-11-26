# 0002: Reconciliation owns runtime mutation

Status: Accepted

## Context

Requests, runtime events and process crashes can race. Events alone are lossy;
names alone do not establish authority over resources on an operator's host.

## Decision

Persist desired intent first. Use events to enqueue, startup/periodic scans to
recover, and one deterministic, idempotent, generation-aware reconciler to
apply runtime changes. Check ownership and resource identity; never adopt or
garbage-collect by name. Persist observations with stale-generation protection.
Deletion finalizes runtime dependencies before discarding identity.

## Consequences

Convergence is asynchronous. Bounded queues, per-application serialization and
retry backoff limit pressure; dependencies can still prevent convergence.
Foreign conflicts require deliberate operator resolution. Orphan preservation
is safer than destructive automatic guessing.

## Alternatives considered

Direct handler/event mutation has multiple racing authorities. Event-only
updates miss restart gaps. Broad pruning or name-based adoption risks foreign
data. Global serialization unnecessarily blocks unrelated applications.

## References

[Architecture](../control-plane/architecture.md),
[reconciler](../../apps/control-plane/internal/reconciler/reconciler.go),
[runtime adapter tests](../../apps/control-plane/internal/swarm/docker_test.go),
[identity decision](0001-application-identity-and-mutations.md).
