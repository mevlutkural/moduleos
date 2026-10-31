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

Build it from the repository root:

```sh
CGO_ENABLED=0 go build -trimpath -o ./moduleosd ./apps/control-plane/cmd/moduleosd
```

Run the executable-level lifecycle qualification and static Linux build checks:

```sh
go test -count=1 -v -timeout=3m ./apps/control-plane/cmd/moduleosd
./scripts/check-moduleosd-build.sh
```

The process suite requires a Unix-like host. It launches the real production
binary with isolated temporary listeners, SQLite databases, and Docker Unix
sockets. Its test-only Docker HTTP server proves process startup, readiness,
signal, in-flight readiness cancellation, stream-cancellation, exit-code, and
cleanup behavior; it does not
claim Docker Engine, Swarm, or Traefik compatibility. Real runtime integration
and the evidence-producing 24-hour Genesis qualification remain separate later
release gates. Database and Docker cleanup registration and reverse-order
execution are verified by the bootstrap and daemon component suites; process
exit alone is not treated as proof that an individual cleanup callback ran.

## Real Docker and Swarm integration

The integration qualification runs the public store, application service,
reconciler, watcher, and Docker adapter against a real Docker Engine and a real
single-node Swarm manager. It exercises canonical runtime specs, convergence,
event and periodic recovery, ownership conflicts, orphan preservation, Project
Links and Swarm DNS, and selected SQLite-backed crash windows.

The suite is destructive and must run only against an empty, disposable
single-node Swarm. It refuses to run without explicit acknowledgement and
fails rather than skipping missing prerequisites:

```sh
MODULEOS_INTEGRATION_DISPOSABLE=1 ./scripts/check-swarm-integration.sh
```

If Swarm is inactive, the runner creates a one-node manager and removes only
that script-owned Swarm afterward. If Swarm is already active, it must already
be a disposable one-node manager and the runner leaves it active. The suite
uses immutable multi-architecture images, tracks exact resource identities,
and fails if its final service/network snapshot differs from its baseline.

This qualification proves the in-process control-plane integration boundary;
it does not launch the public HTTP daemon, qualify Traefik or operator flows,
make performance claims, or replace the separate evidence-producing 24-hour
Genesis release qualification.

## Production container image

`apps/control-plane/Dockerfile` builds the production image from the
repository root. Its build context is limited by
`apps/control-plane/Dockerfile.dockerignore`:

```sh
docker buildx build -f apps/control-plane/Dockerfile \
  --build-arg VERSION=dev \
  --build-arg REVISION="$(git rev-parse HEAD)" \
  --build-arg BUILD_DATE=2026-01-01T00:00:00Z \
  --tag moduleosd:dev --load .
```

The image contains static `moduleosd` and `moduleosd-container` binaries on a
digest-pinned distroless base. It has no shell or package manager. The same
Dockerfile builds `linux/amd64` and `linux/arm64`.

The entrypoint, `moduleosd-container start`, briefly runs as root. It makes
only these changes:

- It changes the owner of `/var/lib/moduleos` and of the exact SQLite
  database, `-wal`, and `-shm` files named by `DATABASE_PATH` to
  `65532:65532`. Nothing else changes, and nothing changes recursively.
- It adds the configured Docker socket's group as a supplementary group.

It then drops to `65532:65532`, sets `no_new_privs`, and replaces itself with
`moduleosd`, which runs as PID 1 and receives signals directly.

The entrypoint refuses to start and logs one stable error code in these
cases:

- `DATABASE_PATH` is not directly inside `/var/lib/moduleos`;
- the data directory is a symlink, or root cannot search it (with a reduced
  capability set, keep `CHOWN`, `SETUID`, `SETGID`, and `DAC_READ_SEARCH`);
- a SQLite file is a symlink, hard link, or special file;
- the Docker socket is not a socket or is owned by group `0`.

A missing Docker socket is not fatal. The daemon stays live and `/ready`
reports Docker as unavailable.

The image healthcheck calls `moduleosd-container healthcheck`. It drops to
`65532:65532` first, then probes `/api/v1/live` only. A Docker outage
therefore never makes the container unhealthy.

`BUILD_DATE` is required at build time and must be an RFC 3339 UTC timestamp.

The image defaults to `ENV=production`, `LISTEN_ADDRESS=0.0.0.0`, `PORT=3000`,
and `DATABASE_PATH=/var/lib/moduleos/moduleos.db`. Production still requires
`API_KEY_FILE` and an explicit `BASE_DOMAIN`. The image declares no volume:
mount a named volume at `/var/lib/moduleos` to persist the database.

Mounting the Docker socket gives the daemon host-level control of Docker,
whatever the mount mode and even though the process is non-root.

Qualify the image with:

```sh
./scripts/check-container-image.sh
```

The script builds the image for the Docker Engine's native architecture,
checks that the other architecture also builds, and runs the container suite.

The suite creates only run-labelled containers and volumes and removes exactly
those. It never mounts the host Docker socket and does not touch Swarm,
services, or networks. It proves:

- metadata;
- the fixed non-root identity, `no_new_privs`, and an empty capability set;
- the socket group;
- liveness without Docker;
- graceful `SIGTERM` and `SIGINT` shutdown;
- persistence across container replacement;
- adoption of root-owned restored data;
- fail-closed preparation;
- read-only root filesystem operation;
- non-root starts.

For a local development run, make the data root and a separate database
directory available and start the daemon with development defaults:

```sh
mkdir -p /tmp/moduleos-data /tmp/moduleos-state
ALLOWED_MOUNT_ROOTS=/tmp/moduleos-data \
DATABASE_PATH=/tmp/moduleos-state/moduleos.db \
go run ./apps/control-plane/cmd/moduleosd
```

The directory that holds `DATABASE_PATH` and its `-wal` and `-shm` files
must stay separate from every `ALLOWED_MOUNT_ROOTS` entry. Startup fails when
an allowed root equals, contains, or lies inside it, in either lexical or
symlink-resolved form. Application bind mounts that equal, contain, or lie
inside that directory are rejected. The database file and its `-wal` and
`-shm` files must not be symbolic links: SQLite follows a link and keeps its
sidecars beside the target, so startup fails if any of them is one.

Genesis controls the Docker Engine on the same host through a local Unix
socket. The default endpoint is `unix:///var/run/docker.sock`; containers must
mount that socket at the configured path. Remote Docker transports are not
supported.

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
