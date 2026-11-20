# Local Kubernetes fleet with k3d (phase 2)

Used once the operator exists: one hub cluster + N edge clusters on a shared Docker network.

```bash
./hack/k3d/up.sh 2        # hub + 2 edge clusters
kubectl config get-contexts | grep orbit
./hack/k3d/down.sh
```

Flux can then be installed against a local Git server or your GitHub fork; for a fast
inner loop, apply `gitops/platform/*` with `kubectl apply -k` directly.
