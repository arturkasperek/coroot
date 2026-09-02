#!/usr/bin/env bash
# Point kubectl at the kind make-dev cluster without changing the
# caller's current kubectl context (often EKS).
#
# kind export --kubeconfig FILE writes a dedicated kubeconfig. Subsequent
# tools inherit KUBECONFIG so installs cannot land in whatever cluster
# `kubectl` was last using.
set -euo pipefail

KIND_CLUSTER_NAME="${KIND_CLUSTER_NAME:-coroot-dev}"
KIND_KUBECONFIG="${KIND_KUBECONFIG:-/tmp/kind-${KIND_CLUSTER_NAME}.kubeconfig}"
CONTEXT="kind-${KIND_CLUSTER_NAME}"

print_path() {
  printf '%s\n' "$KIND_KUBECONFIG"
}

print_export() {
  printf 'export KUBECONFIG=%q\n' "$KIND_KUBECONFIG"
}

rewrite_cluster() {
  local kubeconfig="${1:-}"
  local kc_args=()
  if [ -n "$kubeconfig" ]; then
    kc_args=(--kubeconfig "$kubeconfig")
  fi
  local cur new
  cur="$(kubectl "${kc_args[@]}" config view --raw -o jsonpath="{.clusters[?(@.name==\"${CONTEXT}\")].cluster.server}" 2>/dev/null || true)"
  if [ -z "$cur" ]; then
    return 0
  fi
  new="$(bash "$(cd "$(dirname "$0")" && pwd)/kind-kubeconfig-docker-host.sh" --rewrite-server-url "$cur")"
  if [ -n "$new" ] && [ "$new" != "$cur" ]; then
    echo "[dev] pointing ${CONTEXT} at ${new} (SSH Docker context)" >&2
    kubectl "${kc_args[@]}" config set-cluster "$CONTEXT" --server="$new" >/dev/null
  fi
}

write_kubeconfig() {
  kind export kubeconfig --name "$KIND_CLUSTER_NAME" --kubeconfig "$KIND_KUBECONFIG"
  rewrite_cluster "$KIND_KUBECONFIG"
  # `kind create` / `kind export` also write 0.0.0.0 into ~/.kube/config.
  # Patch that copy so `kubectl config use-context kind-coroot-dev` works
  # from a normal shell (no KUBECONFIG=/tmp/...).
  if [ -f "${HOME}/.kube/config" ]; then
    rewrite_cluster "${HOME}/.kube/config"
  fi
}

case "${1:-}" in
  --print-path)
    print_path
    ;;
  --print-export)
    print_export
    ;;
  --export)
    write_kubeconfig
    print_export
    ;;
  *)
    echo "usage: $0 --print-path | --print-export | --export" >&2
    exit 2
    ;;
esac
