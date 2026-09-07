#!/usr/bin/env bash
# Unit test for check-docker-remote.sh (no cluster required).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
SCRIPT="$ROOT/scripts/dev/check-docker-remote.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

printf 'KUBERNETES_CONTEXT_NAME=k3s-server\nDEV_REMOTE_HOST=\n' >"$tmp/local.env"
if ! COROOT_ENV="$tmp/local.env" bash "$SCRIPT" --docker-host 'unix:///var/run/docker.sock'; then
  echo "empty DEV_REMOTE_HOST should allow any docker host" >&2
  exit 1
fi

printf 'KUBERNETES_CONTEXT_NAME=k3s-server\nDEV_REMOTE_HOST=server\n' >"$tmp/remote.env"
if ! COROOT_ENV="$tmp/remote.env" bash "$SCRIPT" --docker-host 'ssh://artur@server'; then
  echo "ssh://artur@server should match DEV_REMOTE_HOST=server" >&2
  exit 1
fi

if COROOT_ENV="$tmp/remote.env" bash "$SCRIPT" --docker-host 'unix:///var/run/docker.sock' >/dev/null 2>&1; then
  echo "unix docker socket should fail when DEV_REMOTE_HOST=server" >&2
  exit 1
fi

if COROOT_ENV="$tmp/remote.env" bash "$SCRIPT" --docker-host 'ssh://artur@other' >/dev/null 2>&1; then
  echo "ssh://other should fail when DEV_REMOTE_HOST=server" >&2
  exit 1
fi
