# Investigation: how fleets of space and edge nodes are orchestrated

- **Date**: 2026-11-25
- **Status**: Desk research. No candidate was deployed or benchmarked.
- **Related**: [ADR-0008](../adr/0008-build-fleet-orchestration.md) (build vs adopt),
  [ADR-0001](../adr/0001-gke-hub-k3s-edge.md), [ADR-0002](../adr/0002-nats-jetstream-leaf-nodes.md),
  [ADR-0004](../adr/0004-progressive-rollout-and-local-rollback.md)

## Question

Phase 2 builds a custom operator (`Rollout` waves with a health gate) on top of NATS and a
node-local dead man's switch. Before investing in it: what do real space operators use, and
could an existing open-source fleet or edge platform do the job?

Orbit's requirements, from the ADRs:

| # | Requirement | Source |
|---|---|---|
| R1 | Links are down most of the time; a node must converge whenever it is in range (minutes per orbit) | ADR-0002, #16, #31 |
| R2 | Progressive rollout in waves, promoted only through a health gate; unreachable ≠ failed | ADR-0004 |
| R3 | A node rolls itself back with no hub contact (dead man's switch) | ADR-0004 |
| R4 | Store-and-forward transport tolerant to silent loss (NATS leaf nodes) | ADR-0002, ADR-0006 |
| R5 | One small k3s cluster per node, a hub on GKE, a few euros a month | ADR-0001, ADR-0005 |

## What space operators use

There is **no standard product**. Two layers are kept apart:

- **Flight software** (attitude, power, communications) is conservative and not
  containerised. NASA's open-source core Flight System underpins more than 40 missions,
  from small satellites to Artemis and the Roman Space Telescope [cfs].
- **Mega-constellations build their own fleet tooling.** Starlink pushes regular software
  updates to more than 4,000 satellites. It describes two problems: updating one satellite
  safely, and orchestrating thousands of updates without impacting users. It reports more
  than 200 updates with no satellite lost to a failed update [usu]; a 2020 interview
  mentioned about one update a week [geekwire]. The mechanism is not public.
- **Payload / mission computing** is where containers appear, mostly vendor-led (claims not
  independently verified):

| Operator | Stack | Status |
|---|---|---|
| Tiansuan (research constellation, China) | **KubeEdge** + Sedna on Tiansuan-1; intermittent connection to a ground controller depending on orbital position | Launched 2021-12-07 [kubeedge-sat][tiansuan] |
| Loft Orbital | Cockpit operations software + Microsoft Azure Orbital Space SDK (containers, Dapr) | 13+ hosted software/AI missions [loft][azure] |
| Axiom Space / NASA | Red Hat Device Edge (MicroShift) on AxDCU-1 at the ISS; single-node OpenShift on Spaceborne Computer-2 | Announced/flown [redhat] |
| Hypergiant | K3s on satellites, updates pulled when in range | Announced 2021, flight not confirmed [suse] |

Research also flags that vanilla Kubernetes control planes suit LEO constellations poorly
(geographic dispersion, frequent handovers) [kubespace].

## Candidates

| | KubeEdge (CNCF) | OpenYurt (CNCF) | Rancher Fleet (SUSE) | Open Cluster Management (CNCF) |
|---|---|---|---|---|
| Model | Edge hosts join **one central cluster as nodes** | Same (nodes of a central cluster) | GitOps to **many clusters**, pull agent per cluster | Hub + managed clusters, Placement + ManifestWork |
| R1 offline | **Strong**: edgecore keeps metadata in SQLite and runs autonomously when offline [kubeedge] | Strong (node autonomy) | Agent pulls; official docs do not describe disconnected-cluster behaviour [rancher-dc] | Not assessed |
| R2 waves + gate | Not found as a fleet-level feature | Not found | **Yes**: partitions, `maxUnavailable`, pause when too many partitions are NotReady [fleet] | **Yes**: `Progressive` / `ProgressivePerGroup`, `progressDeadline`, gate on CEL-defined health conditions [ocm] |
| R3 local rollback | No (kubelet semantics) | No | No (decided by the manager) | No (decided by the hub) |
| R4 transport | Own cloud↔edge channel | Own tunnel | HTTPS to the manager | HTTPS to the hub |
| R5 fit | Conflicts with one-k3s-per-node (ADR-0001) | Same conflict | Needs a Rancher/Fleet manager on the hub | Hub components on GKE; fits |

## Findings

1. **No candidate covers R2, R3 and R4 together.** Their rollback is always decided centrally,
   while R3 exists precisely for when the node cannot reach the hub.
2. **KubeEdge best matches the space context** (real flight, offline autonomy). It
   orchestrates *pods on remote nodes*, not *gated version waves across a fleet*, and it
   imposes its own transport and cluster model.
3. **Rancher Fleet and OCM best match Phase 2's rollout logic.** Their gates
   (`maxUnavailable` per partition, CEL health conditions, `progressDeadline`) are good
   prior art for our `HealthGate`. But their gate reads cluster health through the
   manager connection, which is exactly what is missing for most of an orbit.
4. **Not verified here**: actual behaviour under hours-long disconnection for each candidate
   (a spike would be needed), current KubeEdge APIs for grouped rollouts (EdgeApplication /
   NodeGroup), and the OCM API field names, which differ between doc versions.

## Sources

- [cfs] NASA core Flight System — https://etd.gsfc.nasa.gov/capabilities/core-flight-system/
- [usu] Starlink software updates, SmallSat 2023 — https://digitalcommons.usu.edu/smallsat/2023/all2023/72
- [geekwire] SpaceX engineers on Starlink (2020) — https://www.geekwire.com/2020/5-trillion-bytes-day-spacex-engineers-flash-facts-starlink-satellites/
- [kubeedge-sat] KubeEdge satellite case study — https://kubeedge.io/case-studies/satellite/
- [tiansuan] Tiansuan Constellation (arXiv 2112.14957) — https://arxiv.org/abs/2112.14957
- [loft] Loft Orbital, virtual missions — https://www.loftorbital.com/post/virtualmission
- [azure] Azure Orbital Space SDK — https://azure.microsoft.com/blog/any-developer-can-be-a-space-developer-with-the-new-azure-orbital-space-sdk/
- [redhat] Red Hat and Axiom Space AxDCU-1 — https://redhat.com/en/about/press-releases/red-hat-teams-axiom-space-launch-optimize-axiom-spaces-data-center-unit-1-orbit
- [suse] Hypergiant and SUSE, K3s in space — https://www.suse.com/c/hypergiant-and-suse-rgs-taking-kubernetes-to-the-final-frontier/
- [kubespace] KubeSpace (arXiv 2601.21383) — https://arxiv.org/abs/2601.21383
- [kubeedge] KubeEdge README — https://github.com/kubeedge/kubeedge
- [fleet] Rancher Fleet rollout strategy — https://fleet.rancher.io/how-tos-for-users/rollout
- [rancher-dc] Rancher, disconnected clusters — https://documentation.suse.com/cloudnative/rancher-manager/latest/en/cluster-admin/best-practices/disconnected-clusters.html
- [ocm] OCM ManifestWorkReplicaSet — https://main--open-cluster-management.netlify.app/docs/concepts/work-distribution/manifestworkreplicaset
