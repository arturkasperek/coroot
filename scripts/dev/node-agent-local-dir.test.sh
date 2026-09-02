#!/usr/bin/env bash
# Unit test for node-agent-local-dir.sh (no cluster required).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
SCRIPT="$ROOT/scripts/dev/node-agent-local-dir.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

mkdir -p "$tmp/coroot"
if COROOT_ROOT="$tmp/coroot" "$SCRIPT" >/dev/null 2>&1; then
  echo "expected empty sibling to fail" >&2
  exit 1
fi

mkdir -p "$tmp/coroot-node-agent"
printf 'module github.com/coroot/coroot-node-agent\n' >"$tmp/coroot-node-agent/go.mod"
touch "$tmp/coroot-node-agent/Dockerfile"
got="$(COROOT_ROOT="$tmp/coroot" "$SCRIPT")"
want="$tmp/coroot-node-agent"
if [[ "$got" != "$want" ]]; then
  echo "path mismatch: got=$got want=$want" >&2
  exit 1
fi
