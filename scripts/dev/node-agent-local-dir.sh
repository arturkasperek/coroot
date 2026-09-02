#!/usr/bin/env bash
# Print the absolute path of a sibling coroot-node-agent checkout, or exit 1.
# Expected location: ../coroot-node-agent relative to the Coroot repo root.
set -euo pipefail

ROOT="${COROOT_ROOT:-$(cd "$(dirname "$0")/../.." && pwd)}"
DIR="$(cd "$ROOT/.." && pwd)/coroot-node-agent"
if [[ -f "$DIR/go.mod" && -f "$DIR/Dockerfile" ]]; then
  printf '%s\n' "$DIR"
  exit 0
fi
exit 1
