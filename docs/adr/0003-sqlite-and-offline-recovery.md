# 0003: Single-writer SQLite and offline recovery

Status: Accepted

## Context

Genesis needs durable intent without a separate database service. SQLite WAL
requires suitable local storage; copying only the main file can lose committed
state still present in sidecars.

## Decision

Run one control-plane writer with SQLite WAL, foreign keys, a busy timeout and
embedded startup migrations. Keep the entire state directory separate from app
bind roots. Back up offline after proving the writer stopped; restore the full
directory to a separate empty local volume, retaining the original. Validate
integrity, foreign keys and the migration state against the matching public
source. Recover with the same image, not an assumed schema downgrade.

## Consequences

Management maintenance has downtime. Snapshots contain sensitive app state and
must be protected. This is not control-plane HA, distributed storage or an app
data/Secret backup. Unsupported/dirty schemas stop startup; do not force schema
versions to make an old binary run.

## Alternatives considered

A distributed database adds an unsupported operational dependency. Live file
copy risks inconsistent WAL state. In-place restore destroys the fallback.
Automatic binary rollback cannot undo already applied schema migrations.

## References

[Store](../../apps/control-plane/internal/store/sqlite.go),
[migration tests](../../apps/control-plane/internal/store/sqlite_migration_test.go),
[offline procedure](../control-plane/operations.md#offline-backup-and-same-image-restore),
[host trust](0005-secret-auth-and-host-trust.md).
