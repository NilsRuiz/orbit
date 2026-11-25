# ADR-0008: Build the fleet rollout orchestration instead of adopting an edge platform

- **Status**: Accepted
- **Date**: 2026-11-25

## Context

Phase 2 writes a custom operator: `EdgeNode`, `Fleet`, and `Rollout` waves with a health gate.
It works with the agent's local dead man's switch (ADR-0004) over NATS leaf nodes
(ADR-0002). Custom orchestration code has to be maintained forever, so the existing options
were reviewed first: [2026-11-fleet-orchestration-landscape](../investigations/2026-11-fleet-orchestration-landscape.md).

What matters for Orbit:

- links are down most of an orbit;
- progressive waves gated on health, where unreachable is not failed;
- rollback decided **on the node** without the hub;
- silent-loss-tolerant store-and-forward;
- one k3s per node at a few euros a month.

Space operators show no standard here. Mega-constellations such as Starlink build their own
fleet update tooling, and container platforms in orbit (KubeEdge on Tiansuan-1, Azure Orbital
Space SDK at Loft Orbital, MicroShift on the ISS) address **running** payload software
rather than **gating** fleet-wide version changes.

## Decision

Build the fleet-level rollout orchestration in Orbit. That is the operator and the agent
protocol over NATS, nothing more. Everything generic is adopted, not rebuilt: Flux for
cluster state, NATS for transport, controller-runtime for the operator, k3s on nodes. The
gate semantics borrow from Rancher Fleet (`maxUnavailable` per partition) and Open Cluster
Management (`progressDeadline`, health conditions).

## Alternatives considered

- **KubeEdge / OpenYurt**: the strongest offline autonomy, and KubeEdge has actually flown on a
  satellite. But edge hosts become *nodes of one central cluster*, which conflicts with one k3s
  per node (ADR-0001). They bring their own cloud↔edge channel instead of NATS, and no
  fleet-level gated version waves were found.
- **Rancher Fleet**: the closest match for rollouts (partitions, `maxUnavailable`, pause on
  NotReady partitions). But health is judged centrally, there is no node-local rollback, and
  its behaviour during hours-long disconnections is not documented. It also requires a
  Rancher/Fleet manager on the hub.
- **Open Cluster Management**: progressive per-group rollouts with a deadline and CEL-defined
  health. It has the same central-gate limitation, and wave membership needs Placement
  decision groups on top of our own node model.
- **A hybrid (OCM or Fleet for distribution, Orbit only for the local rollback)**: two control
  loops would own the same version, and the gate would still be blind while links are down.

## Consequences

- ➕ The design follows the constraint that defines the domain: most decisions must hold
  while the hub is unreachable.
- ➕ A narrow scope: about one CRD-driven controller loop and one agent protocol. No generic
  workload distribution.
- ➖ **The orchestration code is ours to maintain and secure.**
  - Mitigation: keep it narrow and behind pure, unit-tested planning/gate functions (#45).
  - Revisit this ADR if a candidate documents gated rollouts across disconnected clusters
    *and* local rollback, or if Orbit's goal shifts from demonstrating the design to
    operating it.
- ➖ **Not empirically validated**: no candidate was deployed against our link simulations
  (`link.sh cut|blackhole`). A spike on Rancher Fleet or OCM would turn this desk review into
  evidence.
- ➖ **No ecosystem features for free** (UIs, multi-tenancy, device management). Acceptable for
  the current scope.
