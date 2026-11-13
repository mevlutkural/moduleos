#!/usr/bin/env bash
set -euo pipefail
if [[ ${1:-} == --help && $# == 1 ]]; then printf 'Usage: %s --env-file FILE\n' "${0##*/}"; exit 0; fi
source "$(dirname -- "${BASH_SOURCE[0]}")/lib.sh"
init_command "$@"
preflight
