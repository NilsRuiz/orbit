#!/usr/bin/env bash
# The local fleet on k3d, everything from GitOps: a hub cluster (gitops/clusters/hub) and
# three satellites (gitops/clusters/edge) on one Docker network.
#
#   fleet.sh up                    create or update the fleet (idempotent)
#   fleet.sh down                  delete it
#   fleet.sh link-cut <node>       silently drop the node's traffic to the hub's leaf port
#   fleet.sh link-restore <node>   restore it
#
# SOURCE=github (default): the clusters pull main from GitHub, as in the cloud.
# SOURCE=local: they pull this working tree, served from a local OCI registry, to try a
#   GitOps change before merging it. Re-run `up` to publish new changes (Flux polls 1m).
set -euo pipefail

K3S_IMAGE=rancher/k3s:v1.37.2-k3s1 # Kubernetes minor of envtest and the hub (hub.sh)
FLUX_CLI=ghcr.io/fluxcd/flux-cli:v2.10.1 # same Flux as gitops/flux
NET=orbit-fleet
HUB=orbit-fleet-hub
SATS=(sat-1 sat-2 sat-3)
# Same constellations as the docker compose fleet (hack/compose).
declare -A LABELS=([sat-1]=orbit.io/constellation=alpha [sat-2]=orbit.io/constellation=alpha
  [sat-3]=orbit.io/constellation=beta)
REGISTRY=orbit-fleet-registry
REGISTRY_PORT=5098
SOURCE=${SOURCE:-github}
LINK_RULE=orbit-fleet-link-cut

root=$(cd "$(dirname "$0")/../.." && pwd)
log() { echo "==> $*"; }
kc() { kubectl --context "k3d-$1" "${@:2}"; }

cluster_up() { # cluster_up <name>
  if k3d cluster get "$1" >/dev/null 2>&1; then
    return
  fi
  log "cluster $1"
  k3d cluster create "$1" --image "$K3S_IMAGE" --network "$NET" \
    --k3s-arg "--disable=traefik@server:*" --wait >/dev/null
}

# localize rewrites a copy of the repository for SOURCE=local: the flux-system source is
# the local registry's OCIRepository instead of GitHub. Nothing else changes.
localize() { # localize <copy root>
  python3 - "$1" "oci://$REGISTRY:5000/orbit-gitops" <<'EOF2'
import pathlib, re, sys
root, url = pathlib.Path(sys.argv[1]), sys.argv[2]
for sync in root.glob("gitops/clusters/*/flux-system/gotk-sync.yaml"):
    s = sync.read_text()
    s, n = re.subn(
        r"kind: GitRepository\n(metadata:\n  name: flux-system\n  namespace: flux-system\nspec:\n  interval: 1m\n)"
        r"  url: \S+\n  ref:\n    branch: main\n",
        lambda m: f"kind: OCIRepository\n{m.group(1)}  url: {url}\n  insecure: true\n  ref:\n    tag: local\n", s)
    if n != 1:
        sys.exit(f"{sync}: GitRepository flux-system not found")
    sync.write_text(s)
for p in (root / "gitops/clusters").rglob("*.yaml"):
    t = p.read_text()
    p.write_text(t.replace("    kind: GitRepository\n    name: flux-system", "    kind: OCIRepository\n    name: flux-system"))
EOF2
}

# copy_tree copies what the clusters fetch: gitops/, and operator/config built by the hub.
copy_tree() { # copy_tree <dest>
  mkdir -p "$1/operator"
  cp -r "$root/gitops" "$1/"
  cp -r "$root/operator/config" "$1/operator/"
  localize "$1"
}

