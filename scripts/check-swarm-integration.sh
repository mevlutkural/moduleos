#!/usr/bin/env bash
set -Eeuo pipefail

if [[ "${MODULEOS_INTEGRATION_DISPOSABLE:-}" != "1" ]]; then
  echo "error: MODULEOS_INTEGRATION_DISPOSABLE=1 is required" >&2
  exit 2
fi

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "${script_dir}/.." && pwd)"
cd "${repo_root}"

command -v docker >/dev/null 2>&1 || {
  echo "error: Docker CLI is required" >&2
  exit 2
}

readonly docker_endpoint="unix:///var/run/docker.sock"
readonly -a docker_cmd=(docker --host "${docker_endpoint}")
export MODULEOS_INTEGRATION_DOCKER_ENDPOINT="${docker_endpoint}"
readonly integration_run_id="$(od -An -N6 -tx1 /dev/urandom | tr -d ' \n')"
export MODULEOS_INTEGRATION_RUN_ID="${integration_run_id}"

"${docker_cmd[@]}" info >/dev/null

swarm_state="$("${docker_cmd[@]}" info --format '{{.Swarm.LocalNodeState}}')"
swarm_owned=0
baseline_captured=0
declare -a baseline_service_ids=()
declare -a baseline_network_ids=()

contains_id() {
  local candidate="$1"
  shift
  local baseline_id
  for baseline_id in "$@"; do
    if [[ "${candidate}" == "${baseline_id}" ]]; then
      return 0
    fi
  done
  return 1
}

remove_network_with_retry() {
  local network_id="$1"
  local attempt
  for ((attempt = 1; attempt <= 60; attempt++)); do
    if "${docker_cmd[@]}" network rm "${network_id}" >/dev/null 2>&1; then
      return 0
    fi
    if ! "${docker_cmd[@]}" network inspect "${network_id}" >/dev/null 2>&1; then
      return 0
    fi
    sleep 0.25
  done
  return 1
}

owns_service() {
  local resource_id="$1"
  local managed_label
  local name
  local resource_kind
  local run_label
  name="$("${docker_cmd[@]}" service inspect --format '{{.Spec.Name}}' "${resource_id}")" || return 1
  run_label="$("${docker_cmd[@]}" service inspect --format '{{index .Spec.Labels "io.moduleos.integration.run"}}' "${resource_id}")" || return 1
  if [[ "${run_label}" == "${integration_run_id}" ]]; then
    return 0
  fi
  managed_label="$("${docker_cmd[@]}" service inspect --format '{{index .Spec.Labels "moduleos.managed"}}' "${resource_id}")" || return 1
  resource_kind="$("${docker_cmd[@]}" service inspect --format '{{index .Spec.Labels "moduleos.resource.kind"}}' "${resource_id}")" || return 1
  [[ "${name}" == *-"${integration_run_id}"-* && "${managed_label}" == "true" && "${resource_kind}" == "application-service" ]]
}

owns_network() {
  local resource_id="$1"
  local managed_label
  local name
  local resource_kind
  local run_label
  name="$("${docker_cmd[@]}" network inspect --format '{{.Name}}' "${resource_id}")" || return 1
  run_label="$("${docker_cmd[@]}" network inspect --format '{{index .Labels "io.moduleos.integration.run"}}' "${resource_id}")" || return 1
  if [[ "${run_label}" == "${integration_run_id}" ]]; then
    return 0
  fi
  managed_label="$("${docker_cmd[@]}" network inspect --format '{{index .Labels "moduleos.managed"}}' "${resource_id}")" || return 1
  resource_kind="$("${docker_cmd[@]}" network inspect --format '{{index .Labels "moduleos.resource.kind"}}' "${resource_id}")" || return 1
  [[ "${name}" == *-"${integration_run_id}"-* && "${managed_label}" == "true" && "${resource_kind}" == "project-network" ]]
}

