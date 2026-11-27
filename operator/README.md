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

## NATS resources

The operator **owns** its JetStream resources on the hub and creates them at start-up, or
updates them to the configuration declared in its code, keeping their content:

| Resource | Config | Created by |
|---|---|---|
| KV `orbit-desired` | history 5, file storage (`desired.BucketConfig`) | the Rollout controller, on first use |
| Stream `ORBIT_STATUS` | `orbit.edge.*.status`, file, limits, 7 days (`ingest.StatusStreamConfig`) | the ingester, retried until the hub answers |

A fresh hub needs nothing else. In docker compose, `hack/scripts/init-hub.sh` creates them
for the agent-only demo, which has no operator. An incompatible existing stream (for
example memory storage, which cannot be changed in place) makes the ingester log
`status stream not ready, retrying`, until it is deleted.

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

**Rollout controller** ([ADR-0004](../docs/adr/0004-progressive-rollout-and-local-rollback.md)):

```
Pending ─► Progressing(wave i) ─► gate ─┬─ Promote ─► soak (pauseFor) ─► wave i+1 … ─► Succeeded
                                        └─ Halt ─► revert wave i ─► Halted
```

Each reconcile recomputes the plan and the gate from the cluster and the bucket, so it can
resume anywhere. The status keeps where the run is: `currentWave`, `waveStartTime`,
`soakUntil`.

- **A run** has the id `<name>-g<generation>`, taken when it starts. Only a new
  `targetVersion` or `fleetRef` starts a new run, from the first wave. Editing `paused`,
  the waves or the gate applies to the run in progress. `Succeeded` and `Halted` are
  final until then.
- **Writes**: `{"version", "rolloutId", "previousVersion"}` is written to
  `orbit-desired/<node>` for every node of the started waves, including unreachable ones,
  which apply it once back in range.
- **Gate timeout, per node**: it counts from the latest of the wave start, the node's
  return in range (`EdgeNode.status.reachableSince`) and the moment it adopted this run's
  `rolloutId` (`status.rolloutIdSince`, i.e. started applying). A satellite back after
  hours, or after a short link cut that never made it Unreachable, gets the full timeout
  from when it starts applying.
  - At its deadline, a node is `Stuck`, and counts as failed, only if it acknowledged the
    run.
  - Otherwise it never received the desired state, so it is `Pending`: it left range
    before being marked Unreachable, which takes 2 × `contactInterval`.
- **Halt** reverts the current wave only: earlier waves passed their gate. A run never
  re-writes a node it reverted.
- **The last wave** does not complete while some nodes are pending (out of range). The
  rollout stays `Progressing` with reason `WaitingForPendingNodes`: neither failed nor
  done.
- **`spec.paused`**: no promotion and no new writes, but the gate can still halt. Safety
  comes before a manual hold.
- **Wake-ups**:
  - relevant changes of the fleet's `EdgeNode`s (status fields `Classify` reads, labels);
  - the fleet itself;
  - `RequeueAfter` for the nearest node deadline or the end of a soak. No polling.
- **The `Rollout` is read uncached**: the cache may not hold the controller's own last
  status write yet. Acting on it once re-wrote a bad version over a revert.
- **Events**: `WaveStarted`, `WavePromoted`, `Paused`, `Halted` (Warning, with the counts
  and the number of nodes reverted), `Succeeded`.

```
$ kubectl get rollouts
NAME      TARGET   PHASE       WAVE   UPDATED   FAILED   PENDING
payload   1.2.0    Succeeded   2      10        0        0
```

Desired states are written through `internal/desired`:
- the writes are compare-and-swap on the KV revision, so a concurrent `make desired` is
  detected, not overwritten;
- each value records `previousVersion`, so a halted wave reverts from the bucket itself.
  A revert writes `<rolloutId>-revert`, and never touches a value another writer has
  changed since. See the format in [`agent/README.md`](../agent/README.md#messages).

The rules are pure functions in `internal/rollout`:

- **Waves** (`PlanWaves`): wave *i* ends with `ceil(percent × N / 100)` nodes, so a 10 %
  wave reaches at least one node. Nodes are ordered by a hash of the rollout name and the
  node name: the same canaries for every release of a `Rollout`, not alphabetical, and a
  node joining the fleet does not reshuffle the others.
- **Node result** (`Classify`): `Succeeded` (idle on target), `Failed` (rolled back from
  *this* rollout), `Pending` (unreachable, outcome unknown) or `InProgress`.
- **Gate** (`EvaluateGate`), over all waves started so far:
  - halts early once failures exceed the threshold even if every node in progress
    succeeds;
  - `Stuck` nodes (past their `Deadline`) count as failed;
  - if every node is pending, it waits, even past the timeout: no blind promotion, and
    out of range is not a failure.

## Testing strategy

- Unit tests for wave planning and gate evaluation (pure functions).
- `envtest` for controllers + the in-process NATS server (see `agent/internal/transport`
  for the pattern).
- e2e on kind in CI: hub + simulated agents, assert a bad version halts at wave 1.
