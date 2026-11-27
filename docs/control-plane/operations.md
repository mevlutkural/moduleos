# Control-plane operator runbook

Read [official deployment](../../deploy/swarm/README.md) first and keep the
[configuration reference](configuration.md) beside this runbook. Commands below
assume the **repository root**, Bash 4+, a dedicated rootful Linux single-node
Swarm manager and an exclusive maintenance window. Use the same privileged
operator shell throughout a procedure; stop on any failed guard. Do not run
these procedures against shared Docker installations as an experiment.

## Security and data safety

- Restrict the published management port with a firewall/private network and
  provide TLS termination externally. Loopback URLs below are for local
  verification; never send a bearer credential over an untrusted plaintext link.
- The Docker socket grants host-level Docker control, even mounted read-only
  and even when `moduleosd` runs non-root. Trust the image, host and operators.
- The API redacts application environment values and bind-source paths. It
  does **not** turn SQLite, backups, Docker inspection or application stdout
  into sanitized data. App environment values and paths are stored in SQLite;
  app logs can print secrets. Protect snapshots, source key files and client
  header files as credentials. Encrypt off-host backups and restrict retention.
- No shell tracing, command recording, environment credential, literal key in
  an argument, raw `docker inspect` dump or unredacted log upload. Preflight
  checks a Secret's existence/identity, not its bytes. Production fails startup
  if the configured file cannot be safely read or validated.
- A control-plane backup does not include app bind data, external databases,
  image registries or Docker Secrets. Back these up independently with their
  own consistency procedures. Preserve the image identity, public source
  revision, private operator env file and Secret name alongside recovery notes.
- Report vulnerabilities through [SECURITY.md](../../SECURITY.md), not public
  issue logs. [SUPPORT.md](../../SUPPORT.md) explains community support.

## Deploy and verify

Follow [prepare, preflight and deploy](../../deploy/swarm/README.md); no released
registry image is assumed. Build a native image from public source or use a
trusted digest-pinned release when one exists. Never scale the control plane
beyond one replica. The following setup is also used by the maintenance blocks:

