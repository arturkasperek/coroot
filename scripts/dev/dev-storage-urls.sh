#!/usr/bin/env bash
# Print shell assignments for Prometheus / ClickHouse endpoints.
# When Docker talks over SSH, kind extraPortMappings bind on that host,
# not on the laptop loopback.
set -euo pipefail

docker_host_args=()
if [ "${1:-}" = "--docker-host" ]; then
  docker_host_args=(--docker-host "${2:-}")
fi

DIR="$(cd "$(dirname "$0")" && pwd)"
if [ ${#docker_host_args[@]} -gt 0 ]; then
  remote="$("$DIR/kind-kubeconfig-docker-host.sh" "${docker_host_args[@]}" --print-remote-host)"
else
  remote="$("$DIR/kind-kubeconfig-docker-host.sh" --print-remote-host)"
fi
host="${remote:-127.0.0.1}"

printf 'export COROOT_DEV_PROMETHEUS_URL=%q\n' "http://${host}:9090"
printf 'export COROOT_DEV_CLICKHOUSE_ADDRESS=%q\n' "${host}:9000"
printf 'export COROOT_DEV_CLICKHOUSE_HTTP=%q\n' "http://${host}:8123"
