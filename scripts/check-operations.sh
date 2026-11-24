#!/usr/bin/env bash
# Only the existing guarded stack owner may initialize or dismantle Swarm.
set -Eeuo pipefail
umask 077
evidence_dir=${MODULEOS_OPERATIONS_EVIDENCE_DIR:-}
if [[ -z $evidence_dir ]]; then evidence_dir=$(mktemp -d "${TMPDIR:-/tmp}/moduleos-operations-evidence.XXXXXX"); fi
[[ -d $evidence_dir && ! -L $evidence_dir ]] || { echo 'Invalid operational evidence directory' >&2; exit 2; }
evidence_dir=$(cd -- "$evidence_dir" && pwd -P)
export MODULEOS_OPERATIONS_EVIDENCE="$evidence_dir/results.jsonl"
(set -o noclobber; : > "$MODULEOS_OPERATIONS_EVIDENCE") || { echo 'Refusing to overwrite evidence' >&2; exit 2; }
chmod 0600 "$MODULEOS_OPERATIONS_EVIDENCE"
early_failure() {
  local status=$?
  if ((status)); then printf '%s\n' '{"run":"pending","arch":"unknown","source":"unknown","image":"unknown","stage":"final","status":"failed","reason":"setup_failed","elapsed_ms":0}' >> "$MODULEOS_OPERATIONS_EVIDENCE"; fi
}
trap early_failure EXIT
printf '%s\n' '{"run":"pending","arch":"unknown","source":"unknown","image":"unknown","stage":"setup","status":"started","reason":"none","elapsed_ms":0}' >> "$MODULEOS_OPERATIONS_EVIDENCE"
[[ ${MODULEOS_OPERATIONS_DISPOSABLE:-} == 1 ]] || { echo 'Operational qualification requires MODULEOS_OPERATIONS_DISPOSABLE=1' >&2; exit 2; }
[[ $(uname -s) == Linux && $(uname -m) == x86_64 && $EUID == 0 ]] || { echo 'Operational qualification requires root on disposable native Linux amd64' >&2; exit 2; }
command -v timeout >/dev/null || { echo 'GNU timeout required' >&2; exit 2; }
script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
export MODULEOS_STACK_DISPOSABLE=1 MODULEOS_STACK_OPERATIONS=1
# Reserve more than the stack owner's entire cleanup budget before hard kill.
exec timeout --signal=TERM --kill-after=200s 45m bash "$script_dir/check-swarm-stack.sh"
