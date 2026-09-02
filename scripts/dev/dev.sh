#!/usr/bin/env bash
# Create or reuse the kind cluster, then start Tilt (in-cluster backend + frontend).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

KIND_CLUSTER_NAME="${KIND_CLUSTER_NAME:-coroot-dev}"

bash "$ROOT/scripts/dev/k8s-dev-tools.sh"

DEV_SSH_HOST="$(bash "$ROOT/scripts/dev/kind-kubeconfig-docker-host.sh" --print-remote-host)"
if [ -n "$DEV_SSH_HOST" ]; then
  echo "[dev] Docker context is SSH ($DEV_SSH_HOST) — kind API advertised there"
else
  echo "[dev] Docker context is local (unix) — no kind API rewrite"
fi

if ! kind get clusters | grep -qx "$KIND_CLUSTER_NAME"; then
  echo "[dev] no kind cluster named $KIND_CLUSTER_NAME — creating"
  KIND_CFG="$(mktemp)"
  bash "$ROOT/scripts/dev/kind-kubeconfig-docker-host.sh" --patch-kind-config "$ROOT/deploy/kind/kind-config.yaml" > "$KIND_CFG"
  if ! kind create cluster --name "$KIND_CLUSTER_NAME" --config "$KIND_CFG"; then
    rm -f "$KIND_CFG"
    exit 1
  fi
  rm -f "$KIND_CFG"
else
  echo "[dev] kind cluster $KIND_CLUSTER_NAME already exists"
fi

eval "$(bash "$ROOT/scripts/dev/kind-dev-kubeconfig.sh" --export)"

bash "$ROOT/scripts/dev/k8s-dev-prereqs.sh"

echo
echo "  [dev] starting Tilt — edit files locally; backend/frontend reload in-cluster."
echo "    Tilt UI:  http://localhost:10350"
echo "    Coroot:        http://localhost:18080"
echo "    Next.js demo:  http://localhost:13000  (calls Express)"
echo "    Express demo:  http://localhost:13001  /api/hello /api/slow /api/error"
echo

exec tilt up --context "kind-${KIND_CLUSTER_NAME}" ${TILT_ARGS:-}
