#!/usr/bin/env bash
set -Eeuo pipefail

[[ ${MODULEOS_STACK_DISPOSABLE:-} == 1 ]] || { echo 'Stack smoke requires MODULEOS_STACK_DISPOSABLE=1' >&2; exit 2; }
[[ $(uname -s) == Linux && $(uname -m) == x86_64 ]] || { echo 'Stack smoke requires disposable Linux amd64' >&2; exit 2; }
((EUID == 0)) || { echo 'Stack smoke requires root to inspect Docker volume and process metadata' >&2; exit 2; }
[[ -S /var/run/docker.sock ]] || { echo 'Local Docker socket required' >&2; exit 2; }
((BASH_VERSINFO[0] >= 4)) || { echo 'Bash 4+ required' >&2; exit 2; }
for tool in docker go jq timeout realpath stat findmnt curl od ip setsid; do command -v "$tool" >/dev/null || { echo "Missing tool: $tool" >&2; exit 2; }; done
repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"
cleanup_active=0
cleanup_deadline=0
suite_pid=''
active_command_pid=''
capture_file=$(mktemp /tmp/moduleos-stack-capture.XXXXXX)
stop_active_command() {
  local i
  [[ -n "$active_command_pid" ]] || return 0
  kill -TERM -- "-$active_command_pid" 2>/dev/null || true
  for ((i=0;i<6;i++)); do
    kill -0 -- "-$active_command_pid" 2>/dev/null || break
    sleep 0.5
  done
  kill -KILL -- "-$active_command_pid" 2>/dev/null || true
  if [[ -r /proc/$active_command_pid/stat && $(awk '{print $3}' "/proc/$active_command_pid/stat" 2>/dev/null) == D ]]; then
    echo 'Docker child is uninterruptible' >&2
    return 1
  fi
  wait "$active_command_pid" 2>/dev/null || true
  active_command_pid=''
}
early_cleanup() {
  local result=$?
  trap - EXIT
  trap '' INT TERM
  stop_active_command || result=1
  rm -- "$capture_file" || result=1
  exit "$result"
}
trap early_cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
run_docker_bounded() {
  local duration=$1 kill_after=$2 status=0
  shift 2
  env -u DOCKER_HOST -u DOCKER_CONTEXT -u DOCKER_TLS_VERIFY -u DOCKER_CERT_PATH \
    setsid timeout --signal=TERM --kill-after="$kill_after" "$duration" docker --host unix:///var/run/docker.sock "$@" <&0 &
  active_command_pid=$!
  wait "$active_command_pid" || status=$?
  active_command_pid=''
  return "$status"
}
docker_cmd() {
  local limit=10 remaining
  if ((cleanup_active)); then
    remaining=$((cleanup_deadline-SECONDS))
    ((remaining > 0)) || return 124
    ((remaining < limit)) && limit=$remaining
  fi
  run_docker_bounded "${limit}s" 2s "$@"
}
docker_build() {
  run_docker_bounded 600s 5s "$@"
}
capture_docker_cmd() {
  local destination=$1 limit=$2 size
  shift 2
  : > "$capture_file"
  docker_cmd "$@" > "$capture_file" || return $?
  size=$(wc -c < "$capture_file") || return 1
  ((size <= limit)) || return 1
  printf -v "$destination" '%s' "$(<"$capture_file")"
}
remove_network_bounded() {
  local id=$1 attempt
  for ((attempt=0;attempt<4 && SECONDS<cleanup_deadline;attempt++)); do
    if docker_cmd network rm "$id" >/dev/null 2> "$work_dir/network-remove.err"; then return 0; fi
    [[ $(<"$work_dir/network-remove.err") == *'active endpoints'* ]] || return 1
    sleep 1
  done
  return 1
}
capture_docker_cmd info 65536 info --format '{{json .}}'
[[ $(jq -r '.OSType' <<< "$info") == linux ]] || { echo 'Linux Docker server required' >&2; exit 2; }
security_options=$(jq -r '(.SecurityOptions // []) | join(" ")' <<< "$info")
[[ $security_options != *rootless* && $security_options != *userns* ]] || { echo 'Rootful Docker without userns-remap required' >&2; exit 2; }
docker_cmd buildx version >/dev/null || { echo 'Docker buildx required' >&2; exit 2; }
os_name=$(jq -r '.OperatingSystem' <<< "$info")
[[ $os_name != *OrbStack* && $os_name != *Docker\ Desktop* ]] || { echo 'Refusing desktop Docker' >&2; exit 2; }
[[ $(jq -r '.Swarm.LocalNodeState' <<< "$info") == inactive ]] || { echo 'A fresh disposable daemon with inactive Swarm is required' >&2; exit 2; }
capture_docker_cmd existing_containers 8192 container ls -aq
capture_docker_cmd existing_services 8192 service ls -q 2>/dev/null || existing_services=''
[[ -z "$existing_containers" && -z "$existing_services" ]] || { echo 'Refusing nonempty daemon' >&2; exit 2; }
capture_docker_cmd existing_networks 8192 network ls --filter name=moduleos-root-net -q
[[ -z "$existing_networks" ]] || { echo 'Refusing preexisting ModuleOS network' >&2; exit 2; }
capture_docker_cmd existing_networks 8192 network ls --filter name=moduleos-system_default -q
[[ -z "$existing_networks" ]] || { echo 'Refusing preexisting stack network' >&2; exit 2; }
run_id="$(od -An -N6 -tx1 /dev/urandom | tr -d ' \n')"
[[ $run_id =~ ^[a-f0-9]{12}$ ]] || exit 2
port=$((18000 + 16#${run_id:0:4} % 20000))
image="localhost:5000/moduleos-stack-smoke:${run_id}"
bad_image="localhost:5000/moduleos-stack-smoke:${run_id}-unhealthy"
updated_image="localhost:5000/moduleos-stack-smoke:${run_id}-updated"
image_id=''
root_network_id=''
canary_network="moduleos-canary-net-${run_id}"
canary_volume="moduleos-canary-volume-${run_id}"
canary_secret="moduleos_canary_${run_id}_v1"
canary_network_id=''
canary_secret_id=''
canary_volume_created=0
canary_volume_identity=''
secret="moduleos_smoke_${run_id}_v1"
volume="moduleos-stack-smoke-${run_id}"
secret_id=''
volume_identity=''
work_dir="$(mktemp -d /tmp/moduleos-stack-smoke.XXXXXX)"
app_root="$(mktemp -d /tmp/moduleos-apps.XXXXXX)"
chmod 0755 "$app_root"
swarm_owned=0
service_id=''
app_service_id=''
app_network_id=''
default_network_id=''
cleanup() {
  local result=$? remaining inspect_status owned_containers
  trap - EXIT
  trap '' INT TERM
  cleanup_active=1
  cleanup_deadline=$((SECONDS + 180))
  stop_active_command || result=1
  if [[ -n "$suite_pid" ]]; then
    kill -TERM -- "-$suite_pid" 2>/dev/null || true
    for ((i=0;i<6;i++)); do
      kill -0 -- "-$suite_pid" 2>/dev/null || break
      sleep 0.5
    done
    if kill -0 -- "-$suite_pid" 2>/dev/null; then
      kill -KILL -- "-$suite_pid" 2>/dev/null || true
      for ((i=0;i<4;i++)); do
        kill -0 -- "-$suite_pid" 2>/dev/null || break
        sleep 0.5
      done
    fi
    if kill -0 -- "-$suite_pid" 2>/dev/null; then
      echo 'Test process group did not terminate before cleanup' >&2
      result=1
    else
      wait "$suite_pid" 2>/dev/null || true
    fi
  fi
  if [[ -f "$work_dir/service-id" ]]; then
    service_id="$(cat "$work_dir/service-id")"
  fi
  [[ ! -f "$work_dir/app-service-id" ]] || app_service_id="$(cat "$work_dir/app-service-id")"
  [[ ! -f "$work_dir/app-network-id" ]] || app_network_id="$(cat "$work_dir/app-network-id")"
  [[ ! -f "$work_dir/root-network-id" ]] || root_network_id="$(cat "$work_dir/root-network-id")"
  [[ ! -f "$work_dir/default-network-id" ]] || default_network_id="$(cat "$work_dir/default-network-id")"
  if [[ -z "$service_id" && "$swarm_owned" == 1 ]]; then
    candidate=$(docker_cmd service inspect --format '{{.ID}} {{.Spec.TaskTemplate.ContainerSpec.Image}} {{index .Spec.Labels "com.docker.stack.namespace"}}' moduleos-system_moduleosd 2>/dev/null || true)
    if [[ "$candidate" == *" $image moduleos-system" || "$candidate" == *" $bad_image moduleos-system" || "$candidate" == *" $updated_image moduleos-system" || ( -n "$image_id" && "$candidate" == *" $image_id moduleos-system" ) ]]; then
      service_id=${candidate%% *}
    fi
  fi
  if [[ -n "$service_id" ]]; then
    current=$(docker_cmd service inspect --format '{{.ID}}' moduleos-system_moduleosd 2>/dev/null || true)
    if [[ "$current" == "$service_id" ]]; then
      docker_cmd stack rm moduleos-system >/dev/null 2>&1 || result=1
    elif [[ -n "$current" ]]; then
      echo 'Refusing to remove a replaced service' >&2; result=1
    fi
  fi
  for ((i=0;i<60 && SECONDS<cleanup_deadline;i++)); do
    [[ -z $(docker_cmd container ls -q --filter label=com.docker.swarm.service.name=moduleos-system_moduleosd 2>/dev/null) ]] && break
    sleep 0.5
  done
  app_service="moduleos_app${run_id}"
  app_record=$(docker_cmd service inspect --format '{{.ID}}|{{.Spec.Name}}|{{index .Spec.Labels "moduleos.managed"}}|{{index .Spec.Labels "moduleos.resource.kind"}}' "$app_service" 2>/dev/null || true)
  if [[ -n "$app_record" ]]; then
    if [[ "$app_record" == *"|$app_service|true|application-service" && ( -z "$app_service_id" || "${app_record%%|*}" == "$app_service_id" ) ]]; then
      docker_cmd service rm "${app_record%%|*}" >/dev/null 2>&1 || result=1
    else
      echo 'Refusing to remove an unowned application service' >&2; result=1
    fi
  fi
  for ((i=0;i<60 && SECONDS<cleanup_deadline;i++)); do
    [[ -z $(docker_cmd service ls --filter name=moduleos-system_moduleosd -q 2>/dev/null) && -z $(docker_cmd service ls --filter name="$app_service" -q 2>/dev/null) ]] && break
    sleep 0.5
  done
  for container_id in $(docker_cmd container ls -aq --filter "label=io.moduleos.stack.run=$run_id" 2>/dev/null); do
    if [[ $(docker_cmd container inspect --format '{{index .Config.Labels "io.moduleos.stack.run"}}' "$container_id" 2>/dev/null) == "$run_id" ]]; then
      docker_cmd container rm -f "$container_id" >/dev/null 2>&1 || result=1
    else
      echo 'Refusing to remove an unowned container' >&2; result=1
    fi
  done
  for network in "moduleos-smoke-${run_id}-net"; do
    network_record=$(docker_cmd network inspect --format '{{.ID}}|{{.Name}}|{{index .Labels "moduleos.managed"}}|{{index .Labels "moduleos.resource.kind"}}' "$network" 2>/dev/null || true)
    if [[ -n "$network_record" ]]; then
      if [[ "$network_record" == *"|$network|true|project-network" && ( -z "$app_network_id" || "${network_record%%|*}" == "$app_network_id" ) ]]; then
        remove_network_bounded "${network_record%%|*}" || result=1
      else
        echo 'Refusing to remove an unowned project network' >&2; result=1
      fi
    fi
  done
  root_record=$(docker_cmd network inspect --format '{{.ID}}|{{.Name}}|{{index .Labels "moduleos.managed"}}|{{index .Labels "moduleos.resource.kind"}}' moduleos-root-net 2>/dev/null || true)
  if [[ -n "$root_record" ]]; then
    if [[ "$root_record" == *"|moduleos-root-net|true|project-network" && ( -z "$root_network_id" || "${root_record%%|*}" == "$root_network_id" ) ]]; then
      remove_network_bounded "${root_record%%|*}" || result=1
    else
      echo 'Refusing to remove an unowned or replaced root network' >&2
      result=1
    fi
  fi
  default_record=$(docker_cmd network inspect --format '{{.ID}}|{{.Name}}|{{index .Labels "com.docker.stack.namespace"}}' moduleos-system_default 2>/dev/null || true)
  if [[ -n "$default_record" ]]; then
    if [[ "$default_record" == *'|moduleos-system_default|moduleos-system' && ( -z "$default_network_id" || "${default_record%%|*}" == "$default_network_id" ) ]]; then
      remove_network_bounded "${default_record%%|*}" || result=1
    else
      echo 'Refusing to remove a replaced stack network' >&2
      result=1
    fi
  fi
  if ((swarm_owned)); then
    secret_record=$(docker_cmd secret inspect --format '{{.ID}}|{{index .Spec.Labels "io.moduleos.stack.run"}}' "$secret" 2>/dev/null || true)
    if [[ -n "$secret_record" ]]; then
      if [[ -n "$secret_id" && "$secret_record" == "$secret_id|$run_id" ]]; then
        docker_cmd secret rm "$secret_id" >/dev/null 2>&1 || result=1
      else
        echo 'Refusing to remove a replaced or unrecorded Secret' >&2
        result=1
      fi
    fi
    volume_record=$(docker_cmd volume inspect --format '{{.Name}}|{{.CreatedAt}}|{{.Mountpoint}}|{{index .Labels "io.moduleos.stack.run"}}' "$volume" 2>/dev/null || true)
    if [[ -n "$volume_record" ]]; then
      if [[ -n "$volume_identity" && "$volume_record" == "$volume_identity" ]]; then
        docker_cmd volume rm "$volume" >/dev/null 2>&1 || result=1
      else
        echo 'Refusing to remove a replaced or unrecorded volume' >&2
        result=1
      fi
    fi
    canary_record=$(docker_cmd network inspect --format '{{.ID}}|{{.Name}}|{{index .Labels "io.moduleos.stack.canary"}}' "$canary_network" 2>/dev/null || true)
    if [[ -n "$canary_record" ]]; then
      if [[ "$canary_record" == *"|$canary_network|$run_id" && ( -z "$canary_network_id" || "${canary_record%%|*}" == "$canary_network_id" ) ]]; then
        remove_network_bounded "${canary_record%%|*}" || result=1
      else
        echo 'Canary network was replaced' >&2
        result=1
      fi
    elif [[ -n "$canary_network_id" ]]; then
      echo 'Canary network was removed' >&2
      result=1
    fi
    canary_record=$(docker_cmd secret inspect --format '{{.ID}}|{{.Spec.Name}}|{{index .Spec.Labels "io.moduleos.stack.canary"}}' "$canary_secret" 2>/dev/null || true)
    if [[ -n "$canary_record" ]]; then
      if [[ "$canary_record" == *"|$canary_secret|$run_id" && ( -z "$canary_secret_id" || "${canary_record%%|*}" == "$canary_secret_id" ) ]]; then
        docker_cmd secret rm "${canary_record%%|*}" >/dev/null 2>&1 || result=1
      else
        echo 'Canary secret was replaced' >&2
        result=1
      fi
    elif [[ -n "$canary_secret_id" ]]; then
      echo 'Canary secret was removed' >&2
      result=1
    fi
    canary_record=$(docker_cmd volume inspect --format '{{.Name}}|{{.CreatedAt}}|{{.Mountpoint}}|{{index .Labels "io.moduleos.stack.canary"}}' "$canary_volume" 2>/dev/null || true)
    if [[ -n "$canary_record" ]]; then
      if [[ -n "$canary_volume_identity" && "$canary_record" == "$canary_volume_identity" ]]; then
        docker_cmd volume rm "$canary_volume" >/dev/null 2>&1 || result=1
      else
        echo 'Canary volume was replaced' >&2
        result=1
      fi
    elif ((canary_volume_created)); then
      echo 'Canary volume was removed' >&2
      result=1
    fi
    docker_cmd swarm leave --force >/dev/null 2>&1 || result=1
  fi
  if [[ $(docker_cmd image inspect --format '{{index .Config.Labels "io.moduleos.stack.run"}}' "$updated_image" 2>/dev/null) == "$run_id" ]]; then
    docker_cmd image rm "$updated_image" >/dev/null 2>&1 || result=1
  fi
  if [[ $(docker_cmd image inspect --format '{{index .Config.Labels "io.moduleos.stack.run"}}' "$bad_image" 2>/dev/null) == "$run_id" ]]; then
    docker_cmd image rm "$bad_image" >/dev/null 2>&1 || result=1
  fi
  if [[ $(docker_cmd image inspect --format '{{index .Config.Labels "io.moduleos.stack.run"}}' "$image" 2>/dev/null) == "$run_id" ]]; then
    docker_cmd image rm "$image" >/dev/null 2>&1 || result=1
  fi
  if ! docker_cmd info >/dev/null 2>&1; then
    echo 'Cleanup could not verify the Docker daemon' >&2
    result=1
  fi
  for resource in \
    "service moduleos-system_moduleosd" "service $app_service" \
    "secret $secret" "volume $volume" \
    "secret $canary_secret" "volume $canary_volume" \
    "image $image" "image $bad_image" "image $updated_image" \
    "network moduleos-system_default" "network moduleos-root-net" "network moduleos-smoke-${run_id}-net" "network $canary_network"; do
    read -r kind name <<< "$resource"
    if docker_cmd "$kind" inspect "$name" >/dev/null 2>&1; then
      printf 'Owned resource remains after cleanup: %s\n' "$kind" >&2
      result=1
    else
      inspect_status=$?
      if ((inspect_status != 1)); then
        printf 'Could not verify resource removal: %s (Docker exit %d)\n' "$kind" "$inspect_status" >&2
        result=1
      fi
    fi
  done
  if owned_containers=$(docker_cmd container ls -aq --filter "label=io.moduleos.stack.run=$run_id" 2>/dev/null); then
    :
  else
    echo 'Could not verify container removal' >&2
    result=1
  fi
  if [[ -n $owned_containers ]]; then
    echo 'Owned containers remain after cleanup' >&2
    result=1
  fi
  if ((SECONDS >= cleanup_deadline)); then
    echo 'Cleanup exceeded its 180-second deadline' >&2
    result=1
  fi
  rm -- "$capture_file" || result=1
  remaining=$((cleanup_deadline-SECONDS))
  if ((remaining > 0)); then
    ((remaining < 5)) || remaining=5
    timeout --signal=TERM --kill-after=2s "${remaining}s" rm -rf -- "$work_dir" "$app_root" || result=1
  else
    echo 'Cleanup deadline expired before local files were removed' >&2
    result=1
  fi
  exit "$result"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
advertise_addr="$(ip -4 route get 1.1.1.1 | awk 'NR==1 {for(i=1;i<=NF;i++) if($i=="src") {print $(i+1); exit}}')"
[[ -n $advertise_addr ]] || { echo 'No Swarm advertise address' >&2; exit 2; }
swarm_owned=1
docker_cmd swarm init --advertise-addr "$advertise_addr" >/dev/null
capture_docker_cmd canary_network_id 8192 network create --driver overlay --attachable --label "io.moduleos.stack.canary=$run_id" "$canary_network"
[[ -n "$canary_network_id" ]] || { echo 'Could not record canary network identity' >&2; exit 2; }
docker_cmd volume create --label "io.moduleos.stack.canary=$run_id" "$canary_volume" >/dev/null
canary_volume_created=1
capture_docker_cmd canary_volume_identity 8192 volume inspect --format '{{.Name}}|{{.CreatedAt}}|{{.Mountpoint}}|{{index .Labels "io.moduleos.stack.canary"}}' "$canary_volume"
[[ -n "$canary_volume_identity" ]] || { echo 'Could not record canary volume identity' >&2; exit 2; }
od -An -N32 -tx1 /dev/urandom | tr -d ' \n' > "$work_dir/canary-key"
capture_docker_cmd canary_secret_id 8192 secret create --label "io.moduleos.stack.canary=$run_id" "$canary_secret" - < "$work_dir/canary-key"
[[ -n "$canary_secret_id" ]] || { echo 'Could not record canary secret identity' >&2; exit 2; }
revision="$(git rev-parse HEAD)"
build_date="$(date -u -d "$(git show -s --format=%cI HEAD)" +%Y-%m-%dT%H:%M:%SZ)"
docker_build buildx build --platform linux/amd64 --load -f apps/control-plane/Dockerfile \
  --build-arg VERSION=stack-smoke --build-arg "REVISION=$revision" \
  --build-arg "BUILD_DATE=$build_date" --label "io.moduleos.stack.run=$run_id" -t "$image" . >/dev/null
capture_docker_cmd image_id 8192 image inspect --format '{{.Id}}' "$image"
[[ $image_id =~ ^sha256:[a-f0-9]{64}$ ]] || { echo 'Built image lacks an immutable ID' >&2; exit 2; }
docker_cmd volume create --driver local --label "io.moduleos.stack.run=$run_id" "$volume" >/dev/null
capture_docker_cmd volume_identity 8192 volume inspect --format '{{.Name}}|{{.CreatedAt}}|{{.Mountpoint}}|{{index .Labels "io.moduleos.stack.run"}}' "$volume"
[[ -n "$volume_identity" ]] || { echo 'Could not record volume identity' >&2; exit 2; }
od -An -N32 -tx1 /dev/urandom | tr -d ' \n' > "$work_dir/key"
capture_docker_cmd secret_id 8192 secret create --label "io.moduleos.stack.run=$run_id" "$secret" - < "$work_dir/key"
[[ -n "$secret_id" ]] || { echo 'Could not record Secret identity' >&2; exit 2; }
cat > "$work_dir/moduleos.env" <<EOF
MODULEOS_IMAGE=$image
MODULEOS_PORT=$port
MODULEOS_BASE_DOMAIN=moduleos.example
MODULEOS_API_KEY_SECRET=$secret
MODULEOS_DATA_VOLUME=$volume
MODULEOS_APP_DATA_ROOT=$app_root
EOF
export MODULEOS_STACK_IMAGE="$image" MODULEOS_STACK_SECRET="$secret" MODULEOS_STACK_VOLUME="$volume"
export MODULEOS_STACK_ENV_FILE="$work_dir/moduleos.env" MODULEOS_STACK_KEY_FILE="$work_dir/key"
export MODULEOS_STACK_SERVICE_ID_FILE="$work_dir/service-id" MODULEOS_STACK_RUN_ID="$run_id"
export MODULEOS_STACK_APP_SERVICE_ID_FILE="$work_dir/app-service-id" MODULEOS_STACK_APP_NETWORK_ID_FILE="$work_dir/app-network-id"
export MODULEOS_STACK_ROOT_NETWORK_ID_FILE="$work_dir/root-network-id"
export MODULEOS_STACK_DEFAULT_NETWORK_ID_FILE="$work_dir/default-network-id"
export MODULEOS_STACK_PORT="$port"
export MODULEOS_STACK_APP_ROOT="$app_root" MODULEOS_STACK_BAD_IMAGE="$bad_image"
setsid timeout --signal=TERM --kill-after=5s 25m go test -tags=stack -count=1 -v -timeout=20m ./deploy/swarm/stacktest &
suite_pid=$!
if wait "$suite_pid"; then
  :
else
  status=$?
  exit "$status"
fi
