# orbit-operator

Kubernetes operator running on the hub. It turns a `Rollout` (intent, from Git via Flux)
into waves of desired-state updates in NATS, and promotes or halts based on agent status.

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

**EdgeNode controller** — subscribes to `orbit.edge.*.heartbeat` and the `ORBIT_STATUS`
stream; creates/updates `EdgeNode` objects; marks a node `Unreachable` after
`2 × expected contact interval` without heartbeat (*unreachable ≠ failed*: a satellite out
of range is normal).

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
