# Control-plane architecture and boundaries

These documents describe the implemented public source, not an announcement
that Genesis has shipped. See [configuration](configuration.md),
[operations](operations.md) and [accepted decisions](../adr/README.md).

## Composition and authority

`moduleosd` is one long-running process. Its small
[main](../../apps/control-plane/cmd/moduleosd/main.go) passes the exit code from
bootstrap; [bootstrap](../../apps/control-plane/internal/bootstrap/production.go)
wires validated configuration, logging, SQLite, Docker adapter, application
services, HTTP router, reconciler and watcher. HTTP routing belongs to the API
adapter; lifecycle and reverse-order owned-resource cleanup belong to the
[daemon](../../apps/control-plane/internal/daemon/daemon.go).

The production topology is one control-plane replica on one rootful Linux
Swarm manager with a local Docker Unix socket and local persistent SQLite
volume. Workload replica scaling is independent of this **single writer**.
No federation, placement across hosts or HA control plane is implied.

The durable flow is:

1. An authenticated request validates intent and concurrency preconditions.
2. The application service commits desired state and enqueues work.
3. The reconciler reads current intent and discovers Docker state.
4. Ownership-scoped, idempotent reconciliation applies required runtime changes.
5. A generation-checked observation is persisted; stale results cannot overwrite
   a newer intent.

The [reconciler](../../apps/control-plane/internal/reconciler/reconciler.go) is
the sole runtime mutation authority for managed application intent. Handlers
and Docker events do not directly mutate application services in Docker.
The [watcher](../../apps/control-plane/internal/watcher/watcher.go) enqueues
feedback; a full scan at startup and periodically afterward recovers missed
events and restart gaps. Work is deduplicated and bounded; same-application
work is serialized. Retryable failures use bounded backoff with jitter.
There is no promise that a dependency recovers within one scan interval.

## Identity, asynchronous operations and history

Application names can be reused; UUIDs cannot. A strong ETag binds the
application UUID **and desired generation**. Mutations require `If-Match`;
missing is 428, malformed is 400, stale/wrong incarnation is 412. A valid no-op
does not increment generation or cause new reconciliation work.

Creation (201) and lifecycle/deploy acceptance (202) acknowledge durable intent,
not healthy containers or successful traffic. Poll application observations
and deployment resources. Current deployment states include `pending`,
`applying`, `succeeded`, `failed` and `superseded`; history may also contain
legacy read-only statuses. An older generation's result is not proof of the
current generation's readiness.

Deploy/rollback commands also require a 16–128 character `Idempotency-Key`
using letters, digits, `.`, `_`, `:`, `-`. The durable ledger retains replay
records for 24 hours. An identical replay returns the original acceptance
without another deployment; incompatible reuse is 409. Per-application ledger
capacity can return 429 with `Retry-After`. Use a new key only for a genuinely
new operation, not merely because the first response was lost.

Deployment resolution records an immutable image digest. Rollback creates a
new deployment from an eligible successful deployment's resolved image; it
does **not** restore application data, SQLite schema, configuration or the
ModuleOS binary. A terminal deployment failure is not an automatic new deploy.
Consult the versioned [public contract](../../packages/api-contract/openapi.yaml)
for exact requests, limits, pagination, error codes and replay semantics.

## Ownership, deletion and networking

The [runtime adapter](../../apps/control-plane/internal/swarm/docker.go)
checks managed-resource ownership and persisted identities before changing
services or networks. A matching human-readable name is not adoption authority.
Foreign collisions fail closed. Deletion is durable intent with finalization:
dependent owned resources are removed before stored identity is discarded.
The root project is protected. Orphans are not automatically adopted or pruned;
internal diagnostics exist, but there is **no public orphan diagnostics HTTP
endpoint**. Do not treat ordinary API listings as an orphan inventory.

Projects have overlay networks. Project Links attach a target application to
the source project's network with a DNS alias; shared-network reachability is
bidirectional, not a one-way firewall or tenant security boundary.

Exposed apps join the configured ingress overlay and receive routing labels
for an **external Traefik**. ModuleOS does not install or manage that proxy,
create public DNS records, issue certificates or provide arbitrary custom-domain
management. Direct published ports bypass that proxy and require separate host
firewall policy. Genesis deploys existing images; it is not a source builder,
registry, dashboard, CLI or installer.

## Persistence, health and recovery

[SQLite](../../apps/control-plane/internal/store/sqlite.go) persists projects,
applications, desired and observed state, links, deployment history and command
replay records. WAL, foreign keys and a busy timeout are configured on a single
connection. Embedded migrations run during startup; dirty or unknown schemas
prevent startup. A database directory backup is sensitive full control-plane
state, not a backup of application bind data or Docker Secrets.

Configuration, migration, database-open and listener failures prevent startup.
Temporary Docker/Swarm loss does not: the process remains live, dependency
readiness drops, watcher reconnects and reconciliation retries. Critical worker
unexpected return instead triggers controlled shutdown. An external supervisor
must restart a failed process; restarting Docker is not a ModuleOS repair step.

`GET /api/v1/live` is process liveness; `GET /api/v1/ready` checks the database,
Docker and Swarm-manager capability, plus ingress when exposed apps need it.
Neither is a complete workload health report. `SIGTERM`/`SIGINT` initiates
draining (`shutting_down`), cancellation and bounded cleanup. Clean operator
shutdown exits 0; startup/critical worker/cleanup failure exits nonzero.

Logs are bounded snapshots or finite SSE streams, not an audit/replay system.
Reconnect can duplicate backlog; `Last-Event-ID` replay is not supported. TTY
service logs are rejected. API representations redact app environment values
and bind sources, but persisted state, Docker inspect output and app stdout
can still contain secrets. See [data safety](operations.md#security-and-data-safety).

## What is actually qualified

The public [reference run](https://github.com/mevlutkural/moduleos/actions/runs/45419101924)
includes [native amd64 operational qualification](https://github.com/mevlutkural/moduleos/actions/runs/45419101924/job/138533377218)
on Ubuntu 24.04 runner image `ubuntu24/20261116.376`, rootful Docker Engine
28.0.4, one Swarm manager. The [runner manifest](https://github.com/actions/runner-images/blob/ubuntu24/20261116.376/images/ubuntu/Ubuntu2404-Readme.md)
records installed software. Preflight's Docker API minimum (1.42) is a guard,
not evidence that every Engine above that minimum has been qualified.

[Native ARM64 image smoke and scan](https://github.com/mevlutkural/moduleos/actions/runs/45419101924/job/138533377529)
is a separate boundary: it is **not** a full ARM64 Swarm operations run.
Process fakes, in-process real Docker integration, container checks and
black-box operational qualification prove different things; see
[scripts](../../scripts/README.md). They do not establish throughput, universal
hardware minimums, uptime guarantees, arbitrary Linux compatibility, release
provenance or the separate mandatory evidence-producing **24-hour Genesis
release gate after Phase 9**.
