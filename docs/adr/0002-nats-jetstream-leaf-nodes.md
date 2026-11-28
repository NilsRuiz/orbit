# ADR-0002: NATS JetStream with leaf nodes for hub ↔ edge communication

- **Status**: Accepted
- **Date**: 2026-11-20

## Context

Edge nodes are reachable only intermittently. The hub must be able to express a desired
state that is delivered **when the node comes back**, and nodes must report status that
survives link outages. Local applications on a node must keep working while offline.

## Decision

- A NATS server with **JetStream** on the hub.
- A **NATS leaf node** on every edge node; local processes (agent, payload) talk to
  `localhost:4222`, the leaf node bridges to the hub when the link is up.
- Desired state lives in a JetStream **KV bucket** `orbit-desired` (one key per node):
  the agent *watches* its key and always converges to the latest value.
- Status events go to the stream `ORBIT_STATUS` (`orbit.edge.<node>.status`).
- Liveness uses core NATS heartbeats on `orbit.edge.<node>.heartbeat` (fire-and-forget).

Subject / key conventions:

| Name | Type | Producer → consumer |
|---|---|---|
| `orbit-desired` / `<node>` | KV | operator → agent |
| `orbit.edge.<node>.status` | JetStream stream `ORBIT_STATUS` | agent → operator |
| `orbit.edge.<node>.heartbeat` | core NATS | agent → operator |

## Alternatives considered

- **gRPC streaming** — full control, but reconnection, buffering, replay and
  at-least-once delivery would all be hand-written.
- **MQTT** — familiar in IoT, but persistence/replay semantics depend on the broker and
  there is no equivalent to leaf nodes for local autonomy.
- **Kafka** — far too heavy for edge hardware.
- **Polling an HTTP API** — simplest, but wastes the short contact windows and gives no
  push semantics.

## Consequences

- ➕ Store-and-forward, replay and reconnection come for free; less custom code.
- ➕ KV "latest value wins" semantics match desired-state reconciliation exactly.
- ➖ NATS becomes critical infrastructure on the hub: needs monitoring, persistence and
  an SLO of its own.
- ➖ Security (accounts, NKeys/JWT credentials per node) must be designed — tracked for
  phase 3.
  *2026-11-28*: designed in [ADR-0010](0010-nats-per-node-credentials.md) (JWT
  authentication, per-node users and permissions).
- ➖ *2026-11-23*: a desired state published during a link cut reaches the agent 10–15 s
  after the link is restored (KV watch recovery timer) — see
  [investigation](../investigations/2026-11-link-restore-delay.md), tracked in #16.
