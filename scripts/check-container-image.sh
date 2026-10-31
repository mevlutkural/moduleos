#!/usr/bin/env sh
set -eu

# Builds the production moduleosd image for the Docker Engine's native
# architecture, proves the same Dockerfile also builds for the other supported
# architecture, and runs the container suite against the native image.
#
# The suite creates only run-labelled containers and volumes and removes
# exactly what it created. It never mounts the host Docker socket and never
# touches Swarm, services, or networks.

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repository_root=$(CDPATH= cd -- "$script_dir/.." && pwd)
cd "$repository_root"

buildx() {
  # MODULEOS_BUILDX names a standalone docker-buildx binary when Buildx is not
  # installed as a Docker CLI plugin.
  if [ -n "${MODULEOS_BUILDX:-}" ]; then
    "$MODULEOS_BUILDX" "$@"
  else
    docker buildx "$@"
  fi
}

command -v docker >/dev/null 2>&1 || {
  printf 'error: the Docker CLI is required\n' >&2
  exit 2
}
command -v go >/dev/null 2>&1 || {
  printf 'error: the Go toolchain is required\n' >&2
  exit 2
}
buildx version >/dev/null 2>&1 || {
  printf 'error: Docker Buildx is required (set MODULEOS_BUILDX to a docker-buildx binary if it is not a CLI plugin)\n' >&2
  exit 2
}

server_os=$(docker version --format '{{.Server.Os}}')
native_architecture=$(docker version --format '{{.Server.Arch}}')
if [ "$server_os" != "linux" ]; then
  printf 'error: a Linux Docker Engine is required\n' >&2
  exit 2
fi
case "$native_architecture" in
  amd64) other_architecture=arm64 ;;
  arm64) other_architecture=amd64 ;;
  *)
    printf 'error: unsupported Docker Engine architecture %s\n' "$native_architecture" >&2
    exit 2
    ;;
esac

version=dev
revision=$(git rev-parse HEAD)
if [ -n "$(git status --porcelain --untracked-files=normal)" ]; then
  revision="$revision-dirty"
fi
build_date=$(TZ=UTC0 git log -1 --format=%cd --date=format-local:%Y-%m-%dT%H:%M:%SZ)
run_id=$(od -An -N6 -tx1 /dev/urandom | tr -d ' \n')
image="moduleosd-container-test:$run_id"

run_label="io.moduleos.container-test.run=$run_id"

# The suite removes its own resources. This trap also covers a test binary
# killed by a timeout or an interrupt, and removes only this run's resources.
cleanup() {
  containers=$(docker ps --all --quiet --filter "label=$run_label" 2>/dev/null || true)
  if [ -n "$containers" ]; then
    # shellcheck disable=SC2086
    docker rm --force $containers >/dev/null 2>&1 || true
  fi
  volumes=$(docker volume ls --quiet --filter "label=$run_label" 2>/dev/null || true)
  if [ -n "$volumes" ]; then
    # shellcheck disable=SC2086
    docker volume rm --force $volumes >/dev/null 2>&1 || true
  fi
  docker image rm "$image" >/dev/null 2>&1 || true
}
trap cleanup EXIT HUP INT TERM

build_image() {
  platform=$1
  shift
  buildx build \
    --file apps/control-plane/Dockerfile \
    --platform "linux/$platform" \
    --build-arg "VERSION=$version" \
    --build-arg "REVISION=$revision" \
    --build-arg "BUILD_DATE=$build_date" \
    "$@" \
    .
}

printf 'building moduleosd image for linux/%s\n' "$native_architecture"
build_image "$native_architecture" --tag "$image" --load
printf 'verifying moduleosd image builds for linux/%s\n' "$other_architecture"
build_image "$other_architecture" --output type=cacheonly

MODULEOS_CONTAINER_IMAGE="$image" \
MODULEOS_CONTAINER_VERSION="$version" \
MODULEOS_CONTAINER_REVISION="$revision" \
MODULEOS_CONTAINER_BUILD_DATE="$build_date" \
MODULEOS_CONTAINER_RUN_ID="$run_id" \
  go test -tags container -count=1 -timeout=20m -v ./apps/control-plane/containertest
