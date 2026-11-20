#!/usr/bin/env bash
# Create a local hub + N edge k3d clusters on one Docker network.
set -euo pipefail

edges=${1:-2}
network=orbit-k3d

k3d cluster create orbit-hub --network "$network" --servers 1 --agents 1 \
  --k3s-arg "--disable=traefik@server:*" --wait

for i in $(seq 1 "$edges"); do
  k3d cluster create "orbit-sat-${i}" --network "$network" --servers 1 \
    --k3s-arg "--disable=traefik@server:*" \
    --k3s-arg "--node-label=orbit.io/node-id=sat-${i}@server:*" --wait
done

echo "contexts: k3d-orbit-hub, k3d-orbit-sat-1..${edges}"