```bash
set -euo pipefail
set +x
umask 077
# Run from a trusted root shell on the dedicated Linux manager.
test "$(id -u)" -eq 0
dc() { timeout 45 docker --host unix:///var/run/docker.sock "$@"; }
# A successful empty listing is different from a Docker command failure.
no_consumers() {
  local listing
  listing=$(dc ps -q --filter "volume=$1") || return 1
  [[ -z "$listing" ]]
}
setting_matches() {
  local value
  value=$(awk -F= -v key="$1" '$1 == key {print substr($0, index($0, "=")+1)}' "$3") || return 1
  [[ -n "$value" && "$value" != *$'\n'* && "$value" == "$2" ]]
}
# Never let a moved local tag turn rotation/recovery into an image upgrade.
same_image() {
  local ref resolved
  ref=$(awk -F= '$1 == "MODULEOS_IMAGE" {print substr($0, index($0, "=")+1)}' "$1") || return 1
  [[ -n "$ref" && "$ref" != *$'\n'* ]] || return 1
  if [[ "$ref" == *@sha256:* ]]; then
    resolved=$ref
  else
    resolved=$(dc image inspect --format '{{.Id}}' "$ref") || return 1
  fi
  [[ "$resolved" == "$2" ]]
}
# Check service identity, fixed mounts/Secret and actual current task image.
verify_deployment() {
  local spec cid container image_id secret_id
  spec=$(dc service inspect moduleos-system_moduleosd) || return 1
  secret_id=$(dc secret inspect --format '{{.ID}}' "$3") || return 1
  jq -e --arg id "$service_id" --arg image "$1" --arg volume "$2" --arg secret "$secret_id" '
    length == 1 and .[0].ID == $id and
    .[0].Spec.Labels["com.docker.stack.namespace"] == "moduleos-system" and
    .[0].Spec.Mode.Replicated.Replicas == 1 and
    .[0].Spec.TaskTemplate.ContainerSpec.Image == $image and
    ([.[0].Spec.TaskTemplate.ContainerSpec.Mounts[] |
      select(.Target == "/var/lib/moduleos" and .Type == "volume" and .Source == $volume)] | length == 1) and
    ([.[0].Spec.TaskTemplate.ContainerSpec.Secrets[] |
      select(.SecretID == $secret and .File.Name == "moduleos_api_key")] | length == 1)' \
    <<< "$spec" > /dev/null || return 1
  cid=$(dc ps -q --filter "label=com.docker.swarm.service.id=$service_id") || return 1
  [[ -n "$cid" && "$cid" != *$'\n'* ]] || return 1
  container=$(dc inspect "$cid") || return 1
  image_id=$(dc image inspect --format '{{.Id}}' "$1") || return 1
  jq -e --arg image "$image_id" --arg volume "$2" '
    length == 1 and .[0].State.Running and .[0].State.Health.Status == "healthy" and
    .[0].Image == $image and ([.[0].Mounts[] |
      select(.Destination == "/var/lib/moduleos" and .Type == "volume" and .Name == $volume)] | length == 1)' \
    <<< "$container" > /dev/null
}
env_file=/secure/private/moduleos.env
key_file=/secure/private/moduleos-api-key
api=http://127.0.0.1:3000/api/v1  # Replace 3000 with MODULEOS_PORT.
test -f "$env_file" && test ! -L "$env_file"
test -f "$key_file" && test ! -L "$key_file"
client_dir=$(mktemp -d /secure/private/moduleos-client.XXXXXXXX)
auth_header=$client_dir/current.header
# This example expects a generated 64-character lowercase hexadecimal key,
# without a newline. The daemon also accepts other valid file-backed keys.
test "$(wc -c < "$key_file")" -eq 64
LC_ALL=C grep -Eq '^[0-9a-f]{64}$' "$key_file"
{ printf 'Authorization: Bearer '; cat -- "$key_file"; printf '\n'; } > "$auth_header"
curl --disable --noproxy '*' --silent --show-error --fail --max-time 10 "$api/live" | jq -e '.status == "live"'
curl --disable --noproxy '*' --silent --show-error --fail --max-time 10 "$api/ready" | jq -e '.status == "ready"'
curl --disable --noproxy '*' --silent --show-error --fail --max-time 10 "$api/version" | jq -e '{version, commit, build_date}'
curl --disable --noproxy '*' --silent --show-error --fail --max-time 10 --header "@$auth_header" \
  "$api/projects" | jq -e '.projects | type == "array"'
```

The parent `/secure/private` is an operator-chosen existing physical directory,
owned by root, mode `0700`, outside this checkout and outside app-data roots.
Keep header files private; remove only the explicitly created header files
after use, then `rmdir` their exact temporary directory. Never paste a header
into a support report. A missing/invalid credential must yield 401 on protected
routes. `/live`, `/ready` and `/version` intentionally do not require a key.
Successful liveness is not successful readiness or workload convergence.

## Rotate the API key

There is **one** accepted key. A new versioned external Secret and an ordinary
stop-first deployment replace it at the fixed `/run/secrets/moduleos_api_key`
target. Plan a short management API interruption; managed workloads normally
keep running but cannot be managed through the interrupted API. There is no
hot reload, dual-key window or zero-downtime promise.

