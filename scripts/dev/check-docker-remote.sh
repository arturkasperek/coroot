#!/usr/bin/env bash
# Guard: when DEV_REMOTE_HOST is set, docker build must target that host
# (ssh:// or tcp://), not a local Mac VM. Otherwise docker save on the k3s
# machine would not see the image Tilt just built.
set -euo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"

docker_host_flag=""
docker_host_set=0
while [ $# -gt 0 ]; do
  case "$1" in
    --docker-host)
      docker_host_flag="${2:-}"
      docker_host_set=1
      shift 2
      ;;
    -h|--help)
      echo "usage: $0 [--docker-host URL]" >&2
      exit 2
      ;;
    *)
      echo "usage: $0 [--docker-host URL]" >&2
      exit 2
      ;;
  esac
done

# shellcheck disable=SC1091
source "$DIR/load-env.sh"

if [ -z "${DEV_REMOTE_HOST:-}" ]; then
  exit 0
fi

inspect_docker_host() {
  docker context inspect -f '{{.Endpoints.docker.Host}}' 2>/dev/null || true
}

# Hostname from ssh://user@host or tcp://host:port. Empty for unix:// / loopback.
docker_remote_host() {
  local url="${1:-}"
  url="${url#"${url%%[![:space:]]*}"}"
  url="${url%"${url##*[![:space:]]}"}"
  [ -n "$url" ] || return 0

  local scheme="${url%%://*}"
  scheme="$(printf '%s' "$scheme" | tr '[:upper:]' '[:lower:]')"
  case "$scheme" in
    ssh|tcp) ;;
    *) return 0 ;;
  esac

  local rest="${url#*://}"
  rest="${rest%%/*}"
  if [[ "$rest" == *@* ]]; then
    rest="${rest##*@}"
  fi
  if [[ "$rest" == \[* ]]; then
    rest="${rest#\[}"
    rest="${rest%%]*}"
  else
    rest="${rest%%:*}"
  fi

  case "$rest" in
    ""|127.0.0.1|localhost|::1) return 0 ;;
  esac
  printf '%s' "$rest"
}

host_url=""
if [ "$docker_host_set" -eq 1 ]; then
  host_url="$docker_host_flag"
else
  host_url="$(inspect_docker_host)"
  if [ -z "$host_url" ]; then
    host_url="${DOCKER_HOST:-}"
  fi
fi

actual="$(docker_remote_host "$host_url")"
if [ "$actual" = "$DEV_REMOTE_HOST" ]; then
  exit 0
fi

echo "[dev] DEV_REMOTE_HOST=$DEV_REMOTE_HOST but docker is not talking to that host." >&2
echo "[dev]   docker host: ${host_url:-<unset>}" >&2
echo "[dev]   parsed host: ${actual:-<local/unix>}" >&2
echo "[dev] docker build would land here, then ssh ${DEV_REMOTE_HOST} 'docker save' would miss the image." >&2
echo "[dev] point docker context at ssh://${DEV_REMOTE_HOST} (or tcp://), or clear DEV_REMOTE_HOST for local k3s." >&2
exit 1
