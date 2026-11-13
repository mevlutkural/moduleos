#!/usr/bin/env bash
# Trusted helpers for the two public Swarm commands. Never source the operator file.
set -euo pipefail

readonly stack_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly stack_name=moduleos-system
readonly service_name=moduleos-system_moduleosd
readonly docker_endpoint=unix:///var/run/docker.sock
if ((BASH_VERSINFO[0] < 4)); then
  printf 'moduleos: Bash 4 or newer is required\n' >&2
  exit 2
fi
declare -A settings=()
declare -a docker_env=()
work_dir=''
render_file=''
image_id=''
resolve_image=''
deployment_image=''
secret_id=''
network_id=''
existing_service_id=''
volume_identity=''
active_docker_pid=''

fail() { printf 'moduleos: %s\n' "$1" >&2; exit 1; }
cleanup_temp() { if [[ -n "$work_dir" && -d "$work_dir" ]]; then rm -rf -- "$work_dir"; fi; }
cancel_active() {
  local i
  if [[ -n $active_docker_pid ]]; then
    kill -TERM -- "-$active_docker_pid" 2>/dev/null || true
    for ((i=0;i<20;i++)); do
      kill -0 -- "-$active_docker_pid" 2>/dev/null || break
      sleep 0.1
    done
    kill -KILL -- "-$active_docker_pid" 2>/dev/null || true
    if [[ -r /proc/$active_docker_pid/stat && $(awk '{print $3}' "/proc/$active_docker_pid/stat" 2>/dev/null) == D ]]; then
      printf 'moduleos: Docker child is uninterruptible\n' >&2
    else
      wait "$active_docker_pid" 2>/dev/null || true
    fi
    active_docker_pid=''
  fi
}
usage() { printf 'Usage: %s --env-file FILE\n' "${0##*/}"; }

