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
