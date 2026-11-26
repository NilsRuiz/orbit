# orbit-operator

Kubernetes operator running on the hub. It turns a `Rollout` (intent, from Git via Flux)
into waves of desired-state updates in NATS, and promotes or halts based on agent status.

Why a custom operator rather than KubeEdge, OpenYurt, Rancher Fleet or Open Cluster
Management: [ADR-0008](../docs/adr/0008-build-fleet-orchestration.md).

## Development

Scaffolded with kubebuilder v4.16.0 (domain `orbit.io`, group `fleet`, version `v1alpha1`).
The API lives in [`api/v1alpha1/`](api/v1alpha1/); examples in
[`config/samples/`](config/samples/).

```bash
make manifests generate  # regenerate CRDs (config/crd/bases) and deepcopy after editing *_types.go
make test                # vet + tests with -race against envtest (real kube-apiserver + etcd)
make lint                # golangci-lint, same pinned version as CI
```

Generated files (`zz_generated.deepcopy.go`, `config/crd/bases/`, `config/rbac/role.yaml`)
are committed but never edited by hand; CI fails if they are out of date. Tools
(controller-gen, setup-envtest, kustomize, golangci-lint) are downloaded into `bin/` at the
versions pinned in the `Makefile`.

## Resources

| Kind | Scope | Written by | Purpose |
|---|---|---|---|
| `EdgeNode` | Cluster | operator (from heartbeats) | One per node: last seen, current version, phase |
| `Fleet` | Cluster | Git | Label selector over `EdgeNode`s |
| `Rollout` | Cluster | Git | Target version + waves + health gate for a fleet |

## Controllers

**EdgeNode ingestion and controller** ([ADR-0007](../docs/adr/0007-edgenode-registration-from-heartbeats.md)):

- `internal/ingest` reads agent messages, in the **leader replica only**:
  - heartbeats (`orbit.edge.*.heartbeat`, core NATS) create unknown `EdgeNode`s, make
    their `orbit.io/*` labels follow the agent (other labels are left to humans), and
    refresh `currentVersion`, `phase`, `rolloutId` and `lastSeen`;
  - status events (`ORBIT_STATUS`) through the durable consumer `orbit-operator`: a
    restarted operator resumes where it stopped. An event is applied once, if its stream
    sequence is above `status.observedStatusSeq`, and only if it was stored after
    `lastSeen` (otherwise the last heartbeat already reported a newer state).
  - The payloads are documented in [`agent/README.md`](../agent/README.md#messages).
- `internal/controller` marks a node `Unreachable` after `2 × spec.contactInterval` of
  silence, with `RequeueAfter`. *Unreachable ≠ failed*: a satellite out of range is normal.
- Write budget: `lastSeen` (hub clock) is written at most once a minute, or every
  `contactInterval / 2` if shorter; other fields when they change. That is about one
  write per node per minute in steady state.
- Rules (reachability, label ownership, budget) are pure functions in `internal/edgenode`.

Kubernetes events on `EdgeNode`s (`kubectl get events.events.k8s.io`):

| Reason | Type | When |
|---|---|---|
| `Registered` | Normal | First heartbeat of an unknown node |
| `LabelsChanged` | Normal | The agent reported different `orbit.io/*` labels: they decide which fleets, and so which rollouts, a node gets |
| `LabelsRejected` | Warning | Reported labels outside `orbit.io/` or invalid, ignored |
| `ContactLost` / `ContactRestored` | Normal | Reachability changed |
| `RolledBack` | Warning | The agent rolled back (status event), with its message |

Flags: `-nats-url` (default `nats://127.0.0.1:4222`, the hub; it reconnects forever and
may start before NATS), `-nats-creds`, `-log-level`. Logs are JSON (`log/slog`): messages
that cannot name a valid node, or whose payload `nodeId` differs from the subject, are
dropped with a `dropping heartbeat` / `dropping status event` warning.

**Fleet controller**: resolves `spec.selector` and reports `nodes`, `reachable` and
`unreachable` (a node never seen counts as unreachable). An EdgeNode change wakes only
the fleets that select the node, before or after the change, so a relabelled node
updates both the fleet it leaves and the one it joins. Writes that change neither labels
nor reachability (`lastSeen`, version) wake no fleet. An empty selector selects every
node; an invalid one sets `Ready=False` (`InvalidSelector`).

```
$ kubectl get fleets
NAME                  NODES   REACHABLE   UNREACHABLE   READY
constellation-alpha   2       2           0             True
```

**Rollout controller** — reconcile loop:

```
Pending ─► Progressing(wave i) ─► health gate ─┬─ pass ─► wave i+1 … ─► Succeeded
                                               └─ fail ─► Halted (revert wave i)
```

1. Resolve the fleet → sorted list of `EdgeNode`s (deterministic wave membership).
2. For wave *i*: write `{"version": target, "rolloutId": …}` to `orbit-desired/<node>`.
3. Wait until every node of the wave is `idle` on target, or the gate timeout expires.
   Unreachable nodes do not count as failures; they are tracked as `pending`.
4. Gate: `failed / (succeeded + failed) <= maxFailurePercent` → promote; else halt and
   write the previous version back for the wave.
5. Use `RequeueAfter` (not busy loops); make every step idempotent (KV revision checks).

## Testing strategy

- Unit tests for wave planning and gate evaluation (pure functions).
- `envtest` for controllers + the in-process NATS server (see `agent/internal/transport`
  for the pattern).
- e2e on kind in CI: hub + simulated agents, assert a bad version halts at wave 1.
