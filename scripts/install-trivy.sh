#!/usr/bin/env bash
set -euo pipefail
[[ $# == 1 && -d $1 ]] || { echo 'usage: install-trivy.sh EXISTING_DIRECTORY' >&2; exit 2; }
[[ $(uname -s) == Linux ]] || { echo 'scanner requires Linux' >&2; exit 2; }
script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
bash "$script_dir/extract-ci-tool.sh" trivy "$1"
