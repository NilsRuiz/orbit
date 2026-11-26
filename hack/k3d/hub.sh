#!/usr/bin/env bash
# The operator on a local k3d hub cluster, next to the docker compose fleet.
#   hub.sh up     create the cluster if needed, build and import the operator image,
#                 deploy it and wait until it is ready. Idempotent: re-run after a change.
#   hub.sh down   delete the cluster.
set -euo pipefail

cluster=orbit-hub
context=k3d-${cluster}
image=orbit-operator:dev
# Same Kubernetes minor as envtest (operator/Makefile), which validates the CRDs.
k3s_image=${K3S_IMAGE:-rancher/k3s:v1.37.2-k3s1}
root=$(cd "$(dirname "$0")/../.." && pwd)

up() {
  if ! k3d cluster get "$cluster" >/dev/null 2>&1; then
    k3d cluster create "$cluster" --image "$k3s_image" --servers 1 --agents 0 \
      --k3s-arg "--disable=traefik@server:*" --wait
  fi
  docker build -q -t "$image" "$root/operator" >/dev/null
  k3d image import "$image" --cluster "$cluster" >/dev/null
  # The manifests are the desired state of this local cluster: take over fields changed
  # by hand meanwhile (e.g. `kubectl scale`), instead of failing on the conflict.
  kubectl --context "$context" apply --server-side --force-conflicts -k "$root/hack/k3d/operator" >/dev/null
  # The tag does not change between builds: restart to pick up the new image.
  kubectl --context "$context" -n operator-system rollout restart deployment/operator-controller-manager >/dev/null
  kubectl --context "$context" -n operator-system rollout status deployment/operator-controller-manager --timeout=120s
  # On a new cluster, CoreDNS answers "no such host" for host.k3d.internal for about
  # 90 s (k3d injects the record afterwards). The operator retries by itself; wait for it
  # so the demo starts with the fleet registered.
  echo "waiting for the operator to reach the hub NATS (up to 3 min on a new cluster)..."
  local deadline=$((SECONDS + 180))
  until kubectl --context "$context" -n operator-system logs deployment/operator-controller-manager 2>/dev/null |
    grep -q '"msg":"nats connected"'; do
    if ((SECONDS > deadline)); then
      echo "operator not connected to NATS: is the compose fleet up (make dev-up)?" >&2
      exit 1
    fi
    sleep 2
  done
  echo "operator ready on context $context"
}

down() {
  k3d cluster delete "$cluster"
}

case "${1:-}" in
  up) up ;;
  down) down ;;
  *) echo "usage: $0 up|down" >&2; exit 2 ;;
esac
