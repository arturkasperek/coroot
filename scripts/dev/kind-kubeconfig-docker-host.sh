#!/usr/bin/env bash
# When the current Docker context talks over SSH (e.g. ssh://artur@server),
# kind's kubeconfig still points at 127.0.0.1 on that host. This script
# patches kind config / kubectl server URLs so the laptop can reach the
# cluster. unix:// (Docker Desktop, local sock) is a no-op — same as tcp.
set -euo pipefail

usage() {
  echo "usage: $0 [--docker-host URL] --print-remote-host | --patch-kind-config FILE | --rewrite-server-url URL" >&2
  exit 2
}

docker_host_flag=""
docker_host_set=0
print_remote=0
kind_config=""
rewrite_url=""

while [ $# -gt 0 ]; do
  case "$1" in
    --docker-host)
      docker_host_flag="${2:-}"
      docker_host_set=1
      shift 2
      ;;
    --print-remote-host)
      print_remote=1
      shift
      ;;
    --patch-kind-config)
      kind_config="${2:-}"
      shift 2
      ;;
    --rewrite-server-url)
      rewrite_url="${2:-}"
      shift 2
      ;;
    -h|--help)
      usage
      ;;
    *)
      usage
      ;;
  esac
done

inspect_docker_host() {
  docker context inspect -f '{{.Endpoints.docker.Host}}' 2>/dev/null || true
}

# Prints the SSH context hostname, or empty unless the endpoint is ssh://.
parse_docker_daemon_host() {
  local url="${1:-}"
  url="${url#"${url%%[![:space:]]*}"}"
  url="${url%"${url##*[![:space:]]}"}"
  [ -n "$url" ] || return 0

  local scheme="${url%%://*}"
  scheme="$(printf '%s' "$scheme" | tr '[:upper:]' '[:lower:]')"
  # docker context ls: unix:///… (Desktop / default) vs ssh://user@host.
  # Only SSH needs kind apiServerAddress / kubeconfig rewrite.
  [ "$scheme" = "ssh" ] || return 0

  local rest="${url#*://}"
  rest="${rest%%/*}"
  if [[ "$rest" == *@* ]]; then
    rest="${rest##*@}"
  fi
  # Drop :port (IPv4 / hostname). Leave IPv6 in brackets alone.
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

rewrite_loopback_url() {
  local server="$1" host="$2"
  if [ -z "$host" ]; then
    printf '%s' "$server"
    return 0
  fi
  printf '%s' "$server" | sed -E "s#^(https?://)(127\\.0\\.0\\.1|localhost|0\\.0\\.0\\.0|\\[::1\\])(:[0-9]+)#\\1${host}\\3#"
}

# SSH + remote dockerd: kind's client binds apiServerAddress on the
# laptop to pick a free port. The SSH host's LAN IP is not local, so
# we advertise 0.0.0.0 and put the SSH hostname in the API cert SAN.
# kubectl then uses --rewrite-server-url to replace 0.0.0.0/127.0.0.1.
patch_kind_config() {
  local file="$1" host="$2"
  if [ -z "$host" ]; then
    cat "$file"
    return 0
  fi
  awk -v host="$host" '
    BEGIN { addr=0; san=0 }
    /^[[:space:]]*apiServerAddress:/ {
      print "  apiServerAddress: 0.0.0.0"
      addr=1
      next
    }
    /^[[:space:]]*- role: control-plane[[:space:]]*$/ {
      print
      print "    kubeadmConfigPatches:"
      print "      - |"
      print "        kind: ClusterConfiguration"
      print "        apiServer:"
      print "          certSANs:"
      print "            - \"" host "\""
      print "            - \"127.0.0.1\""
      print "            - \"localhost\""
      print "            - \"0.0.0.0\""
      san=1
      next
    }
    { print }
    END {
      if (!addr) {
        print ""
        print "networking:"
        print "  apiServerAddress: 0.0.0.0"
      }
    }
  ' "$file"
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
remote_host="$(parse_docker_daemon_host "$host_url")"

if [ "$print_remote" -eq 1 ]; then
  printf '%s' "$remote_host"
elif [ -n "$kind_config" ]; then
  patch_kind_config "$kind_config" "$remote_host"
elif [ -n "$rewrite_url" ]; then
  rewrite_loopback_url "$rewrite_url" "$remote_host"
else
  usage
fi
