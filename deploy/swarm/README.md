# Single-node Swarm deployment

This is the official Genesis deployment for the ModuleOS control plane. Use a dedicated Linux host with a rootful Docker Engine, one active Swarm manager, local Unix socket `/var/run/docker.sock`, and a local filesystem suitable for SQLite WAL. Rootless Docker, user namespace remapping, remote Docker endpoints and multiple nodes are unsupported.

Required host tools: Bash 4+, Docker CLI with Stack support, `jq`, GNU `timeout`, `realpath`, `stat`, `findmnt`, `od`, `awk`, `wc`, `mktemp`, `head`, `setsid`, and `curl`. The operator needs Docker access and permission to inspect the app-data root, Docker data root and volume mountpoint. Docker's data directory is commonly restricted to root; run preflight/deploy as root through `sudo` when needed, with a private env file readable by root.

## Prepare image and external objects

Use a trusted explicitly tagged local Linux image, or a registry reference pinned by SHA-256 digest. Until a published image exists, build from the repository root using a unique tag:

```sh
revision=$(git rev-parse HEAD)
build_date=$(date -u -d "$(git show -s --format=%cI HEAD)" +%Y-%m-%dT%H:%M:%SZ)
docker buildx build --platform linux/amd64 --load \
  --build-arg VERSION=dev --build-arg "REVISION=$revision" \
  --build-arg "BUILD_DATE=$build_date" \
  -f apps/control-plane/Dockerfile -t "moduleosd:$revision" .
```

Choose the host's native platform. For a local tag, preflight inspects the image, submits its immutable local image ID to Swarm with `--resolve-image never`, and checks the running container's ID. The inspected image must remain on the single manager. Digest references are suitable for registry-backed releases. The scripts do not publish images.

The operator creates a versioned Docker Secret, external volume and canonical app-data directory. Keep a private copy of the key for API clients; do not put the key in this repository, command arguments or environment variables. For example, with a regular key file containing 32–4096 bytes:

```sh
docker secret create moduleos_api_key_v1 - < /secure/private/moduleos-api-key
docker volume create --driver local moduleos-data
sudo install -d -o 65532 -g 65532 -m 0750 /srv/moduleos-apps
```

Every parent of `/srv/moduleos-apps` must be traversable by UID 65532. The root cannot overlap Docker's data root, the database volume, `/var/lib/moduleos`, `/usr/local/bin`, protected system directories or the Docker socket directory. Production scripts only inspect these objects. A local-driver volume with remote/NFS/CIFS mount options is unsupported for SQLite WAL.

## Configure, deploy, verify and stop

Copy the example to a private file and fill every required value. `MODULEOS_PORT` selects the published host TCP port; the internal port stays 3000. Use the exact image reference and names/paths created above. The optional ingress name defaults to `moduleos-ingress`, log level to `info`, and CORS origins to empty. The env file holds the Secret name, never key bytes; it is parsed as literal `KEY=VALUE` and never executed.

```sh
cp deploy/swarm/moduleos.env.example /secure/private/moduleos.env
chmod 0600 /secure/private/moduleos.env
# Edit /secure/private/moduleos.env with your preferred editor.
sudo ./deploy/swarm/preflight.sh --env-file /secure/private/moduleos.env
sudo ./deploy/swarm/deploy.sh --env-file /secure/private/moduleos.env
```

An explicitly configured ingress must already be an attachable Swarm overlay. If the default is missing, preflight warns: management can start, but readiness for exposed apps will fail until the operator creates it. The scripts do not provision ingress.

Deployment waits for a healthy task and `GET /api/v1/live`. Check `docker service ls` and `docker service ps moduleos-system_moduleosd`. `/live` reports process liveness; `/ready` also checks operational dependencies. A failed update pauses for manual inspection. Stop-first updates briefly interrupt the management API. The scripts do not automatically roll back a potentially migrated database.

```sh
docker stack rm moduleos-system
```

Stack removal is asynchronous. The external Secret and volume remain. Back up the entire SQLite data directory, including possible WAL sidecars, before an upgrade; full rotation, backup/restore and recovery procedures belong to the later operations phase.

**Security boundary:** the API is published on host interfaces. The operator must arrange TLS termination and firewall/network restriction externally. The local Docker socket grants host-level Docker API authority even when bind-mounted read-only. Only trusted operators should control the stack and its image.
