#!/usr/bin/env bash
set -euo pipefail
cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.."
scripts=()
while IFS= read -r -d '' file; do
  IFS= read -r first < "$file" || true
  case "$first" in
    '#!'*bash*) bash -n "$file"; scripts+=("$file") ;;
    '#!'*sh*) sh -n "$file"; scripts+=("$file") ;;
  esac
done < <(git ls-files --cached --others --exclude-standard -z)
(( ${#scripts[@]} > 0 ))
shellcheck --external-sources --source-path=SCRIPTDIR "${scripts[@]}"
hadolint apps/control-plane/Dockerfile
MODULEOS_REQUIRE_STACK_RENDER=1 go test -count=1 ./deploy/swarm
