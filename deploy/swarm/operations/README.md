# Short black-box operational qualification

This is Phase 8C, not the post-Phase-9 Genesis 24-hour campaign. The ordinary
Go suite exercises safe helpers; `-tags operations` additionally compiles the
real production journey. Do not invoke that journey against a development host.

## Run boundary

The full runner requires a genuinely disposable, rootful **native Linux amd64**
host with Bash 4+, Go, Docker/Buildx/Stack, GNU timeout, jq, util-linux tools and
the other prerequisites of `deploy/swarm/README.md`. It refuses desktop engines,
remote endpoints, active pre-existing Swarms and unrelated containers/services.
An acknowledgement alone is not permission to repurpose an existing Swarm.

On a fresh disposable host, from the public repository root:

```sh
evidence=$(mktemp -d)
sudo env PATH="$PATH" MODULEOS_OPERATIONS_DISPOSABLE=1 \
  MODULEOS_OPERATIONS_EVIDENCE_DIR="$evidence" bash scripts/check-operations.sh
sudo go run ./deploy/swarm/operations/cmd/summary "$evidence/results.jsonl"
```

Never use the active developer OrbStack/Swarm for this command. Safe local checks:

```sh
go test -race -count=3 ./deploy/swarm/operations ./scripts
go test -tags operations -run '^$' ./deploy/swarm/operations
go vet -tags operations ./deploy/swarm/operations/...
```

The guarded stack runner remains the sole owner of Swarm setup and teardown.
It builds the real production Dockerfile, creates an external Secret and durable
volume, and the suite calls the official deploy script. Workloads are deterministic
public test source built as A/B variants from immutable Go/distroless bases and
pushed to a run-private localhost registry. The suite verifies registry manifest
hashes, service digests and actual container image IDs. Registry and Traefik images
are pinned by immutable multi-platform digest in `fixtures_test.go`.

## Cases and evidence

| Stage | Boundary and decisive assertions |
| --- | --- |
| Boot | Public live/ready/version, denied unauthenticated mutation, project/app UUID and strong ETag, owned actual tasks |
| Lifecycle | Real update/scale/stop/start, no-op generation, stale/malformed/missing preconditions without changed intent |
| Deployment | Terminal A/B and served version, same-command replay, conflicting key, prior-success rollback, controlled task failure and subsequent recovery |
| Logs | Bounded snapshot and real run marker; three simultaneous SSE slots, fourth-stream 429, framing, client cancellation and six subsequent streams |
| Restart | New process/container, unchanged identities/config/history, external runtime drift repaired without changing durable intent |
| Ownership | Foreign-labelled, same-name fixture survives reconciliation and a real daemon restart; removal of only the test fixture permits recovery |
| Docker outage | Test-only Unix relay closes existing streams/refuses new calls; live remains 200, ready is typed 503; task/container/start/PID remain identical across outage and recovery |
| Routing | Real external Traefik returns the subject marker, disable gives 404 while canary route and direct backend stay healthy, re-enable restores the subject |
| Restore | Sole writer fully stopped; complete offline directory copied to a new independent volume; source hashes unchanged, migration version clean, integrity and foreign keys valid; IDs/ETag/intent/history and subsequent reconciliation survive |
| Deletion | API/finalizer removes actual owned resources; old incarnation's generation-1 ETag cannot stop the new generation-1 application |

The relay is mounted and its replacement task is proven ready before outage
identity is sampled. The direct socket is restored and its new task/mount verified
before closing the relay. The real host Docker daemon is never stopped or renamed.

Every probe, HTTP/SSE read and child command has a bound. The journey has a
22-minute context, a 23-minute test timeout and a 25-minute subprocess timeout.
The outer wrapper limits the entire run to 45 minutes with a separate cleanup
reservation; this is a short qualification gate, not a longevity claim.

Only sanitized `results.jsonl` is retained outside the private workspace.
Allowlisted stage/status/reason, duration and source/image identities are printed
as a human summary **and validated JSONL** in both the job log and GitHub job
summary, also on failure. Replacement diagnostics distinguish restart, ownership,
relay entry and relay exit, and report only an allowlisted last unmet condition.
Raw Docker logs, credentials, environment files and SQLite snapshots are
never uploaded. A setup/registry error is not a product pass; timeouts, product
mismatches and cleanup failure are distinct failures. Final success is emitted
only after both the journey and all resource/file cleanup succeed. Test-only
fixture resources are journaled before creation and removed by immutable identity
plus ownership; independent canaries must survive the journey.

## CI contract

`Operational qualification (amd64)` runs on its own fresh `ubuntu-24.04` runner.
Existing 8A integration, Stack smoke and native amd64/arm64 container+image scan
jobs remain intact. The existing required **Quality** context always aggregates
Source quality, Integration, Operations, both container matrix results, Deploy
assets and Stack smoke. Failure, cancellation or skipping any dependency makes
Quality fail, so the new qualification cannot silently become an optional green
skip under the current branch protection. No repository settings are changed.

A local compile or helper pass does not certify the real journey. Acceptance of
#41 still requires a successful disposable native amd64 run and native arm64 image
smoke on the exact published revision. No release, SBOM or signing is done here.
