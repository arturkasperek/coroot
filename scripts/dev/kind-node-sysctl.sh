#!/usr/bin/env bash
# kind nodes inherit tight inotify/nofile limits. A second cluster on the
# same Docker host (e.g. cohort-dev + coroot-dev) makes kube-proxy die with
# "too many open files", and NodePorts never bind.
set -euo pipefail

KIND_CLUSTER_NAME="${KIND_CLUSTER_NAME:-coroot-dev}"
node="${KIND_CLUSTER_NAME}-control-plane"

if ! docker inspect "$node" >/dev/null 2>&1; then
  echo "[dev] kind node $node not found" >&2
  exit 1
fi

echo "[dev] raising inotify limits on $node"
docker exec "$node" sysctl -w fs.inotify.max_user_watches=524288 >/dev/null
docker exec "$node" sysctl -w fs.inotify.max_user_instances=1024 >/dev/null
docker exec "$node" sysctl -w fs.inotify.max_queued_events=16384 >/dev/null

ready="$(kubectl -n kube-system get pods -l k8s-app=kube-proxy -o jsonpath='{.items[0].status.containerStatuses[0].ready}' 2>/dev/null || true)"
if [ "$ready" != "true" ]; then
  echo "[dev] restarting kube-proxy (was not ready)"
  kubectl -n kube-system delete pod -l k8s-app=kube-proxy --wait=false >/dev/null
  for i in $(seq 1 30); do
    ready="$(kubectl -n kube-system get pods -l k8s-app=kube-proxy -o jsonpath='{.items[0].status.containerStatuses[0].ready}' 2>/dev/null || true)"
    if [ "$ready" = "true" ]; then
      echo "[dev] kube-proxy ready after ${i}s"
      break
    fi
    sleep 1
  done
fi
