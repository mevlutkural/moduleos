#!/usr/bin/env bash
set -euo pipefail
if [[ ${1:-} == --help && $# == 1 ]]; then printf 'Usage: %s --env-file FILE\n' "${0##*/}"; exit 0; fi
source "$(dirname -- "${BASH_SOURCE[0]}")/lib.sh"
diagnose() {
  local logs line
  if capture_docker_combined logs 81920 service logs --raw --tail 20 "$service_name"; then
    while IFS= read -r line; do
      [[ ${#line} -le 4096 ]] || continue
      jq -er 'if .msg == "bootstrap failed" and
        ((.phase == "config" and (.reason | IN("config_invalid","api_key_source_conflict","api_key_source_invalid","api_key_file_invalid","value_empty","value_unparsable","value_invalid","value_required"))) or
         (.phase == "logger" and .reason == "logger_unavailable") or
         (.phase == "banner" and .reason == "banner_failed") or
         (.phase == "prepare" and (.reason | IN("database_open_failed","database_migration_failed","docker_client_invalid","listener_bind_failed","assembly_failed","internal_error"))))
        then "bootstrap failed phase=\(.phase) reason=\(.reason)" +
          (if .phase == "config" and (.variable | IN("ENV","LOG_LEVEL","LISTEN_ADDRESS","PORT","DOCKER_ENDPOINT","DOCKER_SOCKET","BASE_DOMAIN","DATABASE_PATH","API_KEY","API_KEY_FILE","INGRESS_NETWORK","CORS_ALLOWED_ORIGINS","ALLOWED_MOUNT_ROOTS","RECONCILE_INTERVAL","RECONCILE_MAX_BACKOFF","RECONCILE_WORKERS","DEPLOYMENT_TIMEOUT","STABILIZATION_WINDOW","SHUTDOWN_TIMEOUT","READINESS_TIMEOUT","HTTP_READ_TIMEOUT","HTTP_WRITE_TIMEOUT","HTTP_IDLE_TIMEOUT","HTTP_BODY_LIMIT","MAX_REPLICAS","MAX_APPLICATIONS","MAX_PROJECTS","MAX_LOG_TAIL","MAX_LOG_PAYLOAD_BYTES","MAX_SSE_CONNECTIONS","MAX_SSE_CONNECTIONS_PER_APP","LOG_TAIL_TIMEOUT","SSE_OPEN_TIMEOUT","SSE_HEARTBEAT_INTERVAL","SSE_IDLE_TIMEOUT","SSE_MAX_DURATION","SSE_INITIAL_TAIL","REQUEST_CONCURRENCY")) then " variable=\(.variable)" else "" end)
        elif .msg == "container start failed" and (.code | IN("docker_socket_root_group","privilege_drop_failed","data_root_inaccessible","data_root_invalid","docker_socket_invalid","docker_socket_absent","sqlite_file_invalid","database_path_invalid","start_failed"))
        then "container start failed code=\(.code)"
        else empty end' <<< "$line" 2>/dev/null || true
    done <<< "$logs"
  fi
}
deployment_failure() { diagnose; fail "$1"; }
init_command "$@"
preflight
verify_critical_identities
deadline=$((SECONDS + 300))
if ! docker_long stack deploy --detach=false --resolve-image "$resolve_image" -c "$render_file" "$stack_name" > "$work_dir/deploy.out" 2> "$work_dir/deploy.err"; then
  deployment_failure 'deployment_failed: inspect the preserved service state'
fi
[[ ! -s $work_dir/deploy.err ]] || deployment_failure 'deployment_failed: Docker reported a warning'
while ((SECONDS < deadline)); do
  capture_docker state 65536 service inspect --format '{{json .}}' "$service_name" 2>/dev/null || deployment_failure 'verification_failed: service missing'
  [[ $(jq -r '.Spec.Mode.Replicated.Replicas' <<< "$state") == 1 && $(jq -r '.Spec.TaskTemplate.ContainerSpec.ReadOnly' <<< "$state") == true ]] || deployment_failure 'verification_failed: service contract'
  actual_image=$(jq -r '.Spec.TaskTemplate.ContainerSpec.Image' <<< "$state")
  [[ $(normalized_image_ref "$actual_image") == "$(normalized_image_ref "$deployment_image")" ]] || deployment_failure 'verification_failed: service image reference'
  update=$(jq -r '.UpdateStatus.State // "completed"' <<< "$state")
  [[ $update != paused ]] || deployment_failure 'deployment_failed: update paused; manual inspection required'
  capture_docker task 8192 service ps --no-trunc --filter desired-state=running --format '{{json .}}' "$service_name" 2>/dev/null || task=''
  task=${task%%$'\n'*}
  if [[ -n $task && $(jq -r '.CurrentState' <<< "$task") == Running* && $update == completed ]]; then
    task_id=$(jq -r '.ID' <<< "$task")
    capture_docker task_detail 65536 inspect --format '{{json .}}' "$task_id" 2>/dev/null || task_detail=''
    container=$(jq -r '.Status.ContainerStatus.ContainerID // empty' <<< "$task_detail")
    actual=''
    if [[ -n $container ]]; then capture_docker actual 65536 container inspect --format '{{json .}}' "$container" 2>/dev/null || actual=''; fi
    if [[ -n $actual && $(jq -r '.State.Health.Status // ""' <<< "$actual") == healthy ]]; then
      running_image=$(jq -r '.Image' <<< "$actual")
      [[ $resolve_image != never || $running_image == "$image_id" ]] || deployment_failure 'image_mismatch: task image differs from preflight'
      if "${docker_env[@]}" timeout 5s curl --fail --silent --max-time 3 "http://127.0.0.1:${settings[MODULEOS_PORT]}/api/v1/live" >/dev/null; then
        printf 'moduleos: deployed %s image %s; /live OK\n' "$service_name" "$running_image"
        exit 0
      fi
    fi
  fi
  sleep 2
done
deployment_failure 'deployment_timeout: inspect the preserved service state'
