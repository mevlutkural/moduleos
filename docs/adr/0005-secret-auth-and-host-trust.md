# 0005: Secret-backed authentication and Docker host trust

Status: Accepted

## Context

Management requests can change host workloads. Credential exposure and unsafe
bind mounts have consequences beyond the API process itself.

## Decision

Production requires one file-backed bearer credential; the official stack
mounts a versioned external Docker Secret at a fixed target. Validate on
startup, fail closed, and rotate by stop-first redeployment with a planned
management API interruption. Keep SQLite state outside allowed app bind roots.
Run the prepared daemon as UID/GID 65532 with dropped capabilities, but treat
access to the Docker socket as host-level authority even with a read-only bind.

## Consequences

No users/RBAC, dual credentials, hot reload or zero-downtime rotation are
provided. TLS termination and management network restriction are external.
API redaction is not at-rest encryption: database, backups, Docker environment
inspection and application logs remain sensitive. Trusted host operators and
images are part of the security boundary.

## Alternatives considered

Production direct environment keys leak into inspection/support output. Silent
fallback to no authentication is unacceptable. Non-root process identity or a
read-only socket bind alone cannot sandbox the Docker API's authority.

## References

[Key resolver](../../apps/control-plane/internal/config/api_key.go),
[auth middleware](../../apps/control-plane/internal/api/middleware/auth.go),
[rotation and data safety](../control-plane/operations.md),
[security reporting](../../SECURITY.md),
[storage decision](0003-sqlite-and-offline-recovery.md).