Use the privileged shell/variables from [deploy and verify](#deploy-and-verify).
First verify the current key, `/ready`, exact running image and current volume.
Save a private copy of the current env file; keep image, volume and every other
setting unchanged. Choose a **previously unused** versioned name (example v2):

```bash
data_volume=moduleos-data  # Exact current MODULEOS_DATA_VOLUME.
old_secret=moduleos_api_key_v1  # Exact current MODULEOS_API_KEY_SECRET.
setting_matches MODULEOS_DATA_VOLUME "$data_volume" "$env_file"
setting_matches MODULEOS_API_KEY_SECRET "$old_secret" "$env_file"
service_json=$(dc service inspect moduleos-system_moduleosd)
service_id=$(jq -er '.[0] | select(.Spec.Labels["com.docker.stack.namespace"] == "moduleos-system") | .ID' <<< "$service_json")
original_image=$(jq -er '.[0].Spec.TaskTemplate.ContainerSpec.Image' <<< "$service_json")
same_image "$env_file" "$original_image"
verify_deployment "$original_image" "$data_volume" "$old_secret"
new_secret=moduleos_api_key_v2
new_key_file=$client_dir/key-v2
test ! -e "$new_key_file"
od -An -N32 -tx1 /dev/urandom | tr -d ' \n' > "$new_key_file"
test "$(wc -c < "$new_key_file")" -eq 64
LC_ALL=C grep -Eq '^[0-9a-f]{64}$' "$new_key_file"
# secret create fails if that name already exists; do not replace an object.
dc secret create "$new_secret" - < "$new_key_file" > /dev/null
cp -p -- "$env_file" "$client_dir/before-rotation.env"
cp -p -- "$env_file" "$client_dir/rotation.env"
# Edit only MODULEOS_API_KEY_SECRET to moduleos_api_key_v2 in rotation.env.
# Do not put key bytes in that file. Review the private diff locally.
```

After that edit, this guard checks the only changed line, then deploys through
the official path. Do not continue if this is a different operation:

```bash
python3 -I - "$env_file" "$client_dir/rotation.env" "$new_secret" <<'PY'
import pathlib, sys
old, new = (pathlib.Path(p).read_text().splitlines() for p in sys.argv[1:3])
matches = [i for i, line in enumerate(old) if line.startswith('MODULEOS_API_KEY_SECRET=')]
assert len(matches) == 1 and len(old) == len(new), 'unexpected configuration'
expected = old.copy()
expected[matches[0]] = 'MODULEOS_API_KEY_SECRET=' + sys.argv[3]
assert expected == new, 'rotation must change only the secret name'
PY
./deploy/swarm/preflight.sh --env-file "$client_dir/rotation.env"
same_image "$client_dir/rotation.env" "$original_image"
./deploy/swarm/deploy.sh --env-file "$client_dir/rotation.env"
verify_deployment "$original_image" "$data_volume" "$new_secret"
new_header=$client_dir/new.header
{ printf 'Authorization: Bearer '; cat -- "$new_key_file"; printf '\n'; } > "$new_header"
curl --disable --noproxy '*' --silent --show-error --fail --max-time 10 "$api/ready" | jq -e '.status == "ready"'
new_status=$(curl --disable --noproxy '*' --silent --show-error --max-time 10 --output /dev/null \
  --write-out '%{http_code}' --header "@$new_header" "$api/projects")
test "$new_status" = 200
old_status=$(curl --disable --noproxy '*' --silent --show-error --max-time 10 --output /dev/null \
  --write-out '%{http_code}' --header "@$auth_header" "$api/projects")
test "$old_status" = 401
# Preserve the confirmed new configuration and client key securely.
cp -p -- "$client_dir/rotation.env" "$env_file"
auth_header=$new_header
key_file=$new_key_file
```

Update clients from the new private key file; do not discard it before client
delivery. Keep the old Secret and original env snapshot until validation is
complete. If the new task fails, preserve evidence privately. When the old key
is still trustworthy, redeploy the original env file with the **same image and
volume** to revert the credential. This is not binary/schema rollback. Do not
reuse a compromised old credential; use incident response instead. Remove old
Secrets only after checking they are no longer referenced and retention policy
allows it; there is no automatic Secret deletion here.

After successful verification, retain the new private key and env file in your
protected configuration store and update API clients. For the rest of this
shell session use the new files (`env_file=$client_dir/rotation.env`,
`key_file=$new_key_file`, `auth_header=$new_header`); do not accidentally resume
with the old canonical env file. Preserve the old files for trusted recovery
until your retention policy permits removal.

## Offline backup and same-image restore

Use an exclusive maintenance window: disable any outside automation that can
deploy/scale this control plane, deny management writes, and allow no other
writer to the state volume. Require a healthy baseline first. Restoring earlier
intent can reconcile workloads back toward that intent: plan app-data recovery
and desired-state changes together. This procedure does not restore app data.

Additional host tools: Python 3 with `sqlite3`, GNU `cp`, and enough **local**
disk for two full state copies and a validation copy. Keep the exact running
image available; use its matching public source revision for migration checks.
Do not build a different binary or run a newer/older schema against this backup.
The sample assumes the official database filename `moduleos.db`.

Throughout maintenance, nobody may retag/remove the image, replace external
objects or restart a writer. The same-image guard compares the env-file local
tag's resolved image ID to the captured immutable service reference, or requires
an exact digest reference. A mismatch is a stop condition, not permission to
upgrade. The post-deploy guard also inspects the actual task image and volume.
On failure preserve both volumes, private snapshots and notes; do not retry a
partial copy into a nonempty destination or automatically resume an old writer.

Use the shell setup above. Set `data_volume` to the exact value from the private
env file (read it, **do not source it**). Capture a private baseline of application
UUIDs, desired generations, relevant deployment history and the running image.
API redaction means that baseline is **not** a full configuration backup.

```bash
data_volume=moduleos-data  # Exact MODULEOS_DATA_VOLUME; do not guess.
current_secret=moduleos_api_key_v1  # Exact current MODULEOS_API_KEY_SECRET.
setting_matches MODULEOS_DATA_VOLUME "$data_volume" "$env_file"
setting_matches MODULEOS_API_KEY_SECRET "$current_secret" "$env_file"
service_json=$(dc service inspect moduleos-system_moduleosd)
service_id=$(jq -er '.[0] | select(.Spec.Labels["com.docker.stack.namespace"] == "moduleos-system") | .ID' <<< "$service_json")
jq -e --arg volume "$data_volume" 'length == 1 and
  (.[0].Spec.Mode.Replicated.Replicas == 1) and
  ([.[0].Spec.TaskTemplate.ContainerSpec.Mounts[] |
    select(.Target == "/var/lib/moduleos" and .Type == "volume" and .Source == $volume)] | length == 1)' \
  <<< "$service_json" > /dev/null
original_image=$(jq -er '.[0].Spec.TaskTemplate.ContainerSpec.Image' <<< "$service_json")
same_image "$env_file" "$original_image"
verify_deployment "$original_image" "$data_volume" "$current_secret"
# Save a redacted API baseline privately before stopping; /apps is bounded,
# not paginated. Deployment history is paginated per app: follow next_cursor
# until null, retaining each page privately with the matching public contract:
# GET /api/v1/apps/{name}/deployments?limit=25 then &cursor={next_cursor}.
# Collection shape: {applications: [...], total: N}; history shape:
# {deployments: [...], limit: N, next_cursor: string-or-null}.
curl --disable --noproxy '*' --silent --show-error --fail --max-time 10 --header "@$auth_header" \
  "$api/apps" > "$client_dir/before-backup-apps.json"
dc service scale --detach "$service_id=0" > /dev/null
stopped=0
for ((i=0; i<60; i++)); do
  replicas=$(dc service inspect --format '{{.Spec.Mode.Replicated.Replicas}}' "$service_id")
  running=$(dc ps -q --filter "label=com.docker.swarm.service.id=$service_id")
  consumers=$(dc ps -q --filter "volume=$data_volume")
  if [[ "$replicas" == 0 && -z "$running" && -z "$consumers" ]]; then
    stopped=1; break
  fi
  sleep 1
done
test "$stopped" = 1
volume_json=$(dc volume inspect "$data_volume")
jq -e --arg name "$data_volume" 'length == 1 and .[0].Name == $name and
  .[0].Driver == "local" and ((.[0].Options // {}) | length == 0)' \
  <<< "$volume_json" > /dev/null
source_dir=$(jq -er '.[0].Mountpoint' <<< "$volume_json")
backup_root=$(mktemp -d /secure/private/moduleos-backup.XXXXXXXX)
backup_dir=$backup_root/data
mkdir -m 0700 "$backup_dir"
# Keep this private; no full inspection output belongs in a public report.
printf '%s\n' "$volume_json" > "$backup_root/volume.json"
printf '%s\n' "$original_image" > "$backup_root/image.txt"
```

Do not infer quiescence from `/ready` alone. Confirm the manager/service remains
at zero replicas and no volume consumers exist **before every copy**; if another
actor can restart it, stop here. Do not copy a live database or delete WAL/SHM.
The copy below accepts only the current SQLite files, including any sidecars;
unexpected entries fail rather than silently leaving data behind. It checks
physical separation, regular single-link files, byte hashes and ownership/modes
independently of umask. Run it in the same root shell after the stop checks:

```bash
copy_state() {
python3 -I - "$@" <<'PY'
import hashlib, json, os, pathlib, shutil, stat, sys
src, dst, manifest = map(pathlib.Path, sys.argv[1:])
def physical(p):
    assert p.is_absolute() and p.resolve(strict=True) == p and p.is_dir(), 'unsafe directory'
physical(src); physical(dst)
assert src != dst and src not in dst.parents and dst not in src.parents, 'overlapping copy'
assert not list(dst.iterdir()), 'destination must be empty'
def snapshot(p):
    result = {}
    for f in p.iterdir():
        s = f.lstat()
        assert f.name in ('moduleos.db', 'moduleos.db-wal', 'moduleos.db-shm'), 'unexpected entry'
        assert stat.S_ISREG(s.st_mode) and s.st_nlink == 1, 'unsafe file'
        digest = hashlib.sha256()
        with f.open('rb') as stream:
            for chunk in iter(lambda: stream.read(1024 * 1024), b''):
                digest.update(chunk)
        result[f.name] = [digest.hexdigest(), s.st_uid, s.st_gid,
                          stat.S_IMODE(s.st_mode), s.st_size]
    assert 'moduleos.db' in result, 'database missing'
    return result
before = snapshot(src)
directory = src.stat()
for name in before:
    shutil.copy2(src / name, dst / name, follow_symlinks=False)
    s = (src / name).stat()
    os.chown(dst / name, s.st_uid, s.st_gid)
    os.chmod(dst / name, stat.S_IMODE(s.st_mode))
shutil.copystat(src, dst, follow_symlinks=False)
os.chown(dst, directory.st_uid, directory.st_gid)
os.chmod(dst, stat.S_IMODE(directory.st_mode))
assert snapshot(src) == before == snapshot(dst), 'copy or source changed'
assert (dst.stat().st_uid, dst.stat().st_gid, stat.S_IMODE(dst.stat().st_mode)) == (
    directory.st_uid, directory.st_gid, stat.S_IMODE(directory.st_mode)), 'directory metadata changed'
with manifest.open('x') as out:
    json.dump({'files': before, 'directory': [directory.st_uid, directory.st_gid,
                                           stat.S_IMODE(directory.st_mode)]}, out)
os.chmod(manifest, 0o600)
print('offline copy verified')
PY
}
copy_state "$source_dir" "$backup_dir" "$backup_root/manifest.json"
```

Keep that snapshot immutable and private. For restore, create a **new** local
volume with a unique operator marker; never empty or overwrite an existing
volume. Keep the original intact and stopped. Do not run two control planes
against the same state or against old/new restored intent simultaneously.

```bash
restore_marker=$(python3 -I -c 'import uuid; print(uuid.uuid4().hex)')
restore_volume=moduleos-restore-$restore_marker
existing=$(dc volume ls --format '{{.Name}}')
if grep -Fxq -- "$restore_volume" <<< "$existing"; then
  printf 'restore volume already exists; stop\n' >&2
  exit 1
fi
dc volume create --driver local --label "moduleos.operator.restore=$restore_marker" \
  "$restore_volume" > /dev/null
restore_json=$(dc volume inspect "$restore_volume")
jq -e --arg name "$restore_volume" --arg marker "$restore_marker" 'length == 1 and
  .[0].Name == $name and .[0].Driver == "local" and ((.[0].Options // {}) | length == 0) and
  .[0].Labels["moduleos.operator.restore"] == $marker' <<< "$restore_json" > /dev/null
restore_dir=$(jq -er '.[0].Mountpoint' <<< "$restore_json")
no_consumers "$restore_volume"
test "$(dc service inspect --format '{{.Spec.Mode.Replicated.Replicas}}' "$service_id")" = 0
no_consumers "$data_volume"
# Reuse the guarded copier defined above; destination must still be empty.
copy_state "$backup_dir" "$restore_dir" "$backup_root/restore-manifest.json"
test "$(dc service inspect --format '{{.Spec.Mode.Replicated.Replicas}}' "$service_id")" = 0
```

After copying, compare `manifest.json` and `restore-manifest.json` with `cmp`.
Validate a **separate scratch copy**, so opening SQLite cannot change either
the backup or the volume before deployment:

```bash
cmp -- "$backup_root/manifest.json" "$backup_root/restore-manifest.json"
validation_dir=$(mktemp -d /secure/private/moduleos-validate.XXXXXXXX)
cp -a -- "$restore_dir/." "$validation_dir/"
python3 -I - "$validation_dir/moduleos.db" apps/control-plane/internal/store/migrations <<'PY'
import pathlib, sqlite3, sys
migrations = pathlib.Path(sys.argv[2])
expected = max(int(p.name.split('_', 1)[0]) for p in migrations.glob('*.up.sql'))
db = sqlite3.connect(pathlib.Path(sys.argv[1]).as_uri() + '?mode=rw', uri=True, timeout=5)
try:
    assert db.execute('PRAGMA integrity_check').fetchall() == [('ok',)], 'integrity failed'
    assert db.execute('PRAGMA foreign_key_check').fetchall() == [], 'foreign keys failed'
    assert db.execute('SELECT version, dirty FROM schema_migrations').fetchall() == [(expected, 0)], 'schema mismatch'
finally:
    db.close()
print('integrity, foreign keys and matching-source schema verified')
PY
cp -p -- "$env_file" "$backup_root/restore.env"
# Edit only MODULEOS_DATA_VOLUME to the exact restore_volume in restore.env.
```

Validate that only the volume changed and the service still has the captured
image (do not substitute a new build). Then deploy through the official path:

```bash
python3 -I - "$env_file" "$backup_root/restore.env" "$restore_volume" <<'PY'
import pathlib, sys
old, new = (pathlib.Path(p).read_text().splitlines() for p in sys.argv[1:3])
matches = [i for i, line in enumerate(old) if line.startswith('MODULEOS_DATA_VOLUME=')]
assert len(matches) == 1 and len(old) == len(new), 'unexpected configuration'
expected = old.copy()
expected[matches[0]] = 'MODULEOS_DATA_VOLUME=' + sys.argv[3]
assert expected == new, 'restore must change only the volume name'
PY
test "$(dc service inspect --format '{{.Spec.TaskTemplate.ContainerSpec.Image}}' "$service_id")" = "$original_image"
test "$(dc service inspect --format '{{.Spec.Mode.Replicated.Replicas}}' "$service_id")" = 0
no_consumers "$data_volume"
./deploy/swarm/preflight.sh --env-file "$backup_root/restore.env"
same_image "$backup_root/restore.env" "$original_image"
./deploy/swarm/deploy.sh --env-file "$backup_root/restore.env"
verify_deployment "$original_image" "$restore_volume" "$current_secret"
curl --disable --noproxy '*' --silent --show-error --fail --max-time 10 "$api/ready" | jq -e '.status == "ready"'
curl --disable --noproxy '*' --silent --show-error --fail --max-time 10 --header "@$auth_header" \
  "$api/apps" > "$backup_root/restored-applications.json"
```

Compare UUIDs and desired generations with the private pre-backup baseline,
read the relevant deployment history using the [API contract](../../packages/api-contract/openapi.yaml),
and wait for current observed generations and expected running/stopped states.
Exercise one deliberately selected safe workload mutation with its current
ETag; verify convergence, not only 202. Check the service's actual volume/image
and ensure the original volume has no running consumers. Promote `restore.env`
to the private canonical env file only after these checks. Keep the original
volume and snapshot under retention policy; do not automatically remove them.

If a guard, copy or integrity check fails, leave the writer stopped, retain
both volumes and private evidence, and investigate. Do not force migration
versions, remove sidecars, `prune`, recursively chown arbitrary host data, or
fall back to a different binary. Reverting to the untouched original volume
with the same image is a deliberate recovery option only while its state and
app-data consistency remain acceptable; it is not automatic rollback.

## Troubleshooting and shutdown

| Observation | Interpretation and next step |
|---|---|
| 503 `docker_unavailable` | Inspect the local Unix socket and Engine availability privately; ModuleOS stays live and retries. Do not restart a shared Engine as a diagnostic experiment. |
| 503 `swarm_unavailable` | Engine is reachable but Swarm is inactive or this node is not a manager. Check the dedicated host's configured mode; do not initialize/reconfigure a shared cluster to silence readiness. |
| 503 `database_unavailable` | Check state-volume availability/space/permissions; preserve files and use offline recovery rather than editing live SQLite. |
| 503 `ingress_network_unavailable` | An exposed app needs the configured attachable overlay. Check its exact identity and external Traefik attachment, then DNS/TLS separately. |
| 503 `shutting_down` | Draining; reconnect to the replacement only after it is ready. `/live` can still be 200. |
| 503 `server_busy` | Admission is full, not a dependency diagnosis. Respect `Retry-After`; reduce load before tuning limits. |
| 202 but workload not ready | Read current desired/observed generations, `reconcile_error_code`, `reconcile_retryable` and deployment `failure_code`. Retryable dependency failures can recover; fix terminal causes and submit a deliberate new deployment with current ETag/key. Never equate old `succeeded` history with current health. |
| `/live` 200, `/ready` 503 `docker_unavailable` | Process is alive; local Docker cannot be reached. Check socket identity/group and host Engine privately. Watcher/reconciler retry; do not restart a shared Engine as a diagnostic experiment. |
| `database_unavailable` | Readiness DB probe failed. Check local disk capacity, access and storage health; protect state before intervention. |
| `swarm_unavailable` | Engine is not an available Swarm manager. Check the dedicated host's mode/manager state, not just TCP/HTTP reachability. |
| `ingress_network_unavailable` | An exposed app needs ingress. Verify the configured attachable overlay and external Traefik attachment. DNS/TLS/proxy health is not proven by readiness. |
| `shutting_down` | Admission is draining. In-flight readiness must report shutdown rather than a coincident dependency failure. |
| 503 `server_busy`, 429 | Request admission or bounded live-log/idempotency capacity. Observe `Retry-After` where supplied; do not retry in an unbounded tight loop. |
| Mutation 202, app not ready | Intent is durable, convergence pending. Inspect `desired_generation`, `observed_generation`, `observed_state`, safe error code and deployment resource. A stale observation is not current success. |
| Deployment `failed` / `superseded` | Read its safe failure code; resolve the cause or deliberately submit new intent. Replaying the old key returns the old acceptance, not a new attempt. |
| Ownership conflict / unexpected named resource | Do not adopt, relabel, delete or prune by name. Match stored UUID and exact Docker identity/ownership first; preserve foreign resources and investigate privately. |
| `bootstrap failed` / exit 1 | See [stable startup reasons](../../apps/control-plane/README.md#startup-failure-reasons); invalid config, DB/migration or listener failure prevents startup. Do not expose configured values/raw errors in reports. |

Use `dc service ps --format '{{.CurrentState}}' "$service_id"` and the public
health response for a minimal initial diagnostic. Task errors, service logs,
full inspection and database dumps are private and can expose host paths or
app secrets. Sanitize manually before sharing a bounded excerpt; no generic
public diagnostics endpoint or guaranteed automatic orphan repair exists.

Snapshot logs have line/byte/deadline bounds. Live SSE has global/per-app
capacity, finite idle/maximum lifetime and bounded entries. Clients must send
Bearer authentication (browser `EventSource` alone cannot set that header),
handle reconnects/duplicate backlog and not assume replay cursors or complete
audit delivery. TTY services are rejected. See [log limits](configuration.md#daemon-environment)
and the [contract](../../packages/api-contract/openapi.yaml).

`SIGTERM`/`SIGINT` drain admission, mark readiness not-ready, cancel workers and
streams, stop HTTP and clean owned process dependencies in reverse order under
one shutdown budget (default 15s; stack grace 30s). Clean operator shutdown
exits 0; unexpected critical worker/server failure or failed/timed-out cleanup
exits 1. Transient Docker failure alone keeps the process live/not-ready.
Swarm's restart policy supplies process supervision; this is not an in-process
restart loop for arbitrary programming faults.

`docker stack rm moduleos-system` removes the **control-plane stack**
asynchronously. It leaves external Secrets, the DB volume and managed workload
services in place. Verify old task termination before maintenance. It is not a
workload uninstaller or a backup. External Traefik, DNS and TLS remain operator
responsibilities; project network links allow bidirectional access, not a
one-way authorization policy.

## Evidence boundary

See [the exact qualified reference](architecture.md#what-is-actually-qualified).
Existing CI exercises same-image offline recovery and operational failure
scenarios, but a CI scenario is not proof that every command in a new runbook
has been executed on every host. Operators should rehearse these literal
procedures on a **disposable dedicated Linux host** before production use.
No 24-hour release qualification, universal capacity or uptime guarantee is
asserted by this runbook. Genesis requires its separate evidence-producing
24-hour release gate after Phase 9.
