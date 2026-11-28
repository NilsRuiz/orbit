# GitOps (Flux)

```
flux/           Flux v2.10.1 components (generated), shared by every cluster
clusters/
  hub/          entrypoint of the hub cluster
    flux-system/  Flux itself and what it syncs, managed by Flux
  edge/         entrypoint shared by every edge cluster (per-node values via postBuild)
    flux-system/
platform/
  nats-hub/     NATS 2.15.3 + JetStream (domain "hub"), leaf-node port behind an internal LB
  operator/     orbit-operator (operator/config/default, released image, in-cluster NATS)
  nats-leaf/    NATS leaf node on each edge node
  monitoring/   kube-prometheus-stack, not deployed until Phase 4 (SLOs, dashboards)
apps/
  payload/      where the payload's versions come from (OCIRepository), how they are
                applied (Kustomization), and the agent's RBAC
  agent/        orbit-agent (k3s payload driver), its state on the node's disk
```

## Bootstrap

Flux pulls this public repository **without credentials**, and manages its own installation
from `clusters/<cluster>/flux-system`. This replaces `flux bootstrap github`, which needs a
write token per cluster.

**The hub**, once the cluster exists and `get-credentials` has run: its two NATS Secrets
(see below), then one apply, the only manual one on a cloud cluster. Everything after it
comes from Git.

```bash
kubectl apply --server-side -k gitops/clusters/hub/flux-system
flux get kustomizations --watch   # flux-system, then nats-hub, then operator
```

- **Why the exception**: Flux cannot install itself from Git before it exists. This applies
  exactly the committed files, and Flux adopts them at once: later changes are PRs.
- **If the first apply fails** on `no matches for kind`, run it again. It applies the Flux
  CRDs and objects of those kinds together.
- **Upgrading Flux**: regenerate `flux/gotk-components.yaml` (command in its
  `kustomization.yaml`) in a PR.
- **Upgrading the operator**: bump `newTag` in `platform/operator` after a release.
- **The KV bucket** `orbit-desired` appears with the first `Rollout` (the operator creates
  it on first use). Until then agents wait for it; the status stream exists from the start.

**An edge cluster** needs its `orbit-node` ConfigMap and its NATS credentials Secret
first, then the same single apply.
On the edge VMs, their startup script runs both (#90). Locally, `make fleet-up` does
(hack/k3d/README.md).

```bash
kubectl create namespace flux-system
kubectl -n flux-system create configmap orbit-node --from-literal=NODE_ID=sat-1 \
  --from-literal=NODE_LABELS=orbit.io/constellation=alpha \
  --from-literal=HUB_LEAF_URL=nats-leaf://<hub leaf address>:7422
kubectl apply --server-side -k gitops/clusters/edge/flux-system
```

**Per-cluster values** are the only state not in Git, because they differ between
clusters. Flux substitutes them (`postBuild`):

| ConfigMap (`flux-system`) | Cluster | Keys |
|---|---|---|
| `orbit-node` | each edge cluster, required | `NODE_ID` (DNS-1123 label), `NODE_LABELS` (`orbit.io/*`, comma-separated), `HUB_LEAF_URL` |
| `orbit-hub` | the hub, optional | `LEAF_SOURCE_RANGE`: who may open a leaf connection, default the GCP edge subnet `10.10.0.0/20` |

**NATS credentials** ([ADR-0010](../docs/adr/0010-nats-per-node-credentials.md)) are
Secrets created before the bootstrap apply. They are issued with `orbit-creds`
(`agent/README.md`), whose keys stay offline. **Required**: the hub's NATS and the
operator do not start without theirs, and a leaf without its node's credentials is refused
by the hub.

| Secret | Cluster, namespace | Key | From |
|---|---|---|---|
| `nats-auth` | hub, `nats` | `auth.conf` | `orbit-creds hub-config` (public JWTs) |
| `orbit-operator-nats` | hub, `operator-system` | `operator.creds` | `orbit-creds operator -out …` |
| `nats-leaf-creds` | each edge cluster, `nats` | `node.creds` | `orbit-creds node -node <id> -out …` |

```bash
kubectl create namespace nats
kubectl -n nats create secret generic nats-leaf-creds --from-file=node.creds=sat-1.creds
```

Adding a satellite changes nothing on the hub: issue its credentials, create its Secret,
bootstrap it.

Versions of the `payload` app on edge nodes are **not** changed through Git. Each version
is a signed OCI artifact built from [`payload/`](../payload/). The operator writes the
desired version into NATS, and the agent sets the tag of the `payload` OCIRepository
(ADR-0003, ADR-0004, [ADR-0009](../docs/adr/0009-payload-driver-flux-oci.md)). Flux creates
that OCIRepository once and does not overwrite it (`ssa: IfNotPresent`): to change its
spec on a running cluster, delete it and let Flux recreate it.
