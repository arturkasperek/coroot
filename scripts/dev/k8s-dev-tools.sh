#!/usr/bin/env bash
# Preflight for `make dev`. Lists every missing CLI and exits 1.
# Does not install anything. Go/npm live in the Tilt images, not on the laptop.
set -euo pipefail

missing=0

need() {
  local bin="$1" hint="$2"
  if ! command -v "$bin" >/dev/null 2>&1; then
    echo "[dev] missing ${bin} — ${hint}" >&2
    missing=1
  fi
}

need docker  "install Docker Desktop (or a Docker Engine) and ensure the CLI is on PATH"
need kind    "install from https://kind.sigs.k8s.io/docs/user/quick-start/#installation  (brew install kind)"
need kubectl "install from https://kubernetes.io/docs/tasks/tools/  (brew install kubectl)"
need tilt    "install from https://docs.tilt.dev/install.html  (brew install tilt)"

if [ "$missing" -ne 0 ]; then
  echo "[dev] install the tools above, then re-run make dev" >&2
  exit 1
fi
