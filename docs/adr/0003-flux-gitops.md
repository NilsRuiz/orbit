# ADR-0003: Flux (pull-based) for GitOps

- **Status**: Accepted
- **Date**: 2026-11-20

## Context

Cluster state (hub platform components, edge platform components) must be declared in Git
and reconciled continuously. The hub cannot always reach edge clusters.

## Decision

Use **Flux** on every cluster. Each cluster pulls its own configuration from Git
(`gitops/clusters/<cluster>`). Fleet *application* versions are **not** promoted by Git
branches but by the operator (ADR-0004); Git holds the `Rollout` intent only.

## Alternatives considered

- **Argo CD hub-and-spoke** — great UI, but the hub pushes to spokes and needs them
  reachable; an Argo CD per edge cluster is heavy for constrained nodes.
- **Plain `kubectl apply` from CI** — push-based, no drift correction.

## Consequences

- ➕ Works when the hub ↔ edge link is down; lightweight controllers.
- ➕ Drift on any cluster is corrected locally.
- ➖ No central UI out of the box → Grafana dashboards + Flux metrics compensate.
