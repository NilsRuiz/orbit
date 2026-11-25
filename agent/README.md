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

## Development

Module path: `github.com/NilsRuiz/orbit/agent`.

```bash
go test -race ./...  # unit tests + integration tests against in-process NATS servers
go mod tidy          # after changing dependencies; commit go.mod and go.sum
```

`TestConvergenceAfterLeafLinkCut` reproduces the edge topology (agent → leaf → hub) with an
in-process TCP proxy as the "space link". It bounds convergence to 20 s after a 12 s
leaf↔hub cut, while the agent's own connection to its leaf stays open (#16). It takes
about 20 s.

## Configuration

| Variable | Default | Description |
|---|---|---|
| `ORBIT_NODE_ID` | — (required) | Node identity, e.g. `sat-1`. Must be a DNS-1123 label: it names the `EdgeNode` and is a NATS subject token |
| `ORBIT_NATS_URL` | `nats://127.0.0.1:4222` | Local leaf node |
| `ORBIT_NATS_CREDS` | — | NATS credentials file (phase 3) |
| `ORBIT_JS_DOMAIN` | `hub` | Hub JetStream domain, reached through the leaf node |
| `ORBIT_STATE_DIR` | `/var/lib/orbit` | Where `state.json` lives |
| `ORBIT_INITIAL_VERSION` | `1.0.0` | Version assumed on first boot |
| `ORBIT_HEARTBEAT_INTERVAL` | `10s` | |
| `ORBIT_VERIFY_TIMEOUT` | `30s` | Dead man's switch deadline |
| `ORBIT_SIM_BAD_VERSIONS` | — | Comma-separated versions the simulator reports unhealthy |
| `ORBIT_NODE_LABELS` | — | `orbit.io/<name>=<value>` pairs, comma-separated (e.g. `orbit.io/constellation=alpha`). Sent in heartbeats; the operator mirrors them on the `EdgeNode` and `Fleet`s select on them (ADR-0007). Other prefixes are refused at start-up |

## Messages

The agent publishes on the hub through its leaf node (ADR-0002). These payloads are a
contract with the operator, which decodes them in another module; tests pin the field
names (`internal/transport/heartbeat_integration_test.go`).

**Heartbeat**: `orbit.edge.<node>.heartbeat`, core NATS, every `ORBIT_HEARTBEAT_INTERVAL`.

```json
{
  "nodeId": "sat-1",
  "current": "1.2.0",
  "phase": "idle",
  "rolloutId": "r-42",
  "labels": {"orbit.io/constellation": "alpha"},
  "time": "2026-11-25T10:00:00Z"
}
```

- `phase`: `idle`, `applying`, `verifying` or `rolled-back`.
- `rolloutId`: omitted when empty.
- `labels`: always an object, `{}` when none are configured.
- `time`: agent clock, informative only. The operator uses its own reception time for
  `lastSeen`, because a node's clock may drift.

**Status**: `orbit.edge.<node>.status`, stream `ORBIT_STATUS`, one event per state
transition (`reconcile.Status`).

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
- [ ] **Silent mode for the test's `linkProxy`** (`pause()`/`resume()`: stop copying bytes,
      close nothing). It would give a deterministic regression test for #35: a KV watch
      stuck when the first message after a consumer reset is lost, fixed by nats.go
      v1.56.0 in #36. Today that fix is backed only by fleet runs
      (`docs/investigations/2026-11-silent-link-loss.md`). Deferred from #31 (step C).
- [ ] **Faster convergence after a clean cut** (#16): 4.7–14.7 s measured, target ≤ 3 s.
