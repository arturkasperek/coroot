#!/usr/bin/env bash
# Remove the make-dev workload. Leaves the k3s cluster running.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$ROOT"

bash "$DIR/k8s-dev-tools.sh"
# shellcheck disable=SC1091
source "$DIR/load-env.sh"

export KUBERNETES_CONTEXT_NAME
echo "[dev] uninstalling from context $KUBERNETES_CONTEXT_NAME"

tilt down --context "$KUBERNETES_CONTEXT_NAME" || true
kubectl --context "$KUBERNETES_CONTEXT_NAME" delete namespace coroot-dev --ignore-not-found
kubectl --context "$KUBERNETES_CONTEXT_NAME" delete clusterrolebinding coroot-dev-cluster-agent --ignore-not-found
kubectl --context "$KUBERNETES_CONTEXT_NAME" delete clusterrole coroot-dev-cluster-agent --ignore-not-found
