#!/usr/bin/env bash
# Extract, never execute, an upstream digest-pinned tool image. No daemon mounts.
set -euo pipefail
[[ $# == 2 && -d $2 ]] || { echo 'usage: extract-ci-tool.sh TOOL EXISTING_DIRECTORY' >&2; exit 2; }
tool=$1
destination=$(cd -- "$2" && pwd)
case $tool in
  shellcheck) image=koalaman/shellcheck:v0.11.0@sha256:61862eba1fcf09a484ebcc6feea46f1782532571a34ed51fedf90dd25f925a8d; binary=/bin/shellcheck ;;
  hadolint) image=hadolint/hadolint:v2.15.1@sha256:32dac94127fd60b7b7e3fbfc65e1383b9b5e25c9bfd7b8536de7a539fe68a12d; binary=/bin/hadolint ;;
  trivy) image=aquasec/trivy:0.76.2@sha256:8f6bb947dbc528902e11f2f5be52f7b519f6a39d5d148f41166f2040499768fb; binary=/usr/local/bin/trivy ;;
  *) echo 'unknown CI tool' >&2; exit 2 ;;
esac
name="moduleos-tool-$(od -An -N12 -tx1 /dev/urandom | tr -d ' \n')"
work=$(mktemp -d)
source "$(dirname -- "${BASH_SOURCE[0]}")/ci-process.sh"
created=0
cleanup() {
  local status=$? owner
  # EXIT cleanup must finish even if a second signal arrives while inspecting.
  trap '' INT TERM
  if owner=$(timeout --kill-after=2s 30 docker container inspect --format '{{index .Config.Labels "io.moduleos.ci-tool"}}' "$name" 2>/dev/null); then
    if [[ $owner != "$name" ]] || ! timeout --kill-after=2s 30 docker container rm --force "$name" >/dev/null; then
      echo 'CI tool container cleanup failed or ownership changed' >&2
      status=1
    fi
  elif ((created)); then
    echo 'CI tool container cleanup cannot be verified' >&2
    status=1
  fi
  rm -rf -- "$work"
  return "$status"
}
trap cleanup EXIT
trap 'ci_cancel 130' INT
trap 'ci_cancel 143' TERM
ci_run timeout --kill-after=2s 300 docker pull "$image"
# Creation may take effect even if its response is lost; verify ownership in cleanup.
created=1
ci_run timeout --kill-after=2s 30 docker create --name "$name" --label "io.moduleos.ci-tool=$name" "$image" >/dev/null
ci_run timeout --kill-after=2s 30 docker cp "$name:$binary" "$destination/$tool"
chmod 0755 "$destination/$tool"
printf '%s: %s\n' "$tool" "$image"
