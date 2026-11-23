# orbit-agent

Runs on every edge node. Watches the node's key in the `orbit-desired` JetStream KV bucket,
converges the payload to that version, verifies it, and **rolls back locally** if the new
version is not healthy before the dead man's switch fires (ADR-0004).

```
cmd/orbit-agent/      entrypoint, wiring, heartbeat loop
internal/config/      env-based configuration
internal/state/       A/B state persisted atomically on disk (survives reboots/preemption)
internal/reconcile/   state machine: idle → applying → verifying → idle | rolled-back
internal/transport/   NATS: KV watch, status to JetStream, heartbeats
internal/payload/     payload drivers (Sim today, k3s/Flux in phase 3)
```

## First time

The module path `github.com/orbit-fleet/orbit/agent` is a placeholder: replace it with your
own (`github.com/<you>/orbit/agent`) everywhere, then:

```bash
go mod tidy          # generates go.sum — commit it
go get -u ./... && go mod tidy   # optional: bump to latest nats.go / nats-server (newer
                                 # releases may require a newer Go version)
go test -race ./...  # unit tests + integration tests against in-process NATS servers
```

`TestConvergenceAfterLeafLinkCut` reproduces the edge topology (agent → leaf → hub) with an
in-process TCP proxy as the "space link". It bounds convergence to 20 s after a 12 s
leaf↔hub cut, while the agent's own connection to its leaf stays open (#16). It takes
about 20 s.

## Configuration

| Variable | Default | Description |
|---|---|---|
| `ORBIT_NODE_ID` | — (required) | Node identity, e.g. `sat-1` |
| `ORBIT_NATS_URL` | `nats://127.0.0.1:4222` | Local leaf node |
| `ORBIT_NATS_CREDS` | — | NATS credentials file (phase 3) |
| `ORBIT_JS_DOMAIN` | `hub` | Hub JetStream domain, reached through the leaf node |
| `ORBIT_STATE_DIR` | `/var/lib/orbit` | Where `state.json` lives |
| `ORBIT_INITIAL_VERSION` | `1.0.0` | Version assumed on first boot |
| `ORBIT_HEARTBEAT_INTERVAL` | `10s` | |
| `ORBIT_VERIFY_TIMEOUT` | `30s` | Dead man's switch deadline |
| `ORBIT_SIM_BAD_VERSIONS` | — | Comma-separated versions the simulator reports unhealthy |

## Logs

JSON on stdout (`log/slog`). NATS connection events worth alerting on:

| `msg` | Level | Meaning |
|---|---|---|
| `nats disconnected` / `nats reconnected` | warn / info | Agent ↔ local leaf connection (not the space link) |
| `nats async error` | warn | Error reported asynchronously by nats.go, with `err`, plus `subject` and `dropped` when it concerns a subscription: slow consumer, permission violation, inactive JetStream consumer |

## Known gaps (good next tasks)

- [ ] **Status store-and-forward**: today a status published while the hub link is down is
      lost (heartbeats re-sync the current version). Fix: enable JetStream on the leaf node
      with its own domain and *source* a local status stream into the hub's `ORBIT_STATUS`.
- [ ] **K3s payload driver**: patch the local Flux `OCIRepository` tag, check Deployment readiness.
- [ ] **Metrics**: Prometheus endpoint (`orbit_agent_phase`, `orbit_agent_rollbacks_total`).
- [ ] Verify KV watch recovery after long link cuts (chaos test, phase 5). A clean cut
      (connections closed) is covered by `TestConvergenceAfterLeafLinkCut`. Still open: a
      silent cut (packets dropped, sockets left open) and a degraded link.