init_command() {
  if [[ ${1:-} == --help && $# == 1 ]]; then usage; exit 0; fi
  if [[ $# != 2 || $1 != --env-file || -z $2 ]]; then usage >&2; exit 2; fi
  for program in docker jq timeout realpath stat od awk wc mktemp curl findmnt head setsid; do
    command -v "$program" >/dev/null 2>&1 || fail "missing utility: $program"
  done
  umask 077
  export LC_ALL=C
  work_dir="$(mktemp -d)" || fail 'cannot create private workspace'
  trap cleanup_temp EXIT
  trap 'cancel_active; exit 130' INT
  trap 'cancel_active; exit 143' TERM
  parse_env "$2"
  docker_env=(env -i "PATH=$PATH" "HOME=${HOME:-/}" \
    "MODULEOS_IMAGE=${settings[MODULEOS_IMAGE]}" \
    "MODULEOS_PORT=${settings[MODULEOS_PORT]}" \
    "MODULEOS_BASE_DOMAIN=${settings[MODULEOS_BASE_DOMAIN]}" \
    "MODULEOS_API_KEY_SECRET=${settings[MODULEOS_API_KEY_SECRET]}" \
    "MODULEOS_DATA_VOLUME=${settings[MODULEOS_DATA_VOLUME]}" \
    "MODULEOS_APP_DATA_ROOT=${settings[MODULEOS_APP_DATA_ROOT]}" \
    "MODULEOS_INGRESS_NETWORK=${settings[MODULEOS_INGRESS_NETWORK]}" \
    "MODULEOS_LOG_LEVEL=${settings[MODULEOS_LOG_LEVEL]}" \
    "MODULEOS_CORS_ALLOWED_ORIGINS=${settings[MODULEOS_CORS_ALLOWED_ORIGINS]}")
}

parse_env() {
  local file=$1 line key value number=0 size
  [[ -f $file && ! -L $file ]] || fail 'invalid_input: environment file must be a regular file'
  size=$(wc -c 2>/dev/null < "$file") || fail 'invalid_input: unreadable environment file'
  ((size <= 65536)) || fail 'invalid_input: environment file is too large'
  LC_ALL=C od -v -An -tu1 "$file" 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i==0 || ($i<32 && $i!=10) || $i==127) exit 1}' || fail 'invalid_input: control byte or CR line ending'
  while IFS= read -r line || [[ -n $line ]]; do
    ((number+=1))
    ((${#line} <= 4096)) || fail "invalid_input: line $number is too long"
    [[ -z $line || $line == \#* ]] && continue
    [[ $line == *=* ]] || fail "invalid_input: malformed line $number"
    key=${line%%=*}; value=${line#*=}
    case $key in
      MODULEOS_IMAGE|MODULEOS_PORT|MODULEOS_BASE_DOMAIN|MODULEOS_API_KEY_SECRET|MODULEOS_DATA_VOLUME|MODULEOS_APP_DATA_ROOT|MODULEOS_INGRESS_NETWORK|MODULEOS_LOG_LEVEL|MODULEOS_CORS_ALLOWED_ORIGINS) ;;
      *) fail "invalid_input: unknown key on line $number" ;;
    esac
    [[ -z ${settings[$key]+present} ]] || fail "invalid_input: duplicate $key"
    [[ $line != *\'* && $line != *\"* && $line != *\$* && $line != *\`* ]] || fail "invalid_input: forbidden character on line $number"
    [[ $key =~ ^[A-Z_]+$ && $value != ' '* && $value != *' ' && $value != $'\t'* && $value != *$'\t' ]] || fail "invalid_input: whitespace on line $number"
    settings[$key]=$value
  done < "$file"
  for key in MODULEOS_IMAGE MODULEOS_PORT MODULEOS_BASE_DOMAIN MODULEOS_API_KEY_SECRET MODULEOS_DATA_VOLUME MODULEOS_APP_DATA_ROOT; do
    [[ -n ${settings[$key]:-} ]] || fail "invalid_input: missing $key"
  done
  if [[ -n ${settings[MODULEOS_INGRESS_NETWORK]+present} && -z ${settings[MODULEOS_INGRESS_NETWORK]} ]]; then fail 'invalid_input: empty MODULEOS_INGRESS_NETWORK'; fi
  if [[ -n ${settings[MODULEOS_LOG_LEVEL]+present} && -z ${settings[MODULEOS_LOG_LEVEL]} ]]; then fail 'invalid_input: empty MODULEOS_LOG_LEVEL'; fi
  [[ -z ${settings[MODULEOS_INGRESS_NETWORK]+present} ]] && settings[ingress_defaulted]=yes
  settings[MODULEOS_INGRESS_NETWORK]=${settings[MODULEOS_INGRESS_NETWORK]:-moduleos-ingress}
  settings[MODULEOS_LOG_LEVEL]=${settings[MODULEOS_LOG_LEVEL]:-info}
  settings[MODULEOS_CORS_ALLOWED_ORIGINS]=${settings[MODULEOS_CORS_ALLOWED_ORIGINS]:-}
  [[ ${settings[MODULEOS_PORT]} =~ ^[1-9][0-9]{0,4}$ ]] && ((10#${settings[MODULEOS_PORT]} <= 65535)) || fail 'invalid_input: MODULEOS_PORT'
  [[ ${settings[MODULEOS_BASE_DOMAIN]} =~ ^[a-z0-9][a-z0-9.-]{0,251}[a-z0-9]$ && ${settings[MODULEOS_BASE_DOMAIN]} == *.* ]] || fail 'invalid_input: MODULEOS_BASE_DOMAIN'
  [[ ${settings[MODULEOS_LOG_LEVEL]} =~ ^(debug|info|warn|error)$ ]] || fail 'invalid_input: MODULEOS_LOG_LEVEL'
  if [[ -n ${settings[MODULEOS_CORS_ALLOWED_ORIGINS]} ]]; then
    [[ ${settings[MODULEOS_CORS_ALLOWED_ORIGINS]} != ,* && ${settings[MODULEOS_CORS_ALLOWED_ORIGINS]} != *, && ${settings[MODULEOS_CORS_ALLOWED_ORIGINS]} != *,,* ]] || fail 'invalid_input: MODULEOS_CORS_ALLOWED_ORIGINS'
    local origin cors_port
    local -a origins
    IFS=',' read -r -a origins <<< "${settings[MODULEOS_CORS_ALLOWED_ORIGINS]}"
    for origin in "${origins[@]}"; do
      origin=${origin#"${origin%%[![:space:]]*}"}
      origin=${origin%"${origin##*[![:space:]]}"}
      [[ $origin =~ ^https?://(\[[0-9a-fA-F:.]+\]|[a-zA-Z0-9.-]+)(:([0-9]+))?$ ]] || fail 'invalid_input: MODULEOS_CORS_ALLOWED_ORIGINS'
      cors_port=${BASH_REMATCH[3]:-}
      if [[ -n $cors_port ]]; then
        [[ $cors_port =~ ^[1-9][0-9]{0,4}$ ]] && ((10#$cors_port <= 65535)) || fail 'invalid_input: MODULEOS_CORS_ALLOWED_ORIGINS port'
      fi
    done
  fi
  [[ ${settings[MODULEOS_INGRESS_NETWORK]} =~ ^[a-z0-9]([a-z0-9_-]{0,61}[a-z0-9])?$ ]] || fail 'invalid_input: MODULEOS_INGRESS_NETWORK'
  [[ ${settings[MODULEOS_API_KEY_SECRET]} =~ ^[a-z0-9][a-z0-9_.-]*_v[0-9]+$ && ${#settings[MODULEOS_API_KEY_SECRET]} -le 63 ]] || fail 'invalid_input: MODULEOS_API_KEY_SECRET'
  [[ ${settings[MODULEOS_DATA_VOLUME]} =~ ^[a-zA-Z0-9][a-zA-Z0-9_.-]*$ && ${#settings[MODULEOS_DATA_VOLUME]} -le 255 ]] || fail 'invalid_input: MODULEOS_DATA_VOLUME'
}

docker_bounded() {
  local duration=$1 kill_after=$2 status=0
  shift 2
  "${docker_env[@]}" setsid timeout --signal=TERM --kill-after="$kill_after" "$duration" docker --host "$docker_endpoint" "$@" &
  active_docker_pid=$!
  wait "$active_docker_pid" || status=$?
  active_docker_pid=''
  return "$status"
}
docker_call() { docker_bounded 10s 2s "$@"; }
docker_long() { docker_bounded 300s 5s "$@"; }
capture_docker() {
  local destination=$1 limit=$2 size
  shift 2
  : > "$work_dir/docker-capture"
  docker_call "$@" > "$work_dir/docker-capture" || return $?
  size=$(wc -c < "$work_dir/docker-capture") || return 1
  ((size <= limit)) || return 1
  printf -v "$destination" '%s' "$(<"$work_dir/docker-capture")"
}
capture_docker_combined() {
  local destination=$1 limit=$2 size
  shift 2
  : > "$work_dir/docker-capture"
  docker_call "$@" > "$work_dir/docker-capture" 2>&1 || return $?
  size=$(wc -c < "$work_dir/docker-capture") || return 1
  ((size <= limit)) || return 1
  printf -v "$destination" '%s' "$(<"$work_dir/docker-capture")"
}

is_overlap() { [[ $1 == "$2" || $1 == "$2/"* || $2 == "$1/"* ]]; }

validate_root() {
  local root=${settings[MODULEOS_APP_DATA_ROOT]} real forbidden part owner group mode bits
  [[ $root == /* && $root != / && $root != */ && $root != *,* && -d $root ]] || fail 'invalid_app_root'
  real=$(realpath -e -- "$root" 2>/dev/null) || fail 'invalid_app_root'
  [[ $root == "$real" ]] || fail 'invalid_app_root: path must be canonical'
  for forbidden in /etc /proc /sys /dev /usr/local/bin /var/lib/moduleos /var/run "$docker_root" "$volume_root" "$(dirname -- "$(realpath -e /var/run/docker.sock)")"; do
    is_overlap "$root" "$forbidden" && fail 'invalid_app_root: protected path overlap'
  done
  part=$root
  while :; do
    owner=$(stat -c %u -- "$part" 2>/dev/null) || fail 'invalid_app_root: cannot inspect permissions'
    group=$(stat -c %g -- "$part" 2>/dev/null) || fail 'invalid_app_root: cannot inspect permissions'
    mode=$(stat -c %a -- "$part" 2>/dev/null) || fail 'invalid_app_root: cannot inspect permissions'
    bits=$((8#$mode))
    if ((owner == 65532)); then
      ((bits & 0100)) || fail 'invalid_app_root: uid 65532 cannot traverse'
    elif ((group == 65532)); then
      ((bits & 0010)) || fail 'invalid_app_root: uid 65532 cannot traverse'
    elif ! ((bits & 0001)); then
      fail 'invalid_app_root: uid 65532 cannot traverse'
    fi
    [[ $part == / ]] && break
    part=$(dirname -- "$part")
  done
}

validate_image() {
  local ref=${settings[MODULEOS_IMAGE]} last local_json index
  if [[ $ref == *@* ]]; then
    [[ $ref =~ ^[a-z0-9][a-z0-9._:/-]*@sha256:[a-f0-9]{64}$ ]] || fail 'invalid_input: MODULEOS_IMAGE digest'
    resolve_image=always
    deployment_image=$ref
  else
    last=${ref##*/}
    [[ $ref != sha256:* && $ref =~ ^[a-z0-9][a-z0-9._:/-]*$ && $last == *:* && ${last##*:} != latest ]] || fail 'invalid_input: MODULEOS_IMAGE tag'
    resolve_image=never
    capture_docker local_json 65536 image inspect --format '{{json .}}' "$ref" 2>/dev/null || fail 'invalid_input: local MODULEOS_IMAGE missing'
    [[ $(jq -r '.Os' <<< "$local_json") == linux && $(jq -r '.Architecture' <<< "$local_json") == "$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')" ]] || fail 'invalid_input: MODULEOS_IMAGE platform'
    image_id=$(jq -r '.Id' <<< "$local_json")
    [[ $image_id =~ ^sha256:[a-f0-9]{64}$ ]] || fail 'invalid_input: MODULEOS_IMAGE identity'
    jq -e '.Config.Entrypoint == ["/usr/local/bin/moduleosd-container","start"] and .Config.Healthcheck.Test == ["CMD","/usr/local/bin/moduleosd-container","healthcheck"] and (.Config.Env | index("ENV=production")) and (.Config.Env | index("PORT=3000"))' <<< "$local_json" >/dev/null || fail 'invalid_input: MODULEOS_IMAGE contract'
    # A Swarm worker can pull a tag even when manager resolution is disabled.
    # Submit the inspected local image ID so another artifact cannot run.
    deployment_image=$image_id
  fi
  for index in "${!docker_env[@]}"; do
    [[ ${docker_env[$index]} != MODULEOS_IMAGE=* ]] || docker_env[$index]="MODULEOS_IMAGE=$deployment_image"
  done
}

normalized_image_ref() {
  local ref=$1
  ref=${ref#index.docker.io/}
  ref=${ref#docker.io/}
  if [[ $1 == docker.io/library/* || $1 == index.docker.io/library/* ]]; then
    ref=${ref#library/}
  fi
  printf '%s' "$ref"
}

unquote_scalar() {
  local value=$1
  case $value in
    \"*\") value=${value#\"}; value=${value%\"} ;;
    \'*\') value=${value#\'}; value=${value%\'} ;;
    *\"*|*\'*) return 1 ;;
  esac
  printf '%s' "$value"
}

validate_render() {
  local shutdown grace
  render_file=$work_dir/stack.yml
  if ! docker_call stack config -c "$stack_dir/stack.yml" > "$render_file" 2> "$work_dir/render.err"; then fail 'render_failed'; fi
  [[ ! -s $work_dir/render.err ]] || fail 'render_failed: Docker warning'
  [[ ! $(<"$render_file") == *'API_KEY='* ]] || fail 'render_failed: unexpected API_KEY'
  shutdown=$(awk '/^      SHUTDOWN_TIMEOUT: / {print $2}' "$render_file")
  grace=$(awk '/^    stop_grace_period: / {print $2}' "$render_file")
  shutdown=$(unquote_scalar "$shutdown") || fail 'render_failed: invalid stop scalar'
  grace=$(unquote_scalar "$grace") || fail 'render_failed: invalid stop scalar'
  [[ $shutdown =~ ^[1-9][0-9]*s$ && $grace =~ ^[1-9][0-9]*s$ ]] || fail 'render_failed: missing stop budget'
  shutdown=${shutdown%s}; grace=${grace%s}
  ((${#shutdown} < 8 && ${#grace} < 8 && 10#$grace > 10#$shutdown)) || fail 'render_failed: stop budget'
}

preflight() {
  local info version swarm nodes socket_group secret volume network network_json security_opts listed_services listed_networks fs_type
  [[ $(uname -s) == Linux ]] || fail 'unsupported_host: Linux required'
  capture_docker info 65536 info --format '{{json .}}' 2>/dev/null || fail 'unsupported_host: Docker unavailable'
  [[ $(jq -r '.OSType' <<< "$info") == linux ]] || fail 'unsupported_host: Linux engine required'
  security_opts=$(jq -er '(.SecurityOptions // []) | if type == "array" then join(" ") else error("bad options") end' <<< "$info" 2>/dev/null) || fail 'unsupported_host: Docker security metadata'
  [[ $security_opts != *rootless* && $security_opts != *userns* ]] || fail 'unsupported_host: rootful engine required'
  capture_docker version 128 version --format '{{.Server.APIVersion}}' 2>/dev/null || fail 'unsupported_host: Docker API unavailable'
  [[ $version =~ ^[0-9]+\.[0-9]+$ ]] || fail 'unsupported_host: invalid Docker API'
  ((${version%%.*} > 1 || (${version%%.*} == 1 && ${version##*.} >= 42))) || fail 'unsupported_host: Docker API too old'
  [[ $(jq -r '.Swarm.LocalNodeState' <<< "$info") == active && $(jq -r '.Swarm.ControlAvailable' <<< "$info") == true ]] || fail 'unsupported_host: manager Swarm required'
  capture_docker nodes 65536 node ls --format '{{json .}}' 2>/dev/null || fail 'unsupported_host: cannot inspect Swarm nodes'
  [[ $(wc -l <<< "$nodes") -eq 1 ]] && jq -e '.Status == "Ready" and .Availability == "Active" and .Self == true and .ManagerStatus != ""' <<< "$nodes" >/dev/null || fail 'unsupported_host: exactly one ready manager required'
  [[ -S /var/run/docker.sock ]] || fail 'unsupported_host: local Docker socket missing'
  socket_group=$(stat -c %g /var/run/docker.sock) || fail 'unsupported_host: Docker socket metadata'
  [[ $socket_group =~ ^[0-9]+$ ]] && ((socket_group > 0)) || fail 'unsupported_host: Docker socket group zero'
  capture_docker listed_services 8192 service ls --filter "name=$service_name" --format '{{.Name}}' 2>/dev/null || fail 'unsupported_host: cannot inspect services'
  local existing
  if [[ $listed_services == "$service_name" || $listed_services == *$'\n'"$service_name" || $listed_services == "$service_name"$'\n'* ]]; then
    capture_docker existing 65536 service inspect --format '{{json .}}' "$service_name" 2>/dev/null || fail 'unsupported_host: fixed service changed during inspection'
    [[ $(jq -r '.Spec.Labels["com.docker.stack.namespace"] // empty' <<< "$existing") == "$stack_name" ]] || fail 'unsupported_host: foreign service at fixed name'
    existing_service_id=$(jq -r '.ID // empty' <<< "$existing")
    [[ -n $existing_service_id ]] || fail 'unsupported_host: service identity unavailable'
  fi
  capture_docker secret 65536 secret inspect --format '{{json .}}' "${settings[MODULEOS_API_KEY_SECRET]}" 2>/dev/null || fail 'missing_secret'
  [[ $(jq -r '.Spec.Name' <<< "$secret") == "${settings[MODULEOS_API_KEY_SECRET]}" ]] || fail 'missing_secret: identity'
  secret_id=$(jq -r '.ID // empty' <<< "$secret")
  [[ -n $secret_id ]] || fail 'missing_secret: identity'
  capture_docker volume 65536 volume inspect --format '{{json .}}' "${settings[MODULEOS_DATA_VOLUME]}" 2>/dev/null || fail 'invalid_volume: missing'
  [[ $(jq -r '.Driver' <<< "$volume") == local && $(jq -r '(.Options // {}) | length' <<< "$volume") == 0 ]] || fail 'invalid_volume: local volume without driver options required'
  volume_root=$(jq -r '.Mountpoint' <<< "$volume")
  docker_root=$(jq -r '.DockerRootDir' <<< "$info")
  [[ -d $volume_root && -d $docker_root ]] || fail 'invalid_volume: inaccessible mountpoint'
  volume_root=$(realpath -e -- "$volume_root" 2>/dev/null) || fail 'invalid_volume: mountpoint'
  volume_identity=$(stat -c '%d:%i' -- "$volume_root" 2>/dev/null) || fail 'invalid_volume: mountpoint identity'
  docker_root=$(realpath -e -- "$docker_root" 2>/dev/null) || fail 'unsupported_host: Docker root'
  fs_type=$(findmnt -n -o FSTYPE -T "$volume_root" 2>/dev/null) || fail 'invalid_volume: backing filesystem unavailable'
  case $fs_type in
    nfs|nfs4|cifs|smb3|9p|autofs|tmpfs|fuse.*) fail 'invalid_volume: local persistent filesystem required' ;;
  esac
  if capture_docker network_json 65536 network inspect --format '{{json .}}' "${settings[MODULEOS_INGRESS_NETWORK]}" 2>/dev/null; then
    [[ $(jq -r '.Driver' <<< "$network_json") == overlay && $(jq -r '.Scope' <<< "$network_json") == swarm && $(jq -r '.Attachable' <<< "$network_json") == true ]] || fail 'invalid_ingress_network'
    network_id=$(jq -r '.Id // empty' <<< "$network_json")
    [[ -n $network_id ]] || fail 'invalid_ingress_network: identity'
  else
    capture_docker listed_networks 8192 network ls --filter "name=${settings[MODULEOS_INGRESS_NETWORK]}" --format '{{.Name}}' 2>/dev/null || fail 'invalid_ingress_network: inspection failed'
    if [[ $listed_networks == "${settings[MODULEOS_INGRESS_NETWORK]}" || $listed_networks == "${settings[MODULEOS_INGRESS_NETWORK]}"$'\n'* || $listed_networks == *$'\n'"${settings[MODULEOS_INGRESS_NETWORK]}" || $listed_networks == *$'\n'"${settings[MODULEOS_INGRESS_NETWORK]}"$'\n'* ]]; then
      fail 'invalid_ingress_network: existing network could not be inspected'
    fi
    [[ -n ${settings[ingress_defaulted]+present} ]] || fail 'invalid_ingress_network: missing explicit network'
    printf 'moduleos: warning: missing moduleos-ingress; exposed apps require an attachable overlay\n' >&2
    printf 'docker network create --driver overlay --attachable moduleos-ingress\n' >&2
  fi
  validate_root
  if command -v ss >/dev/null 2>&1 && ss -H -ltn "sport = :${settings[MODULEOS_PORT]}" 2>/dev/null | awk 'END {exit NR == 0}'; then
    printf 'moduleos: warning: selected host port already has a listener\n' >&2
  fi
  validate_image
  validate_render
  printf 'moduleos: preflight passed\n'
}

verify_critical_identities() {
  local current current_root current_identity names
  capture_docker current 65536 secret inspect --format '{{json .}}' "${settings[MODULEOS_API_KEY_SECRET]}" 2>/dev/null || fail 'identity_changed: secret'
  [[ $(jq -r '.ID // empty' <<< "$current") == "$secret_id" && $(jq -r '.Spec.Name // empty' <<< "$current") == "${settings[MODULEOS_API_KEY_SECRET]}" ]] || fail 'identity_changed: secret'
  capture_docker current 65536 volume inspect --format '{{json .}}' "${settings[MODULEOS_DATA_VOLUME]}" 2>/dev/null || fail 'identity_changed: volume'
  [[ $(jq -r '.Driver' <<< "$current") == local && $(jq -r '(.Options // {}) | length' <<< "$current") == 0 ]] || fail 'identity_changed: volume'
  current_root=$(jq -r '.Mountpoint // empty' <<< "$current")
  current_root=$(realpath -e -- "$current_root" 2>/dev/null) || fail 'identity_changed: volume'
  [[ $current_root == "$volume_root" ]] || fail 'identity_changed: volume'
  current_identity=$(stat -c '%d:%i' -- "$current_root" 2>/dev/null) || fail 'identity_changed: volume'
  [[ $current_identity == "$volume_identity" ]] || fail 'identity_changed: volume'
  if [[ -n $network_id ]]; then
    capture_docker current 65536 network inspect --format '{{json .}}' "${settings[MODULEOS_INGRESS_NETWORK]}" 2>/dev/null || fail 'identity_changed: ingress network'
    [[ $(jq -r '.Id // empty' <<< "$current") == "$network_id" && $(jq -r '.Driver' <<< "$current") == overlay && $(jq -r '.Scope' <<< "$current") == swarm && $(jq -r '.Attachable' <<< "$current") == true ]] || fail 'identity_changed: ingress network'
  elif docker_call network inspect "${settings[MODULEOS_INGRESS_NETWORK]}" >/dev/null 2>&1; then
    fail 'identity_changed: ingress network'
  fi
  capture_docker names 8192 service ls --filter "name=$service_name" --format '{{.Name}}' 2>/dev/null || fail 'identity_changed: service'
  if [[ -n $existing_service_id ]]; then
    capture_docker current 65536 service inspect --format '{{json .}}' "$service_name" 2>/dev/null || fail 'identity_changed: service'
    [[ $(jq -r '.ID // empty' <<< "$current") == "$existing_service_id" && $(jq -r '.Spec.Labels["com.docker.stack.namespace"] // empty' <<< "$current") == "$stack_name" ]] || fail 'identity_changed: service'
  elif [[ $names == "$service_name" || $names == *$'\n'"$service_name" || $names == "$service_name"$'\n'* ]]; then
    fail 'identity_changed: service'
  fi
  if [[ $resolve_image == never ]]; then
    capture_docker current 65536 image inspect --format '{{json .}}' "${settings[MODULEOS_IMAGE]}" 2>/dev/null || fail 'identity_changed: image'
    [[ $(jq -r '.Id // empty' <<< "$current") == "$image_id" ]] || fail 'identity_changed: image'
  fi
}
