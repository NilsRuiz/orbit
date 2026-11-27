# GitOps (Flux)

```
clusters/
  hub/      entrypoint for the hub cluster (Flux Kustomizations)
  edge/     entrypoint shared by every edge cluster (node ID injected via postBuild)
platform/
  nats-hub/     NATS + JetStream (domain "hub"), leaf-node port behind an internal LB
  nats-leaf/    NATS leaf node on each edge node
  monitoring/   kube-prometheus-stack (hub)
apps/
  payload/      where the payload's versions come from (OCIRepository), how they are
                applied (Kustomization), and the agent's RBAC
```

## Bootstrap

```bash
# Hub (after `terraform apply` and get-credentials)
flux bootstrap github --owner=<you> --repository=orbit --path=gitops/clusters/hub --personal

# Each edge node (kubeconfig from the k3s VM, via IAP tunnel)
flux bootstrap github --owner=<you> --repository=orbit --path=gitops/clusters/edge --personal
kubectl -n flux-system create configmap orbit-node --from-literal=NODE_ID=sat-1 \
  --from-literal=HUB_LEAF_URL=nats-leaf://<hub-internal-lb-ip>:7422
```

Versions of the `payload` app on edge nodes are **not** changed through Git. Each version
is a signed OCI artifact built from [`payload/`](../payload/). The operator writes the
desired version into NATS, and the agent sets the tag of the `payload` OCIRepository
(ADR-0003, ADR-0004, [ADR-0009](../docs/adr/0009-payload-driver-flux-oci.md)). Flux creates
that OCIRepository once and does not overwrite it (`ssa: IfNotPresent`): to change its
spec on a running cluster, delete it and let Flux recreate it.
