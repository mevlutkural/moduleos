# ModuleOS Control Plane

The control plane is the long-running ModuleOS backend. It owns application use
cases, desired state, reconciliation, runtime adapters, persistence, and the
public API transport.

The HTTP API is an adapter within this application; it is not a separate
application or the identity of the control plane.

## Development

Run the control-plane test suite from the repository root:

```sh
go test ./apps/control-plane/...
./scripts/check-coverage.sh
```

The production daemon built from this application is named `moduleosd`.

## Runtime-log limits

Runtime log snapshots and live SSE sessions are bounded control-plane
operations. The relevant environment settings are:

| Variable | Default | Purpose |
|---|---:|---|
| `MAX_LOG_TAIL` | `1000` | Maximum accepted `tail` line count. |
| `MAX_LOG_PAYLOAD_BYTES` | `1048576` | Snapshot aggregate and per-stream-entry byte ceiling. |
| `MAX_SSE_CONNECTIONS` | `20` | Global live-log session limit. |
| `MAX_SSE_CONNECTIONS_PER_APP` | `3` | Per-application live-log session limit. |
| `LOG_TAIL_TIMEOUT` | `10s` | Snapshot open/read deadline. |
| `SSE_OPEN_TIMEOUT` | `10s` | Live Docker stream open deadline. |
| `SSE_HEARTBEAT_INTERVAL` | `15s` | Keepalive comment interval. |
| `SSE_IDLE_TIMEOUT` | `5m` | Maximum interval without an application log entry. |
| `SSE_MAX_DURATION` | `30m` | Absolute live-session lifetime. |
| `SSE_INITIAL_TAIL` | `50` | Default initial backlog for a live session. |

All values are validated at startup. Live sessions are intentionally finite;
clients reconnect after the maximum duration and may receive duplicate backlog
lines because runtime logs have no replay cursor.

TTY-enabled Docker services are rejected by the log API. Docker does not frame
daemon-side log errors separately for TTY streams, so forwarding those streams
could expose runtime internals as if they were application output.
