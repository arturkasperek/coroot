#!/usr/bin/env bash
# Load coroot/.env (or $COROOT_ENV) and require KUBERNETES_CONTEXT_NAME.
# Usage:
#   eval "$(scripts/dev/load-env.sh --export)"
#   source scripts/dev/load-env.sh
set -euo pipefail

_load_env_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
_load_env_file="${COROOT_ENV:-${_load_env_dir}/.env}"

if [ ! -f "$_load_env_file" ]; then
  echo "[dev] missing ${_load_env_file} — set KUBERNETES_CONTEXT_NAME (e.g. k3s-server)" >&2
  exit 1
fi

set -a
# shellcheck disable=SC1090
source "$_load_env_file"
set +a

if [ -z "${KUBERNETES_CONTEXT_NAME:-}" ]; then
  echo "[dev] KUBERNETES_CONTEXT_NAME is empty in ${_load_env_file}" >&2
  exit 1
fi

DEV_REMOTE_HOST="${DEV_REMOTE_HOST:-}"

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  case "${1:-}" in
    --export|"")
      printf 'export KUBERNETES_CONTEXT_NAME=%q\n' "$KUBERNETES_CONTEXT_NAME"
      printf 'export DEV_REMOTE_HOST=%q\n' "$DEV_REMOTE_HOST"
      ;;
    *)
      echo "usage: $0 [--export]" >&2
      exit 2
      ;;
  esac
fi
