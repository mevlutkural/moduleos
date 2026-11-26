# Architecture Decision Records

Architecture Decision Records (ADRs) capture important decisions that are costly
to reverse or that define boundaries other contributors must understand.

## When an ADR is required

Create an ADR for material changes to areas such as:

- desired-state ownership and reconciliation semantics;
- public APIs, compatibility, and versioning;
- persistence formats and migration policy;
- authentication, authorization, and trust boundaries;
- runtime adapters and workload ownership;
- clustering, membership, leader election, or placement;
- release architecture and artifact guarantees.

Routine implementation details do not need an ADR unless they establish a
lasting constraint.

## Accepted decisions

- [0001: Application identity and mutations](0001-application-identity-and-mutations.md)
- [0002: Reconciliation and runtime ownership](0002-reconciliation-and-ownership.md)
- [0003: SQLite and offline recovery](0003-sqlite-and-offline-recovery.md)
- [0004: Single-node networking and external ingress](0004-single-node-networking-and-external-ingress.md)
- [0005: Secret-backed authentication and Docker host trust](0005-secret-auth-and-host-trust.md)

These records describe existing public implementation decisions. They do not
introduce new runtime capabilities or release guarantees.

## Format

Use a four-digit sequence and a short slug:

```text
0001-example-decision.md
```

Each ADR should include:

1. Title and status (`Proposed`, `Accepted`, `Rejected`, or `Superseded`).
2. Context and constraints.
3. Decision.
4. Consequences, including risks and trade-offs.
5. Alternatives considered.
6. Links to superseded or related decisions.

Accepted ADRs are historical records. Do not rewrite an accepted decision to
change its meaning; add a new ADR that supersedes it.
