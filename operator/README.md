# orbit-operator

Kubernetes operator running on the hub. It turns a `Rollout` (intent, from Git via Flux)
into waves of desired-state updates in NATS, and promotes or halts based on agent status.

## Scaffold (run locally — generated code is not committed by hand)

Requires Go, Docker and [kubebuilder](https://book.kubebuilder.io/quick-start).

```bash
cd operator
kubebuilder init --domain orbit.io --repo github.com/<you>/orbit/operator
kubebuilder create api --group fleet --version v1alpha1 --kind EdgeNode --resource --controller
kubebuilder create api --group fleet --version v1alpha1 --kind Fleet    --resource --controller=false
kubebuilder create api --group fleet --version v1alpha1 --kind Rollout  --resource --controller
```

Then copy the spec/status fields from [`design/api_v1alpha1.go`](design/api_v1alpha1.go)
into the generated `api/v1alpha1/*_types.go`, and run `make manifests generate`.

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
