#!/usr/bin/env bash
# Simulate the "space link" between an edge node and the hub.
#
#   link.sh cut <node>        clean cut: leaf disconnected from the hub network; both ends
#                             see the connection drop at once
#   link.sh blackhole <node>  silent loss, like a real radio link: every packet between the
#                             leaf and the hub is dropped in both directions, sockets stay
#                             open and nobody is told (only pings reveal it)
#   link.sh restore <node>    back in range, link healthy (undoes cut, blackhole, degrade)
#   link.sh degrade <node>    in range but bad link: latency, jitter, packet loss
set -euo pipefail

action=${1:?usage: link.sh cut|blackhole|restore|degrade <node>}
node=${2:?missing node, e.g. sat-2}
leaf="orbit-leaf-${node}"
net="orbit-net"

# Runs a shell snippet inside the leaf's network namespace. The snippet can use $hub (the
# hub's IP) and $dev (the leaf interface that routes to the hub).
in_leaf_netns() {
  local hub_ip
  hub_ip=$(docker inspect -f "{{(index .NetworkSettings.Networks \"${net}\").IPAddress}}" orbit-hub)
  docker run --rm --net "container:${leaf}" --cap-add NET_ADMIN nicolaka/netshoot \
    sh -c "hub=${hub_ip}; dev=\$(ip route get \$hub | awk '{for(i=1;i<=NF;i++) if(\$i==\"dev\") print \$(i+1)}'); $1"
}

# shellcheck disable=SC2016 # $hub and $dev are expanded inside the netshoot container on purpose
case "$action" in
  cut)
    docker network disconnect "$net" "$leaf"
    echo "🛰️  ${node}: link cut"
    ;;
  blackhole)
    # iptables rather than `tc netem loss 100%`: tc only shapes egress, so the hub could
    # still push to the leaf (a one-way cut). -C first keeps the action idempotent.
    in_leaf_netns '
      iptables -C INPUT  -s $hub -j DROP 2>/dev/null || iptables -I INPUT  -s $hub -j DROP
      iptables -C OUTPUT -d $hub -j DROP 2>/dev/null || iptables -I OUTPUT -d $hub -j DROP'
    echo "🛰️  ${node}: link blackholed (all packets to/from the hub dropped, sockets left open)"
    ;;
  restore)
    docker network connect "$net" "$leaf" 2>/dev/null || true
    in_leaf_netns '
      while iptables -D INPUT  -s $hub -j DROP 2>/dev/null; do :; done
      while iptables -D OUTPUT -d $hub -j DROP 2>/dev/null; do :; done
      tc qdisc del dev $dev root 2>/dev/null || true'
    echo "🛰️  ${node}: link restored"
    ;;
  degrade)
    in_leaf_netns 'tc qdisc replace dev $dev root netem delay 800ms 200ms loss 20%'
    echo "🛰️  ${node}: link degraded (800ms ±200ms, 20% loss)"
    ;;
  *)
    echo "unknown action: $action" >&2
    exit 1
    ;;
esac
