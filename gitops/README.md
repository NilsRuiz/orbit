# GitOps (Flux)

```
clusters/
  hub/          entrypoint of the hub cluster
    flux-system/  Flux itself (generated components + what to sync), managed by Flux
  edge/         entrypoint shared by every edge cluster (node ID injected via postBuild)
platform/
  nats-hub/     NATS 2.15.3 + JetStream (domain "hub"), leaf-node port behind an internal LB
  operator/     orbit-operator (operator/config/default, released image, in-cluster NATS)
  nats-leaf/    NATS leaf node on each edge node
  monitoring/   kube-prometheus-stack, not deployed until Phase 4 (SLOs, dashboards)
apps/
  payload/      where the payload's versions come from (OCIRepository), how they are
                applied (Kustomization), and the agent's RBAC
```

## Bootstrap

Flux pulls this public repository **without credentials**, and manages its own installation
from `clusters/<cluster>/flux-system`. This replaces `flux bootstrap github`, which needs a
write token per cluster.

**The hub**, once the cluster exists and `get-credentials` has run: one apply, the only
manual one on a cloud cluster. Everything after it comes from Git.

```bash
kubectl apply --server-side -k gitops/clusters/hub/flux-system
flux get kustomizations --watch   # flux-system, then nats-hub, then operator
```

- **Why the exception**: Flux cannot install itself from Git before it exists. This applies
  exactly the committed files, and Flux adopts them at once: later changes are PRs.
- **If the first apply fails** on `no matches for kind`, run it again. It applies the Flux
  CRDs and objects of those kinds together.
- **Upgrading Flux**: regenerate `flux-system/gotk-components.yaml` (command in its
  `kustomization.yaml`) in a PR.
- **Upgrading the operator**: bump `newTag` in `platform/operator` after a release.
- **The KV bucket** `orbit-desired` appears with the first `Rollout` (the operator creates
  it on first use). Until then agents wait for it; the status stream exists from the start.

**Edge clusters**: their bootstrap comes with #88 (P3.3), from the VMs' startup script
(#90).

Versions of the `payload` app on edge nodes are **not** changed through Git. Each version
is a signed OCI artifact built from [`payload/`](../payload/). The operator writes the
desired version into NATS, and the agent sets the tag of the `payload` OCIRepository
(ADR-0003, ADR-0004, [ADR-0009](../docs/adr/0009-payload-driver-flux-oci.md)). Flux creates
that OCIRepository once and does not overwrite it (`ssa: IfNotPresent`): to change its
spec on a running cluster, delete it and let Flux recreate it.
