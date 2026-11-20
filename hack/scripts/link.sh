#!/usr/bin/env bash
# Simulate the "space link" between an edge node and the hub.
#
#   link.sh cut <node>       satellite out of range (leaf disconnected from the hub)
#   link.sh restore <node>   back in range, link healthy
#   link.sh degrade <node>   in range but bad link: latency, jitter, packet loss
set -euo pipefail

action=${1:?usage: link.sh cut|restore|degrade <node>}
node=${2:?missing node, e.g. sat-2}
leaf="orbit-leaf-${node}"
net="orbit-net"

netem() {
  # Runs tc inside the leaf's network namespace, on the interface that routes to the hub.
  local hub_ip
  hub_ip=$(docker inspect -f "{{(index .NetworkSettings.Networks \"${net}\").IPAddress}}" orbit-hub)
  docker run --rm --net "container:${leaf}" --cap-add NET_ADMIN nicolaka/netshoot \
    sh -c "dev=\$(ip route get ${hub_ip} | awk '{for(i=1;i<=NF;i++) if(\$i==\"dev\") print \$(i+1)}'); $1"
}

# shellcheck disable=SC2016 # $dev is expanded inside the netshoot container on purpose
case "$action" in
  cut)
    docker network disconnect "$net" "$leaf"
    echo "🛰️  ${node}: link cut"
    ;;
  restore)
    docker network connect "$net" "$leaf" 2>/dev/null || true
    netem 'tc qdisc del dev $dev root 2>/dev/null || true'
    echo "🛰️  ${node}: link restored"
    ;;
  degrade)
    netem 'tc qdisc replace dev $dev root netem delay 800ms 200ms loss 20%'
    echo "🛰️  ${node}: link degraded (800ms ±200ms, 20% loss)"
    ;;
  *)
    echo "unknown action: $action" >&2
    exit 1
    ;;
esac