# publish_local pushes the working tree, as the clusters fetch it, to the local registry.
publish_local() {
  if ! docker container inspect "$REGISTRY" >/dev/null 2>&1; then
    # Restarted with Docker, like the k3d clusters: they fetch their source from it.
    docker run -d --name "$REGISTRY" --restart unless-stopped --network "$NET" \
      -p "127.0.0.1:$REGISTRY_PORT:5000" registry:2 >/dev/null
  fi
  local src
  src=$(mktemp -d)
  copy_tree "$src"
  chmod -R a+rX "$src" # the flux CLI container runs as another user
  # Mounted under /tmp: flux writes its temporary archive next to the source path.
  docker run --rm --network host -v "$src:/tmp/src:ro" "$FLUX_CLI" push artifact \
    "oci://localhost:$REGISTRY_PORT/orbit-gitops:local" --path /tmp/src --source local \
    --revision "local@sha1:$(git -C "$root" rev-parse HEAD)" >/dev/null
  rm -rf "$src"
  log "working tree published to the local registry"
}

# bootstrap applies a cluster's flux-system, the single manual apply (gitops/README.md).
bootstrap() { # bootstrap <cluster> <hub|edge>
  local dir="$root/gitops/clusters/$2/flux-system" copy=""
  if [ "$SOURCE" = local ]; then
    copy=$(mktemp -d)
    copy_tree "$copy"
    dir="$copy/gitops/clusters/$2/flux-system"
  fi
  # The first pass creates the Flux CRDs; the sync objects need them registered.
  kc "$1" apply --server-side --force-conflicts -k "$dir" >/dev/null 2>&1 ||
    kc "$1" apply --server-side --force-conflicts -k "$dir" >/dev/null
  [ -z "$copy" ] || rm -rf "$copy"
}

wait_ready() { # wait_ready <cluster> <kustomization>...
  local c=$1 ks
  shift
  for ks in "$@"; do
    kc "$c" -n flux-system wait kustomization "$ks" --for=condition=Ready --timeout=10m >/dev/null ||
      { echo "kustomization $ks not ready on $c" >&2; kc "$c" -n flux-system get kustomizations >&2; exit 1; }
  done
}

configmap() { # configmap <cluster> <name> <key=value>...
  local c=$1 name=$2
  shift 2
  kc "$c" create namespace flux-system --dry-run=client -o yaml | kc "$c" apply --server-side -f - >/dev/null
  kc "$c" -n flux-system create configmap "$name" "${@/#/--from-literal=}" --dry-run=client -o yaml |
    kc "$c" apply --server-side --force-conflicts -f - >/dev/null
}

# hub_leaf_ip is the hub leaf port's current address, for the link-cut rules.
hub_leaf_ip() {
  kc "$HUB" -n nats get service nats-leafnodes -o jsonpath='{.status.loadBalancer.ingress[0].ip}'
}

# check_inotify refuses to start with a low inotify instance limit. Every k3s cluster uses
# several, the limit is per user (root for all of them), and when it runs out a cluster's
# containerd never starts its CRI service ("failed to create fsnotify watcher: too many
# open files"): the cluster waits forever with no node.
check_inotify() {
  local want=512 have
  have=$(sysctl -n fs.inotify.max_user_instances 2>/dev/null) || return 0 # not Linux
  if [ "$have" -lt "$want" ]; then
    echo "fs.inotify.max_user_instances is $have: four k3s clusters need about $want." >&2
    echo "  sudo sysctl fs.inotify.max_user_instances=$want" >&2
    echo "  (to keep it: echo 'fs.inotify.max_user_instances=$want' | sudo tee /etc/sysctl.d/99-inotify.conf)" >&2
    exit 1
  fi
}

