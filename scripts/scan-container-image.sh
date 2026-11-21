#!/usr/bin/env bash
# Scan the already-tested local image, never pull/rebuild it or expose the daemon.
set -euo pipefail
[[ $# == 2 ]] || { echo 'usage: scan-container-image.sh LOCAL_IMAGE ARCH' >&2; exit 2; }
image=$1
architecture=$2
work=$(mktemp -d)
source "$(dirname -- "${BASH_SOURCE[0]}")/ci-process.sh"
trap 'rm -rf -- "$work"' EXIT
trap 'ci_cancel 130' INT
trap 'ci_cancel 143' TERM
identity=''
ci_capture identity timeout --kill-after=2s 30 docker image inspect --format '{{.Id}} {{.Os}}/{{.Architecture}}' "$image"
[[ $identity == sha256:*" linux/$architecture" ]] || { echo 'local image architecture mismatch' >&2; exit 1; }
id=${identity%% *}
printf 'Image scan: %s; policy HIGH/CRITICAL including unfixed\n' "$identity"
# Do not inherit TRIVY_* (e.g. DOWNLOAD_DB_ONLY), proxy credentials, or an
# ambient policy that can turn scanning into a successful non-scan operation.
scanner() { ci_run env -i "PATH=$PATH" "HOME=${HOME:-$work}" "TMPDIR=$work" trivy "$@"; }
scanner --version
ci_run timeout --kill-after=2s 120 docker image save --output "$work/image.tar" "$id"
# An empty config/ignore file prevents ambient operator policy from weakening CI.
printf '{}\n' > "$work/config.yaml"
: > "$work/ignore"
scanner image --config "$work/config.yaml" --ignorefile "$work/ignore" \
  --input "$work/image.tar" --scanners vuln --severity HIGH,CRITICAL \
  --ignore-unfixed=false --exit-code 1 --timeout 10m
scanner --version
if [[ -n ${GITHUB_STEP_SUMMARY:-} ]]; then
  printf 'Image scan PASS: \x60%s\x60; HIGH/CRITICAL including unfixed.\n' "$identity" >> "$GITHUB_STEP_SUMMARY"
fi
