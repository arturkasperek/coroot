#!/usr/bin/env bash
# Import a docker image into k3s containerd. Tilt calls this after docker build.
# DEV_REMOTE_HOST from .env: ssh there for save+import; empty means local k3s.
#
# custom_build tags $EXPECTED_REF as name:tilt-build-<unix>, then Tilt retags
# locally to name:tilt-<16 hex of image id> for the Pod. Import the build tag
# and add that content tag so kubelet does not hit a registry.
#
# Bare names (coroot-backend:tag) become docker.io/library/... in containerd.
# Registry names (ghcr.io/coroot/coroot-node-agent:tag) stay as-is.
set -euo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"

ref="${1:-}"
if [ -z "$ref" ]; then
  echo "usage: $0 IMAGE_REF" >&2
  exit 2
fi

# shellcheck disable=SC1091
source "$DIR/load-env.sh"
bash "$DIR/check-docker-remote.sh"

# How docker save / k3s ctr name this ref.
ctr_ref() {
  local img="$1"
  local name="${img%%:*}"
  case "$name" in
    */*) printf '%s' "$img" ;;
    *) printf 'docker.io/library/%s' "$img" ;;
  esac
}

import_and_tag() {
  local img="$1"
  docker save "$img" | /usr/local/bin/k3s ctr images import -
  local id short name src dst
  id="$(docker inspect --format '{{.Id}}' "$img")"
  short="${id#sha256:}"
  short="${short:0:16}"
  name="${img%%:*}"
  src="$(ctr_ref "$img")"
  dst="$(ctr_ref "${name}:tilt-${short}")"
  if [ "$src" != "$dst" ]; then
    echo "[dev] k3s tag $src -> $dst" >&2
    /usr/local/bin/k3s ctr images tag "$src" "$dst"
  fi
}

if [ -n "${DEV_REMOTE_HOST:-}" ]; then
  echo "[dev] importing $ref into k3s on $DEV_REMOTE_HOST" >&2
  if ! ssh -o BatchMode=yes "$DEV_REMOTE_HOST" bash -s -- "$ref" <<'REMOTE'
set -euo pipefail
img="$1"
ctr_ref() {
  local image="$1"
  local n="${image%%:*}"
  case "$n" in
    */*) printf '%s' "$image" ;;
    *) printf 'docker.io/library/%s' "$image" ;;
  esac
}
docker save "$img" | /usr/local/bin/k3s ctr images import -
id="$(docker inspect --format '{{.Id}}' "$img")"
short="${id#sha256:}"
short="${short:0:16}"
name="${img%%:*}"
src="$(ctr_ref "$img")"
dst="$(ctr_ref "${name}:tilt-${short}")"
if [ "$src" != "$dst" ]; then
  echo "[dev] k3s tag $src -> $dst" >&2
  /usr/local/bin/k3s ctr images tag "$src" "$dst"
fi
REMOTE
  then
    echo "[dev] k3s import/tag failed for $ref" >&2
    exit 1
  fi
  exit 0
fi

echo "[dev] importing $ref into local k3s" >&2
import_and_tag "$ref"