up() {
  check_inotify
  docker network inspect "$NET" >/dev/null 2>&1 || docker network create "$NET" >/dev/null
  if [ "$SOURCE" = local ]; then publish_local; fi

  cluster_up "$HUB"
  # Satellites reach the hub's leaf port from the Docker network, not the GCP edge subnet.
  configmap "$HUB" orbit-hub "LEAF_SOURCE_RANGE=$(docker network inspect "$NET" -f '{{(index .IPAM.Config 0).Subnet}}')"
  bootstrap "$HUB" hub
  log "hub: waiting for NATS and the operator"
  wait_ready "$HUB" flux-system nats-hub operator
  # The leaf port is served on the hub's node (k3s service load balancer), at its IP on the
  # Docker network: pods do not resolve container names. A Docker restart may change that
  # IP: running `up` again refreshes it on every satellite.
  local ip=""
  for _ in $(seq 60); do
    ip=$(hub_leaf_ip) && [ -n "$ip" ] && break
    sleep 2
  done
  [ -n "$ip" ] || { echo "hub leaf service has no address" >&2; exit 1; }
  local hub_leaf="$ip:7422"
  log "hub leaf port: $hub_leaf"

  local sat
  for sat in "${SATS[@]}"; do
    cluster_up "orbit-fleet-$sat"
    configmap "orbit-fleet-$sat" orbit-node "NODE_ID=$sat" "NODE_LABELS=${LABELS[$sat]}" \
      "HUB_LEAF_URL=nats-leaf://$hub_leaf"
    bootstrap "orbit-fleet-$sat" edge
    # Substitute the possibly new HUB_LEAF_URL now rather than at the next interval.
    kc "orbit-fleet-$sat" -n flux-system annotate --overwrite kustomization nats-leaf \
      "reconcile.fluxcd.io/requestedAt=$(date +%s)" >/dev/null 2>&1 || true
  done
  for sat in "${SATS[@]}"; do
    log "$sat: waiting for the leaf node, the payload and the agent"
    wait_ready "orbit-fleet-$sat" flux-system nats-leaf payload agent
  done
  # Reachable, not just registered: an EdgeNode outlives its satellite's link.
  log "waiting for the satellites to be reachable from the hub"
  for sat in "${SATS[@]}"; do
    local reach=""
    for _ in $(seq 90); do
      # Not found yet is normal: the agent registers once its pod runs (|| true under set -e).
      reach=$(kc "$HUB" get edgenode "$sat" -o jsonpath='{.status.reachability}' 2>/dev/null || true)
      [ "$reach" = Reachable ] && break
      sleep 2
    done
    [ "$reach" = Reachable ] || { echo "$sat is not reachable from the hub" >&2; exit 1; }
  done
  kc "$HUB" get edgenodes -o wide
}

down() {
  local c
  for c in "$HUB" "${SATS[@]/#/orbit-fleet-}"; do
    k3d cluster delete "$c" >/dev/null 2>&1 || true
  done
  docker rm -f "$REGISTRY" >/dev/null 2>&1 || true
  docker network rm "$NET" >/dev/null 2>&1 || true
  log "fleet deleted"
}

# The satellite's node container drops its traffic to and from the hub's leaf port: a
# silent radio loss, like `make link-blackhole` in docker compose. The leaf's pings detect
# it (ADR-0006); the agent keeps its local leaf, and kubectl still reaches the satellite.
link() { # link <cut|restore> <node>
  local node=$2 ip
  [[ " ${SATS[*]} " == *" $node "* ]] || { echo "unknown node: $node (${SATS[*]})" >&2; exit 2; }
  ip=$(hub_leaf_ip)
  local box="k3d-orbit-fleet-$node-server-0"
  local rules=("-d $ip -p tcp --dport 7422" "-s $ip -p tcp --sport 7422")
  local r
  for r in "${rules[@]}"; do
    # shellcheck disable=SC2086 # the rule is a list of iptables arguments
    while docker exec "$box" iptables -D FORWARD $r -m comment --comment "$LINK_RULE" -j DROP 2>/dev/null; do :; done
    if [ "$1" = cut ]; then
      # shellcheck disable=SC2086
      docker exec "$box" iptables -I FORWARD $r -m comment --comment "$LINK_RULE" -j DROP
    fi
  done
  if [ "$1" = cut ]; then log "$node: link cut (traffic to $ip:7422 silently dropped)"; else log "$node: link restored"; fi
}

case ${1:-} in
  up) up ;;
  down) down ;;
  link-cut) link cut "${2:?node}" ;;
  link-restore) link restore "${2:?node}" ;;
  *) echo "usage: $0 up|down|link-cut <node>|link-restore <node>" >&2; exit 2 ;;
esac
