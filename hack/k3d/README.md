# Local Kubernetes with k3d

Two local setups. Both pin k3s to `v1.37.2-k3s1`, the Kubernetes minor envtest uses.

## The Kubernetes fleet (phase 3): `make fleet-up`

`fleet.sh up` brings up the whole fleet **from GitOps**, like the cloud:

- `orbit-fleet-hub` runs `gitops/clusters/hub`: NATS, then the operator;
- `orbit-fleet-sat-1..3` run `gitops/clusters/edge`: leaf NATS, the payload, the agent.

They share the Docker network `orbit-fleet`. Each cluster gets the single bootstrap apply
of its `flux-system` (gitops/README.md), plus its per-cluster ConfigMap:

- `orbit-node` on each satellite: `NODE_ID`, `NODE_LABELS` (same constellations as docker
  compose), and `HUB_LEAF_URL`;
- `orbit-hub` on the hub: `LEAF_SOURCE_RANGE`, the Docker network.

The command is idempotent.

**Requirements**:

- About 5 GB of RAM: stop the docker compose fleet first (`make dev-down`).
- **On Linux, `fs.inotify.max_user_instances` of at least 512.** The default is 128, and
  four k3s clusters share it. When it runs out, a cluster's containerd never starts its
  CRI service ("failed to create fsnotify watcher: too many open files"), and that cluster
  waits forever with no node. `fleet.sh up` refuses to start below 512 and prints the
  `sysctl` to run.

```bash
make fleet-up                     # from GitHub main
SOURCE=local make fleet-up        # from this working tree (local OCI registry)
make fleet-demo-apply             # demo Fleet + Rollout on the hub
make fleet-release VERSION=1.2.0
make fleet-watch                  # second terminal
make fleet-link-cut NODE=sat-1    # silent radio loss; fleet-link-restore NODE=sat-1
make fleet-down
```

- **`SOURCE=local`**: the working tree, as the clusters fetch it, is pushed to a local
  registry, and the `flux-system` source points at it instead of GitHub. This lets you try
  a GitOps change before merging it. Re-run `up` to publish new changes; Flux polls every
  minute.
- **Link cut**: `iptables DROP` in the satellite's node container, on traffic to and from
  the hub's leaf port.
  - Packets vanish, established connections included, as in a radio loss. The leaf's pings
    detect it (ADR-0006).
  - The agent keeps its local leaf, and `kubectl` still reaches the satellite.
- **After a Docker restart**, run `make fleet-up` again. Containers may get new IPs, and it
  refreshes the hub's leaf address on the satellites. Pods do not resolve Docker container
  names, so the address is an IP.

## The hub for the docker compose fleet (phase 2): `make hub-up`

`hub.sh up` creates the `orbit-hub` cluster:

- it builds the operator image, imports it, and deploys the [`operator/`](operator/)
  overlay;
- the operator reaches the docker compose fleet's hub NATS through
  `host.k3d.internal:4222`.

Re-run it after changing the operator; `make hub-down` deletes the cluster. The demo is in
the root README.
