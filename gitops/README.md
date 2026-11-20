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
  payload/      the workload the fleet runs (podinfo as a stand-in "satellite payload")
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

Versions of the `payload` app on edge nodes are **not** changed through Git: the operator
writes them into NATS and the agent applies them (ADR-0003, ADR-0004).
