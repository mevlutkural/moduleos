#!/usr/bin/env sh
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repository_root=$(CDPATH= cd -- "$script_dir/.." && pwd)
profile_dir=$(mktemp -d "${TMPDIR:-/tmp}/moduleos-coverage.XXXXXX")
trap 'rm -rf "$profile_dir"' EXIT HUP INT TERM

cd "$repository_root"

failed=0
printf '%-16s %-18s %10s %10s\n' "LAYER" "PACKAGE" "ACTUAL" "MINIMUM"
printf '%-16s %-18s %10s %10s\n' "----------------" "------------------" "----------" "----------"

# Every package is evaluated independently. Coverage from one package can never
# compensate for another package falling below its own architectural floor.
while IFS='|' read -r layer package minimum; do
  profile="$profile_dir/$(basename "$package").out"
  test_log="$profile_dir/$(basename "$package").log"

  if ! go test -count=1 -covermode=atomic -coverprofile="$profile" "$package" >"$test_log" 2>&1; then
    printf '%-16s %-18s %10s %10s\n' "$layer" "$(basename "$package")" "TEST FAIL" "${minimum}%"
    sed 's/^/  /' "$test_log"
    failed=1
    continue
  fi

  actual=$(go tool cover -func="$profile" | awk '/^total:/ {gsub(/%/, "", $3); print $3}')
  if [ -z "$actual" ]; then
    printf '%-16s %-18s %10s %10s\n' "$layer" "$(basename "$package")" "NO DATA" "${minimum}%"
    failed=1
    continue
  fi

  if awk -v actual="$actual" -v minimum="$minimum" 'BEGIN { exit !(actual + 0 < minimum + 0) }'; then
    result="FAIL"
    failed=1
  else
    result="PASS"
  fi
  printf '%-16s %-18s %9s%% %7s%% %s\n' "$layer" "$(basename "$package")" "$actual" "$minimum" "$result"
done <<'POLICY'
transport|./apps/control-plane/internal/api|90
transport|./apps/control-plane/internal/api/apiresponse|95
transport|./apps/control-plane/internal/api/handler|90
transport|./apps/control-plane/internal/api/middleware|95
domain|./apps/control-plane/internal/app|90
control-loop|./apps/control-plane/internal/reconciler|85
infrastructure|./apps/control-plane/internal/store|80
infrastructure|./apps/control-plane/internal/swarm|85
infrastructure|./apps/control-plane/internal/watcher|90
foundation|./apps/control-plane/internal/config|95
foundation|./apps/control-plane/internal/buildinfo|100
foundation|./apps/control-plane/internal/banner|100
foundation|./apps/control-plane/internal/logger|100
POLICY

if [ "$failed" -ne 0 ]; then
  printf '\nCoverage policy failed. Add meaningful tests or change the policy in a reviewed pull request.\n' >&2
  exit 1
fi

printf '\nCoverage policy passed.\n'
