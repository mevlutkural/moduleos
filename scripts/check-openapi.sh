#!/usr/bin/env sh
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repository_root=$(CDPATH= cd -- "$script_dir/.." && pwd)

cd "$repository_root"

VACUUM_NO_UPDATE_CHECK=true go run github.com/daveshanley/vacuum@v0.32.0 lint \
  --no-banner \
  --no-style \
  --no-update-check \
  --details \
  --fail-severity warn \
  --ruleset packages/api-contract/vacuum-ruleset.yaml \
  packages/api-contract/openapi.yaml
