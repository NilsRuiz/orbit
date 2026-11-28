# ADR-0007: EdgeNodes register themselves through heartbeats

- **Status**: Accepted
- **Date**: 2026-11-25

## Context

The operator selects nodes for rollouts through `EdgeNode` objects and their labels
(`Fleet.spec.selector`, ADR-0004). The fleet changes at runtime: satellites are added,
replaced, or drop out of range for long periods. Two signals reach the hub (ADR-0002):

- **core NATS heartbeats** (`orbit.edge.<node>.heartbeat`), sent every 10 s, which are
  lost while a node is out of range;
- **status events** on the `ORBIT_STATUS` stream, which are persisted by the hub but lost
  if published while the link is down (agent known gap).

On the hub, every NATS client is trusted alike: no per-node credentials until phase 3.

## Decision

- **Registration.** The operator creates `EdgeNode/<nodeId>` on the first heartbeat of an
  unknown node, and never deletes one: silence means out of range, not gone.
  Decommissioning is a manual `kubectl delete`.
- **Labels.** Labels come from the agent's configuration (`ORBIT_NODE_LABELS`, validated
  at start-up) and travel in the heartbeat. The operator owns only labels under
  `orbit.io/`, adding, changing and removing them to match the heartbeat. Other labels are
  left to humans.
- **Status ingestion.**
  - Heartbeats carry `current`, `phase`, `rolloutId` and `labels`, so the `EdgeNode` always
    re-converges, even after a status event was lost.
  - `ORBIT_STATUS` is read through a **durable pull consumer** (`orbit-operator`, explicit
    ack). A restarted operator resumes where it stopped. An event is applied only if its
    stream sequence is newer than `status.observedStatusSeq`, which makes replays
    idempotent.
  - NATS subscriptions run only in the **leader** replica.
- **Write budget.** `lastSeen` is written at most once a minute; other fields are written
  when they change. `Reachable` → `Unreachable` after `2 × spec.contactInterval` without
  contact, computed with `RequeueAfter`.

## Alternatives considered

- **`EdgeNode`s declared in Git (Flux)**: every new or replaced satellite would need a PR
  before it could receive anything, and Git would hold runtime state it cannot know.
- **Auto-created, labels set in Git**: Flux and the operator would both write the same
  object. That is workable with server-side apply field managers, but it is two owners and
  a classic source of reconcile fights.
- **Node registry in a NATS KV bucket**: no `kubectl get en`, no label selectors, no
  Kubernetes events. The `Fleet` and `Rollout` controllers would have to reimplement them.

## Consequences

- ➕ A new satellite is ready for rollouts as soon as it is in range: zero hub-side steps.
- ➕ A lost status event or an operator restart self-heals: from the next heartbeat or
  from the durable consumer.
- ➖ **A node's labels are self-asserted.** A misconfigured, or spoofed, node can join the
  wrong fleet and receive its rollout.
  - Mitigation now: the hub NATS is not public (ADR-0005), and every label change is
    recorded as a Kubernetes event on the `EdgeNode`.
  - Phase 3: per-node NATS credentials allowed to publish only on `orbit.edge.<node>.>`, so
    a node cannot speak for another.
    *2026-11-28*: decided in [ADR-0010](0010-nats-per-node-credentials.md). Labels stay
    self-asserted: credentials bind a node to its ID, not to its labels.
- ➖ **API server load grows with the fleet.** Bounded by the write budget: about one
  write per node per minute in steady state.
- ➖ **The agent's heartbeat format changes** (new fields). Old agents keep working: they
  are registered without labels, so no fleet selects them.
