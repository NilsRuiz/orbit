# orbit-agent

Runs on every edge node. Watches the node's key in the `orbit-desired` JetStream KV bucket,
converges the payload to that version, verifies it, and **rolls back locally** if the new
version is not healthy before the dead man's switch fires (ADR-0004).

```
cmd/orbit-agent/      entrypoint, wiring, heartbeat loop
internal/config/      env-based configuration
internal/state/       A/B state persisted atomically on disk (survives reboots/preemption)
internal/reconcile/   state machine: idle → applying → verifying → idle | rolled-back
internal/transport/   NATS: KV watch, status to JetStream, heartbeats
internal/payload/     payload drivers: Sim (docker compose), K3s (Flux OCIRepository tag)
internal/natsauth/    per-node NATS credentials and permissions (ADR-0010)
cmd/orbit-creds/      issues them
```

## Development

Module path: `github.com/NilsRuiz/orbit/agent`.

```bash
make test            # go vet + go test -race: unit tests, in-process NATS servers, envtest
go mod tidy          # after changing dependencies; commit go.mod and go.sum
```

`make test` downloads envtest (a real kube-apiserver and etcd) into `bin/` for the k3s
driver tests. They run against the Flux CRDs of the pinned Flux release
(`internal/payload/testdata/crds`, refresh them when bumping Flux); a plain `go test`
without `KUBEBUILDER_ASSETS` fails on purpose.

`TestConvergenceAfterLeafLinkCut` reproduces the edge topology (agent → leaf → hub) with an
in-process TCP proxy as the "space link". It bounds convergence to 20 s after a 12 s
leaf↔hub cut, while the agent's own connection to its leaf stays open (#16). It takes
about 20 s.

## Configuration

| Variable | Default | Description |
|---|---|---|
| `ORBIT_NODE_ID` | — (required) | Node identity, e.g. `sat-1`. Must be a DNS-1123 label: it names the `EdgeNode` and is a NATS subject token |
| `ORBIT_NATS_URL` | `nats://127.0.0.1:4222` | Local leaf node |
| `ORBIT_NATS_CREDS` | — | NATS credentials file, when connecting straight to an authenticated server. On an edge cluster the node's credentials are on its leaf connection instead (see below) |
| `ORBIT_JS_DOMAIN` | `hub` | Hub JetStream domain, reached through the leaf node |
| `ORBIT_STATE_DIR` | `/var/lib/orbit` | Where `state.json` lives |
| `ORBIT_INITIAL_VERSION` | `1.0.0` | Version assumed on first boot |
| `ORBIT_HEARTBEAT_INTERVAL` | `10s` | |
| `ORBIT_VERIFY_TIMEOUT` | `30s` | Dead man's switch deadline. With `k3s`, it includes Flux fetching the artifact and pulling the image: raise it (minutes) |
| `ORBIT_PAYLOAD` | `sim` | Payload driver: `sim` (simulator) or `k3s` (see below) |
| `ORBIT_PAYLOAD_NAMESPACE` | `payload` | `k3s`: namespace of the payload objects |
| `ORBIT_PAYLOAD_NAME` | `payload` | `k3s`: name shared by the `OCIRepository`, the `Kustomization` and the Deployment |
| `ORBIT_SIM_BAD_VERSIONS` | — | `sim`: comma-separated versions the simulator reports unhealthy |
| `ORBIT_NODE_LABELS` | — | `orbit.io/<name>=<value>` pairs, comma-separated (e.g. `orbit.io/constellation=alpha`). Sent in heartbeats; the operator mirrors them on the `EdgeNode` and `Fleet`s select on them (ADR-0007). Other prefixes are refused at start-up |

## Payload drivers

**`k3s`** deploys versions on the node's cluster through Flux
([ADR-0009](../docs/adr/0009-payload-driver-flux-oci.md)). Each version is a signed OCI
artifact built from [`payload/`](../payload/).

- **Apply** sets the tag of the payload `OCIRepository` (`gitops/apps/payload`), and Flux
  fetches and applies the artifact. A rollback is the same call with the previous version.
- **Healthy** requires three things:
  1. the source fetched the version;
  2. the `Kustomization` applied that exact revision and is `Ready`;
  3. the Deployment finished rolling out: no old replica left, every replica updated and
     available.

  Otherwise the agent says why, and a rollback reports it, e.g. `not healthy within 2m:
  deployment: 1 old replicas pending termination`.
- **At start-up** the agent sets the tag to its known-good version again. Flux recreates the
  `OCIRepository` with its initial tag if it was deleted.
- **Cluster access**: the in-cluster service account, or `KUBECONFIG` outside the cluster.
  Its RBAC is the `orbit-agent` Role in `gitops/apps/payload`.

**`sim`** pretends: applying takes 2 s, and versions in `ORBIT_SIM_BAD_VERSIONS` are
unhealthy.

## NATS credentials

The hub authenticates each node with its own NATS user
([ADR-0010](../docs/adr/0010-nats-per-node-credentials.md)). The node's credentials are on
its **leaf connection**: the agent talks to its local leaf without credentials, and the
leaf's user limits what the node can do on the hub.

