#!/usr/bin/env sh
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repository_root=$(CDPATH= cd -- "$script_dir/.." && pwd)
build_dir=$(mktemp -d "${TMPDIR:-/tmp}/moduleosd-build.XXXXXX")
trap 'rm -rf "$build_dir"' EXIT HUP INT TERM

cd "$repository_root"

build_target() {
  architecture=$1
  output="$build_dir/moduleosd-linux-$architecture"
  GOOS=linux GOARCH="$architecture" CGO_ENABLED=0 \
    go build -trimpath -o "$output" ./apps/control-plane/cmd/moduleosd
  if [ ! -s "$output" ]; then
    printf 'moduleosd linux/%s build produced no executable output\n' "$architecture" >&2
    exit 1
  fi
  printf 'moduleosd linux/%s static build passed\n' "$architecture"
}

build_target amd64
build_target arm64
