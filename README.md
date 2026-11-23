# Orbit — GitOps fleet management for intermittently-connected edge nodes

> Deploy, update and observe a fleet of remote edge nodes (simulated satellites / ground
> stations) from a cloud control plane — progressively, safely, and **without ever bricking
> a node that is out of reach**.

![status](https://img.shields.io/badge/status-WIP-orange)
<!-- TODO: add CI badges once workflows are green -->

## Why this project

Edge nodes are many, far away, resource-constrained and only **intermittently connected**
(a LEO satellite sees a ground station a few minutes per orbit). Push-based deployment
tooling assumes the opposite: that every target is reachable, through an inbound
connection, at the moment the central system decides to deploy. Orbit inverts this: the
hub only publishes desired state, and each node pulls and converges whenever its link is up.

Orbit explores what a reliable platform for that world looks like:

- **Intent in Git, orchestration in an operator** — Git declares *what* the fleet should
  run; a Go Kubernetes operator decides *how*: waves, health gates, automatic rollback.
- **Store-and-forward by design** — NATS JetStream + leaf nodes: desired state waits for
  the node to come back in range; status flows back when the link returns.
- **Local safety net** — every node runs a Go agent with an A/B-style *dead man's switch*:
  if a new version is not healthy within its deadline, the node rolls itself back,
  with or without a link to the hub.
- **Everything as code** — Terraform for infrastructure, Flux for cluster state,
  GitHub Actions with keyless auth (OIDC / Workload Identity Federation) for delivery.

## Architecture

```
                         GitHub (source of truth)
                                   │
             ┌─────────────────────┴─────────────────────┐
             ▼                                           ▼
   Terraform (GCP, via OIDC)                    Flux (pull-based GitOps)
             │                                           │
             ▼                                           ▼
 ┌───────────────────────────── HUB (GKE, zonal) ──────────────────────────────┐
 │  orbit-operator (Go)          NATS + JetStream            Prometheus/Grafana │
 │  CRDs: EdgeNode, Fleet,       KV "orbit-desired"          SLOs, error budget │
 │        Rollout                stream "ORBIT_STATUS"                          │
 └────────────────────────────────────┬────────────────────────────────────────┘
                                      │  NATS leaf-node links (unreliable, simulated)
           ┌──────────────────────────┼──────────────────────────┐
           ▼                          ▼                          ▼
   ┌─ edge sat-1 (k3s) ─┐     ┌─ edge sat-2 (k3s) ─┐     ┌─ edge sat-N (k3s) ─┐
   │ NATS leaf node     │     │ NATS leaf node     │     │ NATS leaf node     │
   │ orbit-agent (Go)   │     │ orbit-agent (Go)   │     │ orbit-agent (Go)   │
   │ payload workload   │     │ payload workload   │     │ payload workload   │
   └────────────────────┘     └────────────────────┘     └────────────────────┘
```

Rollout flow:

1. A PR changes `Rollout.spec.targetVersion` → merged → Flux applies it on the hub.
2. The operator picks wave 1 (e.g. 10 % of the fleet) and writes the target version into
   the `orbit-desired` JetStream KV bucket for those nodes.
3. Each agent watches its own key. Reachable nodes update now; others get the update when
   their link comes back.
4. Agents report `applying → healthy | rolled-back` on `orbit.edge.<node>.status`.
5. The operator promotes to the next wave only if the health gate passes; otherwise it
   halts the rollout and reverts the wave.

## Repository layout

| Path | Content |
|---|---|
| [`agent/`](agent/) | Go edge agent: desired-state watch, apply, health check, local rollback |
| [`operator/`](operator/) | Go operator (kubebuilder): `EdgeNode`, `Fleet`, `Rollout` CRDs |
| [`infra/terraform/`](infra/terraform/) | GCP infrastructure: network, GKE hub, edge VMs, GitHub OIDC |
| [`gitops/`](gitops/) | Flux configuration for hub and edge clusters |
| [`hack/`](hack/) | Local dev: docker-compose fleet, link-chaos scripts, k3d |
| [`docs/adr/`](docs/adr/) | Architecture Decision Records |
| [`docs/runbooks/`](docs/runbooks/), [`docs/postmortems/`](docs/postmortems/) | SRE docs |

## Quick start (local, free)

Requirements: Docker, Go ≥ 1.24, `make`. First time only: replace the placeholder module
path `github.com/orbit-fleet/orbit` with your own and run `cd agent && go mod tidy`
(see [agent/README.md](agent/README.md)).

```bash
make dev-up            # hub NATS + 3 edge leaf nodes + 3 agents (docker compose)
make desired NODE=sat-1 VERSION=1.1.0     # simulate the operator: set a target version
make logs              # watch agents apply / report
make link-cut NODE=sat-2                  # satellite goes out of range
make desired NODE=sat-2 VERSION=1.1.0     # update queued while offline
make link-restore NODE=sat-2              # back in range → update applied
make desired NODE=sat-3 VERSION=6.6.6     # "bad" version → agent rolls back by itself
make dev-down
```

## Roadmap

- [x] Repository skeleton, ADRs, CI scaffolding
- [ ] **Phase 1** — agent: desired-state watch, persisted A/B state, dead man's switch
- [ ] **Phase 2** — operator: `EdgeNode` registration from heartbeats, `Rollout` waves
- [ ] **Phase 3** — GCP: Terraform hub + edge VMs, Flux bootstrap, OIDC from CI
- [ ] **Phase 4** — SRE: SLOs (Sloth), alerting, Grafana dashboards
- [ ] **Phase 5** — Chaos: link cuts / latency / bandwidth, a written postmortem
- [ ] **Phase 6** — Second provider for edge nodes (Scaleway) behind the same module interface

## Contributing

Trunk-based development, Conventional Commits, PR + green CI before merge, automated
releases: see [CONTRIBUTING.md](CONTRIBUTING.md).

## Cost policy

Designed to run for a few euros a month: one **zonal** GKE Standard cluster (management fee
covered by the GKE free tier), **spot** VMs, no Cloud NAT by default, and
`make tf-destroy` after each demo session. See [ADR-0005](docs/adr/0005-cost-policy.md).
