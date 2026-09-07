#!/usr/bin/env bash
# Point Tilt at KUBERNETES_CONTEXT_NAME from .env. Does not install or
# start the cluster — k3s must already be reachable.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$ROOT"

bash "$DIR/k8s-dev-tools.sh"
# shellcheck disable=SC1091
source "$DIR/load-env.sh"

bash "$DIR/check-docker-remote.sh"

if ! kubectl config get-contexts -o name | grep -Fxq "$KUBERNETES_CONTEXT_NAME"; then
  echo "[dev] kubectl context '$KUBERNETES_CONTEXT_NAME' not found" >&2
  kubectl config get-contexts >&2 || true
  exit 1
fi

if ! kubectl --context "$KUBERNETES_CONTEXT_NAME" get nodes >/dev/null; then
  echo "[dev] context '$KUBERNETES_CONTEXT_NAME' is not reachable" >&2
  exit 1
fi

echo "[dev] using kubectl context $KUBERNETES_CONTEXT_NAME"
if [ -n "${DEV_REMOTE_HOST:-}" ]; then
  echo "[dev] image import via ssh $DEV_REMOTE_HOST"
else
  echo "[dev] image import on local k3s"
fi

echo
echo "  [dev] starting Tilt — edit files locally; backend/frontend reload in-cluster."
echo "    context:       $KUBERNETES_CONTEXT_NAME"
echo "    Tilt UI:  http://localhost:10350"
echo "    Coroot:        http://localhost:18080"
echo "    Postgres:      localhost:15432  user/db/password coroot  (config DB, not telemetry)"
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
echo "                            (node-agent + cluster-agent remote-write)"
echo "    Next.js demo:  http://localhost:13000  (calls Express)"
echo "    Express demo:  http://localhost:13001  /api/hello /api/slow /api/error"
echo "                   OTEL Prometheus scrape: pod :9464/metrics (cluster-agent)"
if AGENT_SRC="$(bash "$DIR/node-agent-local-dir.sh")"; then
  echo "    Node agent:    local $AGENT_SRC (Tilt docker_build)"
else
  echo "    Node agent:    ghcr.io/coroot/coroot-node-agent:latest"
fi
echo

export KUBERNETES_CONTEXT_NAME
exec tilt up --context "$KUBERNETES_CONTEXT_NAME" ${TILT_ARGS:-}
