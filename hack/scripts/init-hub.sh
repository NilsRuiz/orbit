#!/usr/bin/env bash
# Creates what the operator will own later: the desired-state KV bucket and status stream.
# Idempotent.
set -euo pipefail

nats() {
  docker run --rm --network orbit-net natsio/nats-box:latest nats --server nats://hub:4222 "$@"
}

echo "waiting for hub..."
for _ in $(seq 1 30); do
  if nats server check connection >/dev/null 2>&1; then break; fi
  sleep 1
done

nats kv info orbit-desired >/dev/null 2>&1 || nats kv add orbit-desired --history 5
nats stream info ORBIT_STATUS >/dev/null 2>&1 || \
  nats stream add ORBIT_STATUS --subjects 'orbit.edge.*.status' \
    --storage file --retention limits --max-age 7d --defaults

echo "hub ready: KV orbit-desired, stream ORBIT_STATUS"