| A node may | Subjects |
|---|---|
| send its heartbeats and status events | `orbit.edge.<node>.>` |
| watch its own desired state, in domain `hub` | `$JS.hub.API.STREAM.INFO.KV_orbit-desired`, `$JS.hub.API.CONSUMER.CREATE.KV_orbit-desired.*.$KV.orbit-desired.<node>` |
| receive its own replies | `_INBOX_<node>.>`: the agent's inbox prefix (`transport.InboxPrefix`) |

Everything else is denied. That includes:

- another node's subjects, inbox and desired state;
- every key at once (`*`, `>`);
- direct KV reads, which the agent does not need: it only watches.

`internal/natsauth/natsauth_integration_test.go` runs the agent's real transport through an
authenticated leaf node, and proves each denial next to a control that works. Change
`natsauth.NodePermissions` only with a test.

`orbit-creds` issues them:

```bash
go run ./cmd/orbit-creds init                               # operator + account keys, once
go run ./cmd/orbit-creds hub-config > auth.conf             # public JWTs, for the hub
go run ./cmd/orbit-creds node -node sat-1 -out sat-1.creds  # secret, mode 0600
go run ./cmd/orbit-creds operator -out operator.creds
```

The keys go in `$ORBIT_NATS_KEYS`, by default `~/.config/orbit/nats`, readable by their
owner only. Keep them offline and backed up: losing them means re-issuing every
credential. `init` never replaces existing keys.

## Messages

The agent publishes on the hub through its leaf node (ADR-0002). These payloads are a
contract with the operator, which decodes them in another module; tests pin the field
names (`internal/transport/heartbeat_integration_test.go`).

**Desired state** (read by the agent): key `<node>` in the `orbit-desired` KV bucket,
written by the operator's Rollout controller (or `make desired` locally).

```json
{"version": "1.2.0", "rolloutId": "payload-g3", "previousVersion": "1.1.0"}
```

- The agent uses `version` and `rolloutId`, and ignores other fields. `previousVersion`
  is the operator's revert trail: what the node goes back to if the rollout halts.
- `rolloutId` is `<rollout>-g<generation>`. A value restored by a revert has
  `<rollout>-g<generation>-revert`.
- A deleted key is ignored: the node stays on its current version.

**Heartbeat**: `orbit.edge.<node>.heartbeat`, core NATS, every `ORBIT_HEARTBEAT_INTERVAL`.

```json
{
  "nodeId": "sat-1",
  "current": "1.2.0",
  "phase": "idle",
  "rolloutId": "r-42",
  "labels": {"orbit.io/constellation": "alpha"},
  "time": "2026-11-25T10:00:00Z"
}
```

- `phase`: `idle`, `applying`, `verifying` or `rolled-back`.
- `rolloutId`: omitted when empty.
- `labels`: always an object, `{}` when none are configured.
- `time`: agent clock, informative only. The operator uses its own reception time for
  `lastSeen`, because a node's clock may drift.

**Status**: `orbit.edge.<node>.status`, stream `ORBIT_STATUS`, one event per state
transition (`reconcile.Status`).

## Logs

JSON on stdout (`log/slog`). NATS connection events worth alerting on:

| `msg` | Level | Meaning |
|---|---|---|
| `nats disconnected` / `nats reconnected` | warn / info | Agent ↔ local leaf connection (not the space link) |
| `nats async error` | warn | Error reported asynchronously by nats.go, with `err`, plus `subject` and `dropped` when it concerns a subscription: slow consumer, permission violation, inactive JetStream consumer |

## Known gaps (good next tasks)

- [ ] **Status store-and-forward**: today a status published while the hub link is down is
      lost (heartbeats re-sync the current version). Fix: enable JetStream on the leaf node
      with its own domain and *source* a local status stream into the hub's `ORBIT_STATUS`.
- [x] **K3s payload driver** (#86): set the local Flux `OCIRepository` tag, check that the
      revision is applied and the Deployment rolled out
      ([ADR-0009](../docs/adr/0009-payload-driver-flux-oci.md)).
- [ ] **Metrics**: Prometheus endpoint (`orbit_agent_phase`, `orbit_agent_rollbacks_total`).
- [ ] Verify KV watch recovery after long link cuts (chaos test, phase 5). A clean cut
      (connections closed) is covered by `TestConvergenceAfterLeafLinkCut`. Still open: a
      silent cut (packets dropped, sockets left open) and a degraded link.
- [ ] **Silent mode for the test's `linkProxy`** (`pause()`/`resume()`: stop copying bytes,
      close nothing). It would give a deterministic regression test for #35: a KV watch
      stuck when the first message after a consumer reset is lost, fixed by nats.go
      v1.56.0 in #36. Today that fix is backed only by fleet runs
      (`docs/investigations/2026-11-silent-link-loss.md`). Deferred from #31 (step C).
- [ ] **Faster convergence after a clean cut** (#16): 4.7–14.7 s measured, target ≤ 3 s.
