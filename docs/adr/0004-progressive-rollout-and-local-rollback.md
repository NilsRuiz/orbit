# ADR-0004: Progressive rollouts in an operator, rollback decided locally

- **Status**: Accepted
- **Date**: 2026-11-20

## Context

Updating the whole fleet at once is unacceptable: a bad version would reach every node,
and nodes out of contact cannot be repaired. A node that breaks *after* losing its link
must still recover on its own.

## Decision

Two layers of safety:

1. **Fleet level (hub)** — a Go operator reconciles `Rollout` resources: it updates the
   fleet in **waves** (e.g. 10 % → 50 % → 100 %) and promotes only when a **health gate**
   passes (failure ratio below threshold within a timeout). On failure it halts and
   reverts the current wave.
2. **Node level (edge)** — the agent keeps an **A/B state** (`current`, `previous`) on
   disk. After applying a new version it starts a **dead man's switch**: if the payload is
   not healthy before the deadline, it rolls back to `previous` without waiting for the hub.

## Alternatives considered

- **Flux image automation per wave** — Git commits per wave per node: noisy history and
  slow over intermittent links.
- **Argo Rollouts / Flagger** — excellent for in-cluster canaries, but they operate inside
  one cluster, not across a fleet of disconnected clusters.
- **Hub-only rollback** — fails exactly when it matters most: when the node is offline.

## Consequences

- ➕ Bad versions are contained to one wave; bricked-node risk is bounded locally.
- ➕ Clear separation: Git = intent, operator = orchestration, agent = local safety.
- ➖ More custom code to test (envtest for the operator, unit tests for the agent state machine).
