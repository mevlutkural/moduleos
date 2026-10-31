#!/usr/bin/env sh
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repository_root=$(CDPATH= cd -- "$script_dir/.." && pwd)
build_dir=$(mktemp -d "${TMPDIR:-/tmp}/moduleosd-build.XXXXXX")
trap 'rm -rf "$build_dir"' EXIT HUP INT TERM

cd "$repository_root"

build_target() {
  command=$1
  architecture=$2
  output="$build_dir/$command-linux-$architecture"
  GOOS=linux GOARCH="$architecture" CGO_ENABLED=0 \
    go build -trimpath -o "$output" "./apps/control-plane/cmd/$command"
  if [ ! -s "$output" ]; then
    printf '%s linux/%s build produced no executable output\n' "$command" "$architecture" >&2
    exit 1
  fi
  printf '%s linux/%s static build passed\n' "$command" "$architecture"
}

for command in moduleosd moduleosd-container; do
  build_target "$command" amd64
  build_target "$command" arm64
done
