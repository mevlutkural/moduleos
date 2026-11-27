# Control-plane configuration

Configuration is read and validated **once, before starting the database, HTTP
listener or workers**. Invalid input prevents startup. There is no hot reload.
Unset values use daemon defaults below; explicitly empty values are rejected
except `CORS_ALLOWED_ORIGINS`, `DOCKER_SOCKET` and development `API_KEY`.
Production never allows `API_KEY`, even when empty. Unknown ambient variables
are not a supported configuration interface.

The three layers below are different: daemon environment, production image/stack
overrides, and the literal operator env file. Adding daemon variables to the
operator file does not tune the official stack: its parser rejects unknown keys.

## Daemon environment

Durations use Go duration syntax, for example `500ms`, `15s`, `5m`. Byte counts
and integer limits are decimal. Defaults are not capacity or performance claims.

| Variable | Daemon default | Meaning and validation |
|---|---|---|
| `ENV` | `development` | `development` or `production`. Use production for deployment. |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error`. Treat operational logs as private. |
| `LISTEN_ADDRESS` | `127.0.0.1` | IP literal (including IPv6) or `localhost`; not an arbitrary hostname. |
| `PORT` | `3000` | HTTP listener port, integer 1–65535. Not the stack's host publication port. |
| `DOCKER_ENDPOINT` | `unix:///var/run/docker.sock` | Canonical absolute local socket path or `unix://` plus that path. No remote transports, URL authority, query, fragment, encoded path, whitespace, dot segments or root path. |
| `DOCKER_SOCKET` | empty | Compatibility alias. A nonempty alias overrides an **unset** endpoint. If both are supplied, their canonical endpoint identities must match; this is not filesystem symlink resolution. Prefer `DOCKER_ENDPOINT`. |
| `BASE_DOMAIN` | `moduleos.local` | Explicitly required in production. Lowercase DNS-style name containing a dot, maximum 253 characters. Used for workload routing labels; does not create DNS/TLS. |
| `DATABASE_PATH` | `./moduleos.db` | Explicitly required in production. Nonempty path with an existing writable parent; production rejects memory/URI/query/control forms. Development may use `:memory:` for tests, not persistence. Filesystem database and WAL/SHM paths must not be symlinks. Database directory and allowed mount roots must not overlap, lexically or after symlink resolution. Image imposes a stricter path below. |
| `API_KEY` | empty | Development-only direct credential; empty means no development authentication. Whitespace/control characters are invalid. **Forbidden in production**, including an explicitly empty value. Never set together with `API_KEY_FILE`. |
| `API_KEY_FILE` | unset | Required in production; optional in development. Regular readable file, not a final-component symlink; safe open and inode identity checks. See [credential rules](#credential-rules). No fallback on failure. |
| `INGRESS_NETWORK` | `moduleos-ingress` | Runtime network name, 1–63 lowercase letters/digits/underscore/hyphen, starting/ending with letter/digit. Required for exposed workloads, not every readiness probe. |
| `CORS_ALLOWED_ORIGINS` | empty list | Comma-separated exact HTTP(S) origins, no wildcard, credentials, path, query, fragment or IPv6 zone. Ports 1–65535. Trims entries, normalizes scheme/host/default ports, sorts and deduplicates. Empty disables cross-origin grants; not an authentication substitute. |
| `ALLOWED_MOUNT_ROOTS` | `/srv/moduleos-data` | Nonempty comma-separated canonical absolute non-root paths. No duplicate/overlapping roots. Must stay separate from SQLite state. Bind sources must already exist, resolve under an allowed root and pass additional protected-path/socket and cross-application overlap checks. Never use a broad system directory. |
| `RECONCILE_INTERVAL` | `30s` | Positive periodic full-scan interval; events can enqueue sooner. |
| `RECONCILE_MAX_BACKOFF` | `30s` | At least `1s`; cap on exponential retry base. Jitter can add up to roughly 25%; this is not a maximum end-to-end recovery time. |
| `RECONCILE_WORKERS` | `4` | 1–32 workers; work for an application is serialized. |
| `DEPLOYMENT_TIMEOUT` | `5m` | Positive deployment convergence deadline, not HTTP request time. |
| `STABILIZATION_WINDOW` | `10s` | Nonnegative runtime stabilization interval. |
| `SHUTDOWN_TIMEOUT` | `15s` | Positive shared budget for orderly shutdown or startup rollback. Allow additional container stop grace. |
| `READINESS_TIMEOUT` | `2s` | Positive, at most `5s`, bounds dependency probes. |
| `HTTP_READ_TIMEOUT` | `10s` | Positive HTTP read timeout. |
| `HTTP_WRITE_TIMEOUT` | `30s` | Positive HTTP write timeout; live SSE has its own bounded lifecycle. |
| `HTTP_IDLE_TIMEOUT` | `60s` | Positive HTTP idle timeout. |
| `HTTP_BODY_LIMIT` | `1048576` | 1024–10485760 bytes, request body ceiling. |
| `MAX_REPLICAS` | `20` | 1–1000, workload scale ceiling, not control-plane replicas. Creation requires at least one replica; scale accepts zero. |
| `MAX_APPLICATIONS` | `100` | 1–10000 applications. |
| `MAX_PROJECTS` | `20` | 2–1000 projects, including the protected root project. |
| `MAX_LOG_TAIL` | `1000` | 100–10000 lines, accepted log-tail ceiling. |
| `MAX_LOG_PAYLOAD_BYTES` | `1048576` | 1024–16777216 bytes, snapshot aggregate and live-entry ceiling. |
| `MAX_SSE_CONNECTIONS` | `20` | 1–1024 simultaneous live-log sessions. |
| `MAX_SSE_CONNECTIONS_PER_APP` | `3` | At least 1, no greater than global session limit. |
| `LOG_TAIL_TIMEOUT` | `10s` | Positive, at most `1m`, snapshot open/read deadline. |
| `SSE_OPEN_TIMEOUT` | `10s` | Positive, at most `1m`, live source open deadline. |
| `SSE_HEARTBEAT_INTERVAL` | `15s` | Positive; strictly less than idle timeout. Heartbeats do not count as application output. |
| `SSE_IDLE_TIMEOUT` | `5m` | Positive; strictly between heartbeat interval and maximum duration. |
| `SSE_MAX_DURATION` | `30m` | Positive absolute live-session lifetime. |
| `SSE_INITIAL_TAIL` | `50` | 1–`MAX_LOG_TAIL`, default live-session backlog. |
| `REQUEST_CONCURRENCY` | `128` | 1–65536 admitted requests; overload returns 503 `server_busy`. |

### Credential rules

`API_KEY_FILE` is limited to **4096 file bytes**, including any terminator.
Exactly one trailing LF or CRLF is removed. The remaining credential must be at
least 32 bytes, with no whitespace or control characters. Empty, oversized,
unreadable, changed-during-open and unsafe file inputs fail startup. The loader
does not enforce a particular Unix permission mode: operators must protect the
source file; the stack mounts its Secret as UID/GID 65532, mode `0400`.

Production has one bearer credential with management authority, not users,
roles, sessions or a secret vault. Rotation is a stop-first redeployment, not a
reload. See the [rotation procedure](operations.md#rotate-the-api-key).

## Production image and official stack

The [Dockerfile](../../apps/control-plane/Dockerfile) sets `ENV=production`,
`LISTEN_ADDRESS=0.0.0.0`, `PORT=3000`, and
`DATABASE_PATH=/var/lib/moduleos/moduleos.db`. The entrypoint requires the
database directly inside `/var/lib/moduleos`; persist that directory in a
named local volume. See [image preparation](../../apps/control-plane/README.md#production-container-image)
for UID/GID 65532, exact-file ownership repair and the Docker socket group.

The [official stack](../../deploy/swarm/stack.yml) additionally fixes
`API_KEY_FILE=/run/secrets/moduleos_api_key`,
`DOCKER_ENDPOINT=unix:///var/run/docker.sock` and `SHUTDOWN_TIMEOUT=15s`.
It maps base domain, mount root, ingress, logging and CORS from the operator file.
Other settings retain image/daemon defaults. It runs **one** control-plane
replica on the manager, uses stop-first updates and 30-second container stop
grace, a read-only root filesystem, and bounded Docker JSON logs (10 MiB × 5).
The healthcheck probes liveness, not Docker availability.

## Operator env file

Use [moduleos.env.example](../../deploy/swarm/moduleos.env.example) with
[preflight and deploy](../../deploy/swarm/README.md). This is **not a shell
script**: never `source` it. Only the following keys are accepted.

| Variable | Default / requirement | Meaning and checks |
|---|---|---|
| `MODULEOS_IMAGE` | required | Trusted explicit non-`latest` local Linux image tag, or registry reference pinned by SHA-256 digest. Local tags are resolved to the inspected local immutable image ID; keep that image on this host. No bare image ID input. |
| `MODULEOS_PORT` | required | Published host port 1–65535; internal port remains 3000. Restrict access externally. |
| `MODULEOS_BASE_DOMAIN` | required | Explicit base domain; daemon validates its DNS-style form. DNS/TLS are external. |
| `MODULEOS_API_KEY_SECRET` | required | Existing versioned external Secret name ending `_v` plus digits, e.g. `moduleos_api_key_v1`, maximum 63 characters. Preflight checks object identity, **not secret content**. |
| `MODULEOS_DATA_VOLUME` | required | Existing named local volume, no driver mount options; local filesystem suitable for SQLite WAL. Keep it independent of app data. |
| `MODULEOS_APP_DATA_ROOT` | required | Existing canonical physical absolute app-data directory, no commas/trailing slash; UID 65532 must traverse every parent. Must not overlap protected paths, socket directory, Docker data or control-plane volume. Scripts do not create it. |
| `MODULEOS_INGRESS_NETWORK` | `moduleos-ingress` | Explicit value must exist as an attachable overlay. Missing default warns; readiness then fails when an exposed workload needs it. |
| `MODULEOS_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error`. |
| `MODULEOS_CORS_ALLOWED_ORIGINS` | empty | Same origin rules as daemon CORS configuration. |

Grammar is literal `KEY=VALUE`, with blank/comment lines allowed, regular
non-symlink file at most 64 KiB and lines at most 4096 bytes. No duplicates,
unknown keys, quoting, expansions (`$`, backticks), CR/control characters or
leading/trailing value whitespace. Keep it private (`0600`); it contains object
names and paths but never credential bytes. Do not use `API_KEY` in this file.

## Build and test inputs are not runtime settings

Docker build arguments `TARGETOS`, `TARGETARCH`, `VERSION`, `REVISION` and
`BUILD_DATE` select platform and embedded identity. `BUILD_DATE` must be an RFC
3339 UTC timestamp. The image is built for Linux; use the host's native
architecture for execution. Build args do not belong in the operator env file.

Qualification variables such as `MODULEOS_INTEGRATION_DISPOSABLE` and
`MODULEOS_OPERATIONS_DISPOSABLE` authorize destructive **test fixtures**, not
production settings. See [script boundaries](../../scripts/README.md). Never
set these on a shared production host to bypass a test guard.

Source of truth: [config loader](../../apps/control-plane/internal/config/config.go),
[key resolver](../../apps/control-plane/internal/config/api_key.go),
[deployment parser/preflight](../../deploy/swarm/lib.sh).
