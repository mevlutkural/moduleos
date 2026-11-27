# 0001: Identity-bound application mutations

Status: Accepted

## Context

Names are reusable and clients can retry requests or submit stale intent.
Generation-only concurrency checks cannot distinguish same-name reincarnations.

## Decision

Application mutation ETags bind UUID and desired generation. The service/store
enforce both, including no-ops. Deploy/rollback additionally use a durable,
application-scoped idempotency ledger with fingerprinted requests and bounded
retention. HTTP acceptance is not runtime convergence.

## Consequences

Clients must retain ETags and reuse the same idempotency key for the same
request. Stale incarnations fail closed; valid no-ops avoid generation churn.
Finite replay retention is not an indefinite exactly-once promise.

## Alternatives considered

Name-only or generation-only locks permit stale-incarnation mutation. Blind
retry without durable replay can duplicate deployments. Holding HTTP requests
until convergence couples clients to dependency recovery time.

## References

[API contract](../../packages/api-contract/openapi.yaml),
[command service](../../apps/control-plane/internal/app/deployment_command.go),
[store regression tests](../../apps/control-plane/internal/store/deployment_command_test.go),
[reconciliation decision](0002-reconciliation-and-ownership.md).
