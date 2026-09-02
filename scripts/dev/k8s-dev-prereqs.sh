#!/usr/bin/env bash
# Cluster-level pieces Tilt needs before images: kubeconfig isolation +
# kind node inotify limits (kube-proxy). Prometheus/ClickHouse/Coroot YAML
# is applied by the Tiltfile.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

KIND_CLUSTER_NAME="${KIND_CLUSTER_NAME:-coroot-dev}"
eval "$(bash "$ROOT/scripts/dev/kind-dev-kubeconfig.sh" --export)"
echo "[dev] kubectl using KUBECONFIG=$KUBECONFIG"

bash "$ROOT/scripts/dev/kind-node-sysctl.sh"
