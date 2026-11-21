#!/usr/bin/env bash
set -euo pipefail
[[ $# == 1 && -d $1 ]] || { echo 'usage: install-ci-tools.sh EXISTING_DIRECTORY' >&2; exit 2; }
[[ $(uname -s)/$(uname -m) == Linux/x86_64 ]] || { echo 'linters require Linux amd64' >&2; exit 2; }
script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
bash "$script_dir/extract-ci-tool.sh" shellcheck "$1"
bash "$script_dir/extract-ci-tool.sh" hadolint "$1"
