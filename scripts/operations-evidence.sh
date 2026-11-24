#!/usr/bin/env bash
# Source-only outcome policy. The runner supplies the allowlisted event writer.
operations_outcome() {
  local suite_result=$1 cleanup_result=$2 reason=runner_failed cleanup_ms=${3:-0} total_ms=${4:-0}
  if ((cleanup_result == 0)); then operations_event cleanup passed none "$cleanup_ms"
  else operations_event cleanup failed cleanup_failed "$cleanup_ms"; fi
  if ((suite_result == 0 && cleanup_result == 0)); then operations_event final passed none "$total_ms"
  else
    if ((cleanup_result != 0)); then reason=cleanup_failed
    elif ((suite_result == 124)); then reason=timeout
    elif ((suite_result == 130 || suite_result == 143)); then reason=cancelled; fi
    operations_event final failed "$reason" "$total_ms"
  fi
}
