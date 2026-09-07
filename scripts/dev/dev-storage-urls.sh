#!/usr/bin/env bash
# Print shell assignments for Prometheus / ClickHouse endpoints.
# With DEV_REMOTE_HOST, NodePorts bind on that host, not laptop loopback.
set -euo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck disable=SC1091
source "$DIR/load-env.sh"

host="${DEV_REMOTE_HOST:-127.0.0.1}"

printf 'export COROOT_DEV_PROMETHEUS_URL=%q\n' "http://${host}:9090"
printf 'export COROOT_DEV_CLICKHOUSE_ADDRESS=%q\n' "${host}:9000"
printf 'export COROOT_DEV_CLICKHOUSE_HTTP=%q\n' "http://${host}:8123"
