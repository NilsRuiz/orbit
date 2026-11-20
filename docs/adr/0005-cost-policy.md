# ADR-0005: Cost policy for a personal cloud budget

- **Status**: Accepted
- **Date**: 2026-11-20

## Context

This is a portfolio project paid out of pocket. The control-plane fee is not the main cost:
nodes, load balancers, NAT and egress are.

## Decision

- Develop locally first (docker compose, k3d): cloud only for integration and demos.
- One **zonal** GKE Standard cluster (free-tier eligible), **spot** node pool, autoscaling 1–2.
- Edge nodes: small **spot** VMs.
- **Public nodes, no Cloud NAT by default** (`private_nodes = false`); private nodes + NAT
  is one variable away, documented as the production setting.
- No external load balancers: hub NATS is exposed to edge VMs via an internal path only.
- Budget alerts on the billing account; `make tf-destroy` after every session.
- Container images and OCI artifacts on **GHCR** (free for public repos).

## Consequences

- ➕ Expected cost: a few euros per month with on-demand sessions.
- ➖ Public node IPs are less secure than private nodes → mitigated with firewall rules;
  trade-off explicitly documented here.
- ➖ Spot VMs can be preempted → a good free chaos test.
