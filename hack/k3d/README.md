# Local Kubernetes with k3d

**Hub cluster (phase 2)**: `make hub-up` runs `hub.sh up`. It creates the `orbit-hub`
cluster (k3s pinned to the Kubernetes minor envtest uses), builds the operator image,
imports it, and deploys the `operator/` overlay in [`operator/`](operator/). The operator
reaches the docker compose fleet's hub NATS through `host.k3d.internal:4222`. Re-run it
after changing the operator. `make hub-down` deletes the cluster. The demo is in the root
README.

**Edge clusters (phase 3)**: `up.sh` creates a hub plus N edge clusters on a shared
Docker network, for when satellites run k3s and Flux.

```bash
./hack/k3d/up.sh 2        # hub + 2 edge clusters
kubectl config get-contexts | grep orbit
./hack/k3d/down.sh
```

Flux can then be installed against a local Git server or your GitHub fork; for a fast
inner loop, apply `gitops/platform/*` with `kubectl apply -k` directly.
