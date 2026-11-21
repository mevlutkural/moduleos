#!/usr/bin/env bash
# Sourced by CI helpers. Keep capture and PID ownership in the parent shell.
: "${work:?caller must create a private workspace}"
ci_child=''
ci_run() {
  local result=0
  setsid "$@" <&0 &
  ci_child=$!
  wait "$ci_child" || result=$?
  ci_child=''
  return "$result"
}
ci_capture() {
  local destination=$1
  shift
  ci_run "$@" > "$work/capture" || return $?
  printf -v "$destination" '%s' "$(<"$work/capture")"
}
ci_cancel() {
  local code=$1 attempt
  trap '' INT TERM
  if [[ -n $ci_child ]]; then
    # Also target the PID in case cancellation beats setsid's group setup.
    kill -TERM -- "-$ci_child" "$ci_child" 2>/dev/null || true
    for ((attempt=0; attempt<20; attempt++)); do
      if ! kill -0 -- "-$ci_child" 2>/dev/null && ! kill -0 "$ci_child" 2>/dev/null; then break; fi
      sleep 0.1
    done
    kill -KILL -- "-$ci_child" "$ci_child" 2>/dev/null || true
    wait "$ci_child" 2>/dev/null || true
  fi
  exit "$code"
}
