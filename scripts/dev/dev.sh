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
echo "    k9s / kubectl top: helm metrics-server in kube-system"
echo "    Tabix:         http://localhost:18081  (ClickHouse SQL UI)"
echo "                   host http://127.0.0.1:18123  user default  password empty"
echo "                   samples (db default, not system):"
echo "                   logs:    SELECT Timestamp, ServiceName, SeverityText, Body"
echo "                            FROM default.otel_logs ORDER BY Timestamp DESC LIMIT 100"
echo "                   traces:  SELECT Timestamp, TraceId, SpanName, ServiceName, Duration"
echo "                            FROM default.otel_traces ORDER BY Timestamp DESC LIMIT 100"
echo "                   metrics: SELECT Timestamp, MetricName, Labels, Value"
echo "                            FROM default.metrics ORDER BY Timestamp DESC LIMIT 100"
echo "                            (agents remote-write here; not Prometheus scrape)"
echo "    Next.js demo:  http://localhost:13000  (calls Express)"
echo "    Express demo:  http://localhost:13001  /api/hello /api/slow /api/error"
echo "                   Prometheus: http://localhost:13001 is app; metrics :9464/metrics"
echo "                   PromQL: http_server_duration_count or express_demo_requests_total"
echo

exec tilt up --context "kind-${KIND_CLUSTER_NAME}" ${TILT_ARGS:-}
