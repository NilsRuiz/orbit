# Payload versions

The workload every edge node runs: [podinfo](https://github.com/stefanprodan/podinfo), a
stand-in "satellite payload" with version and health endpoints. Each version is published
by CI as a signed OCI artifact, `ghcr.io/<owner>/orbit-payload:<version>`. On each node,
the agent selects one by setting the tag of the local Flux `OCIRepository`
([ADR-0009](../docs/adr/0009-payload-driver-flux-oci.md)).

```
versions.tsv    version → podinfo tag, healthy or not
manifests/      template of one version (placeholders in double underscores)
```

## Add a version

1. Add a line to `versions.tsv`.
2. Merge. The `payload` workflow renders the version, validates it, pushes it, then signs
   it with cosign (keyless, the workflow's identity on `main`).
3. Roll it out with a `Rollout` (`targetVersion: <version>`).

**Versions are immutable.** The workflow fails if a published version's manifests change:
edit nothing in place, add a new version instead.

The demo's bad version is `6.6.6`, marked unhealthy: its readiness probe targets a path
podinfo does not serve, so its pod never becomes ready. The rolling update keeps the
previous pod (`maxUnavailable: 0`), and the agent rolls back.

## Locally

```bash
hack/payload/render.sh 1.1.0 /tmp/payload-1.1.0                      # one version
ORBIT_PAYLOAD_SIGN=false hack/payload/publish.sh localhost:5000/orbit-payload   # a local registry
```

`publish.sh` needs the `flux` CLI, and `cosign` unless `ORBIT_PAYLOAD_SIGN=false`.
