#!/usr/bin/env bash
# Unit test for load-env.sh (no cluster required).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
SCRIPT="$ROOT/scripts/dev/load-env.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

if COROOT_ENV="$tmp/missing.env" bash "$SCRIPT" --export >/dev/null 2>&1; then
  echo "expected missing .env to fail" >&2
  exit 1
fi

printf 'KUBERNETES_CONTEXT_NAME=\n' >"$tmp/empty.env"
if COROOT_ENV="$tmp/empty.env" bash "$SCRIPT" --export >/dev/null 2>&1; then
  echo "expected empty KUBERNETES_CONTEXT_NAME to fail" >&2
  exit 1
fi

printf '# comment\nKUBERNETES_CONTEXT_NAME=k3s-server\n' >"$tmp/ok.env"
got="$(COROOT_ENV="$tmp/ok.env" bash "$SCRIPT" --export)"
eval "$got"
if [[ "$KUBERNETES_CONTEXT_NAME" != "k3s-server" || -n "$DEV_REMOTE_HOST" ]]; then
  echo "expected k3s-server and empty DEV_REMOTE_HOST, got context=$KUBERNETES_CONTEXT_NAME remote=$DEV_REMOTE_HOST" >&2
  echo "$got" >&2
  exit 1
fi

printf 'KUBERNETES_CONTEXT_NAME=k3s-server\nDEV_REMOTE_HOST=server\n' >"$tmp/remote.env"
got="$(COROOT_ENV="$tmp/remote.env" bash "$SCRIPT" --export)"
eval "$got"
if [[ "$KUBERNETES_CONTEXT_NAME" != "k3s-server" || "$DEV_REMOTE_HOST" != "server" ]]; then
  echo "expected remote=server, got context=$KUBERNETES_CONTEXT_NAME remote=$DEV_REMOTE_HOST" >&2
  echo "$got" >&2
  exit 1
fi
