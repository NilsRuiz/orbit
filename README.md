# Orbit — GitOps fleet management for intermittently-connected edge nodes

> Deploy, update and observe a fleet of remote edge nodes (simulated satellites / ground
> stations) from a cloud control plane — progressively, safely, and **without ever bricking
> a node that is out of reach**.

![status](https://img.shields.io/badge/status-WIP-orange)
[![agent](https://github.com/NilsRuiz/orbit/actions/workflows/agent.yml/badge.svg?branch=main)](https://github.com/NilsRuiz/orbit/actions/workflows/agent.yml)
[![operator](https://github.com/NilsRuiz/orbit/actions/workflows/operator.yml/badge.svg?branch=main)](https://github.com/NilsRuiz/orbit/actions/workflows/operator.yml)

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
  the node to come back in range. Status reports are not buffered yet: a status published
  while the link is down is lost, and heartbeats re-sync the current version on reconnect
  ([agent known gaps](agent/README.md#known-gaps-good-next-tasks)).
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

Requirements: Docker, Go ≥ 1.26, `make`, plus [k3d](https://k3d.io) and `kubectl` for the
operator. Component notes: [agent/README.md](agent/README.md),
[operator/README.md](operator/README.md).

### A rollout across the fleet

The operator runs in a local k3d cluster, the "hub". The satellites are docker compose
containers: a NATS leaf node and an agent each. sat-1 and sat-2 are in constellation
`alpha`, and sat-3 is in `beta`.

```bash
make dev-up            # hub NATS + 3 edge leaf nodes + 3 agents (docker compose)
make hub-up            # k3d cluster + the operator (built locally), about 1 min the first time
make demo-watch        # in a second terminal: EdgeNodes, Fleets, Rollouts, live

make demo-apply        # Fleet constellation-alpha (sat-1, sat-2) + Rollout payload → 1.1.0
```

The satellites register themselves from their heartbeats (`kubectl --context
k3d-orbit-hub get edgenodes --show-labels`). The rollout updates one canary, soaks for
20 s, then updates the rest of the fleet. sat-3 is not in the fleet and stays on 1.0.0.

```bash
make demo-release VERSION=6.6.6     # a bad version: it fails on the canary
```

The canary applies 6.6.6, is "not healthy within 15s", and rolls back by itself (dead
man's switch). The operator then halts the rollout at wave 1 and reverts the canary's
desired state; the other satellite is never touched.

```bash
kubectl --context k3d-orbit-hub get rollout payload    # PHASE Halted, FAILED 1
kubectl --context k3d-orbit-hub get events.events.k8s.io --field-selector reason=Halted
make demo-release VERSION=1.2.0     # a fixed version: a new run, from the first wave
```

Satellites out of range are pending, not failed:

```bash
make link-cut NODE=sat-1                 # cut a satellite's link
make demo-release VERSION=1.3.0          # its update waits; the gate does not fail on it
make link-restore NODE=sat-1             # back in range → applies, and the rollout completes
make hub-down && make dev-down
```

### The same rollout on Kubernetes satellites

Here every satellite is its own k3s cluster, as on the edge VMs. All four clusters come up
**from GitOps**: Flux on each pulls this repository.

- **The hub** gets NATS and the operator.
- **Each satellite** gets a leaf NATS, the agent, and the payload, which is
  [podinfo](https://github.com/stefanprodan/podinfo) published as signed OCI artifacts
  (`payload/`).

The agent applies a version by setting the tag of the local Flux `OCIRepository`
([ADR-0009](docs/adr/0009-payload-driver-flux-oci.md)).

```bash
make dev-down          # free the memory: four clusters need about 5 GB
make fleet-up          # hub + sat-1..3 (k3d), about 5 min; SOURCE=local: this working tree
make fleet-watch       # in a second terminal

make fleet-demo-apply             # same Fleet and Rollout as above → 1.1.0
make fleet-release VERSION=6.6.6  # never ready on the canary: rolled back, rollout halted
make fleet-release VERSION=1.2.0
make fleet-link-cut NODE=sat-1    # silent radio loss to the hub
make fleet-release VERSION=1.3.0  # sat-1 is pending; the rest of the fleet updates
make fleet-link-restore NODE=sat-1
make fleet-down
```

`kubectl --context k3d-orbit-fleet-sat-1 -n payload get deploy payload -L
app.kubernetes.io/version` shows what a satellite actually runs. Details are in
[hack/k3d/README.md](hack/k3d/README.md).

### The agent alone

Without the operator, `make desired` writes a node's desired state directly:

```bash
make dev-up
make logs              # in a second terminal: follow the agents (blocks until Ctrl-C)

make desired NODE=sat-1 VERSION=1.1.0     # set a target version

make link-cut NODE=sat-2                  # clean cut: satellite goes out of range
make desired NODE=sat-2 VERSION=1.1.0     # update queued while offline: nothing happens
make link-restore NODE=sat-2              # back in range → update applied within seconds

make link-blackhole NODE=sat-2            # silent loss (real radio): packets dropped, sockets open
make desired NODE=sat-2 VERSION=1.2.0     # queued again: nothing happens
make link-restore NODE=sat-2              # → applied in about 10 s (see ADR-0006)

make desired NODE=sat-3 VERSION=6.6.6     # "bad" version → "not healthy within 15s", rolls back by itself
make status            # what the hub received on ORBIT_STATUS
make dev-down
```

## Roadmap

- [x] Repository skeleton, ADRs, CI scaffolding
- [x] **Phase 1** — agent: desired-state watch, persisted A/B state, dead man's switch
- [x] **Phase 2** — operator: `EdgeNode` registration from heartbeats, `Rollout` waves
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