restore_runtime_baseline() {
  local cleanup_status=0
  local current_networks
  local current_services
  local resource_id

  if ! current_services="$("${docker_cmd[@]}" service ls --quiet)"; then
    echo "error: failed to list services while restoring the runtime baseline" >&2
    return 1
  fi
  while IFS= read -r resource_id; do
    [[ -z "${resource_id}" ]] && continue
    if ! contains_id "${resource_id}" "${baseline_service_ids[@]}"; then
      if ! owns_service "${resource_id}"; then
        echo "error: refusing to remove unowned post-baseline service ${resource_id:0:12}" >&2
        cleanup_status=1
        continue
      fi
      if ! "${docker_cmd[@]}" service rm "${resource_id}" >/dev/null; then
        echo "error: failed to remove test-created service ${resource_id:0:12}" >&2
        cleanup_status=1
      fi
    fi
  done <<< "${current_services}"

  if ! current_networks="$("${docker_cmd[@]}" network ls --quiet)"; then
    echo "error: failed to list networks while restoring the runtime baseline" >&2
    return 1
  fi
  while IFS= read -r resource_id; do
    [[ -z "${resource_id}" ]] && continue
    if ! contains_id "${resource_id}" "${baseline_network_ids[@]}"; then
      if ! owns_network "${resource_id}"; then
        echo "error: refusing to remove unowned post-baseline network ${resource_id:0:12}" >&2
        cleanup_status=1
        continue
      fi
      if ! remove_network_with_retry "${resource_id}"; then
        echo "error: failed to remove test-created network ${resource_id:0:12}" >&2
        cleanup_status=1
      fi
    fi
  done <<< "${current_networks}"

  return "${cleanup_status}"
}

cleanup() {
  local status=$?
  if [[ "${swarm_owned}" == "1" ]]; then
    if ! "${docker_cmd[@]}" swarm leave --force >/dev/null; then
      echo "error: failed to leave script-owned Swarm" >&2
      status=1
    fi
  elif [[ "${baseline_captured}" == "1" ]] && ! restore_runtime_baseline; then
    status=1
  fi
  exit "${status}"
}
trap cleanup EXIT

if [[ "${swarm_state}" == "inactive" ]]; then
  unexpected_networks="$("${docker_cmd[@]}" network ls --format '{{.Name}}' | awk '$1 != "bridge" && $1 != "docker_gwbridge" && $1 != "host" && $1 != "none" { print }')"
  existing_containers="$("${docker_cmd[@]}" container ls --all --quiet)"
  if [[ -n "${unexpected_networks}" || -n "${existing_containers}" ]]; then
    echo "error: refusing to initialize Swarm on a non-empty Docker daemon" >&2
    exit 2
  fi
  advertise_addr="${MODULEOS_INTEGRATION_ADVERTISE_ADDR:-}"
  if [[ -z "${advertise_addr}" ]] && command -v ip >/dev/null 2>&1; then
    advertise_addr="$(ip -4 route get 1.1.1.1 2>/dev/null | awk 'NR == 1 { for (i = 1; i <= NF; i++) if ($i == "src") { print $(i + 1); exit } }' || true)"
  fi
  if [[ -z "${advertise_addr}" ]] && command -v hostname >/dev/null 2>&1; then
    advertise_addr="$(hostname -I 2>/dev/null | awk '{ print $1 }')"
  fi
  if [[ -z "${advertise_addr}" ]]; then
    echo "error: unable to determine a non-loopback Swarm advertise address" >&2
    exit 2
  fi
  "${docker_cmd[@]}" swarm init --advertise-addr "${advertise_addr}" >/dev/null
  swarm_owned=1
elif [[ "${swarm_state}" != "active" ]]; then
  echo "error: unsupported Swarm state ${swarm_state}" >&2
  exit 2
fi

control_available="$("${docker_cmd[@]}" info --format '{{.Swarm.ControlAvailable}}')"
node_count="$("${docker_cmd[@]}" node ls --format '{{.ID}}' | awk 'NF { count++ } END { print count + 0 }')"
if [[ "${control_available}" != "true" || "${node_count}" != "1" ]]; then
  echo "error: a disposable single-node Swarm manager is required" >&2
  exit 2
fi

if ! baseline_services_output="$("${docker_cmd[@]}" service ls --quiet)"; then
  echo "error: failed to capture the baseline service list" >&2
  exit 2
fi
if ! baseline_networks_output="$("${docker_cmd[@]}" network ls --quiet)"; then
  echo "error: failed to capture the baseline network list" >&2
  exit 2
fi
if [[ -n "${baseline_services_output}" ]]; then
  mapfile -t baseline_service_ids <<< "${baseline_services_output}"
fi
if [[ -n "${baseline_networks_output}" ]]; then
  mapfile -t baseline_network_ids <<< "${baseline_networks_output}"
fi
baseline_captured=1

"${docker_cmd[@]}" network create \
  --driver overlay \
  --attachable \
  --label "io.moduleos.integration.run=${integration_run_id}" \
  --label "moduleos.managed=true" \
  --label "moduleos.resource.kind=project-network" \
  --label "moduleos.project.id=00000000-0000-0000-0000-000000000001" \
  --label "moduleos.project.slug=root" \
  --label "moduleos.schema=v0" \
  moduleos-root-net >/dev/null

go test -tags=integration -count=1 -v -timeout=10m ./apps/control-plane/integration
