# Review guidance

- Preserve desired/observed state separation: runtime events may trigger reconciliation, but reconciliation remains the sole deterministic, idempotent, ownership-scoped, and generation-aware mutation authority.
- Preserve mutation concurrency and replay guarantees: externally reachable mutations enforce resource identity plus generation/CAS preconditions, reject stale incarnations, and retain documented no-op and idempotency behavior without unintended generation or queue movement.
- Preserve security boundaries and cleanup: never expose secrets, environment values, raw internal errors, or unsafe host paths; fail closed at public and runtime boundaries, and guarantee cancellation and cleanup for streams, readers, goroutines, timers, and runtime resources.
