# ADR-0009: The agent deploys payload versions through a Flux OCIRepository tag

- **Status**: Accepted
- **Date**: 2026-11-26

## Context

On a k3s edge node, the agent must turn a desired version into a running workload, and
judge its health so the dead man's switch (ADR-0004) acts on real failures. Constraints:

- **Flux owns cluster state (ADR-0003).** An object the agent changes but Git also declares
  is reverted as drift at the next reconciliation.
- **Links are intermittent (ADR-0002).** Applying a version must not need the hub, and
  rolling back must work offline.
- **Edge nodes are small.** The agent should stay a single static binary with narrow RBAC.

## Decision

- **Versions as OCI artifacts.** Each payload version is a set of manifests, published by
  CI as an OCI artifact `ghcr.io/<owner>/orbit-payload:<version>`. A version is the whole
  config, not just an image.
- **Apply.** The agent sets the tag of the local Flux `OCIRepository`, and Flux pulls and
  applies the artifact.
  - The `OCIRepository` is declared in Git with `kustomize.toolkit.fluxcd.io/ssa: IfNotPresent`.
    Flux creates it once and never overwrites it, so **the tag belongs to the agent**.
  - The agent only sets a tag; the rollback path is the same call with the previous one.
- **Health.** A version is healthy only when all three hold:
  1. the source fetched it (`status.artifact.revision` is `<version>@sha256:…`);
  2. the `Kustomization` applied that revision and is `Ready`;
  3. the Deployment finished rolling out: `observedGeneration` is current and all
     replicas are updated and available.

  The Deployment is checked by the agent itself, so health does not depend on the
  Kustomization's `wait`/`timeout` settings.
- **Kubernetes client.** The agent talks to the API with **`k8s.io/client-go`**: dynamic
  client for the Flux objects, typed client for the Deployment.
- **RBAC.** A Role in the `payload` namespace only: `get`/`patch` the `OCIRepository`, and
  `get` the `Kustomization` and the Deployment.
- **Simulator.** The `Sim` driver stays the default (`ORBIT_PAYLOAD=sim`) for docker compose.

## Alternatives considered

- **Patch the Deployment image.** It needs `ssa: IfNotPresent` on the Deployment too.
  - A version is then only an image: any config change still needs a Git commit, outside
    the rollout and its health gate.
  - Flux could no longer correct drift on the workload.
- **The agent applies the manifests itself (server-side apply).** It reimplements Flux:
  pruning, ordering, health. It needs much wider RBAC, and two controllers would own the
  same objects.
- **Flux image automation** (ImageRepository/ImagePolicy). It picks the *newest* image
  matching a policy, so it cannot follow waves, a health gate or a revert decided by the
  operator (ADR-0004). It also commits to Git, which edge nodes must not write to.
- **Hand-written REST calls instead of client-go.** Fewer dependencies, but it reimplements
  service-account token handling, TLS and API errors: security-sensitive code with no
  upside.

## Consequences

- ➕ **Atomic, versioned releases.** A release changes manifests and image together, and
  the artifact digest pins exactly what runs.
- ➕ **Flux keeps healing.** Flux still corrects drift on the workload; only one field, the
  source tag, is delegated to the agent.
- ➕ **Offline operation.** Apply and rollback are local; Flux fetches from GHCR, not from
  the hub.
- ➖ **Pulls need internet.** A node needs internet access to GHCR to pull a new version.
  - Acceptable for Phase 3: VMs have public egress (ADR-0005).
  - A local OCI mirror is the production answer, not built here.
- ➖ **Bigger binary.** client-go adds about 10 MB to the agent binary. Measured and noted
  in the PR; still one static, distroless image.
- ➖ **`IfNotPresent` hides drift on the `OCIRepository`.** A spec change in Git is no
  longer applied to it.
  - Mitigation: keep that object minimal (URL, interval, tag) and document that changing
    it means recreating it.
- ➖ **Dependence on Flux internals.** Health relies on Flux status fields
  (`artifact.revision`, `lastAppliedRevision`).
  - Mitigation: pin the Flux API version (`source.toolkit.fluxcd.io/v1`,
    `kustomize.toolkit.fluxcd.io/v1`), and test against the CRDs of the pinned Flux
    release.
