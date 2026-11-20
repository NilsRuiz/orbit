# ADR-0001: GKE for the hub, k3s on plain VMs for edge nodes, all on GCP first

- **Status**: Accepted
- **Date**: 2026-11-20

## Context

The hub runs the operator, NATS/JetStream and observability: it needs a reliable, managed
control plane. Edge nodes simulate satellites / ground stations: real edge devices never
run a managed Kubernetes — they run a lightweight distribution on constrained hardware.
Budget is a personal one: a few euros a month.

## Decision

- **Hub**: one GKE **Standard, zonal** cluster (management fee covered by the GKE free tier).
- **Edge**: **k3s** installed on small **spot Compute Engine VMs**, one VM = one node.
- Start **GCP-only**; keep the `edge-node` Terraform module behind a provider-neutral
  interface so a second provider (Scaleway) can be added in phase 6.

## Alternatives considered

- **GKE for every edge node** — unrealistic for edge, and each extra cluster costs a
  management fee (~73 $/month).
- **GKE Autopilot hub** — also free-tier eligible, but forbids privileged workloads
  needed by chaos tooling (Chaos Mesh).
- **AWS EKS** — no control-plane free tier.
- **Multi-cloud from day one (GCP + Scaleway)** — more realistic network separation,
  but doubles IAM/provider complexity before anything works. Deferred, not rejected.
- **Scaleway Kosmos (one managed control plane, nodes from any provider)** — a single
  stretched cluster tolerates long link outages poorly; autonomous edge clusters do.

## Consequences

- ➕ One account, one IAM model, one Terraform provider to start with.
- ➕ k3s on ARM/x86 VMs mirrors real edge constraints.
- ➖ Edge and hub share a VPC: network separation must be simulated (link chaos, ADR-0002).
