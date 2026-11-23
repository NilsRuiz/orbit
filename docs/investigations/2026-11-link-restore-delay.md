# Investigation: desired-state delay after a link restore

- **Date**: 2026-11-23
- **Issue**: #16
- **Status**: Measured, root cause partially confirmed, no fix yet
- **Related**: [ADR-0002](../adr/0002-nats-jetstream-leaf-nodes.md) (NATS JetStream with leaf nodes)

## Context

ADR-0002 promises that a desired state published while a node is out of range is
delivered **when the node comes back**. It does not say how quickly. Contact windows are
short (about 10 minutes for a LEO satellite), so every second between "link back" and
"agent starts converging" is lost window time, and that delay adds up across the waves
of a progressive rollout (ADR-0004).

The question: after a link cut, how long does the agent take to receive a version
published during the cut, and is that delay abnormal?

How the pieces fit together:

```
operator → KV orbit-desired (hub) ══ space link ══ leaf-sat-N ── agent-sat-N
```

The agent only talks to its local leaf node. Its own NATS connection therefore stays up
during a link cut. Only the leaf ↔ hub connection is broken.

## Protocol

Environment: local fleet (`make dev-up`, `hack/compose/docker-compose.yml`), 1 hub and
3 edge nodes, Docker 29.1.3, `nats:2.11-alpine` image (hub reports 2.11.17), agent built
with `nats.go` v1.41.0. Agent `ORBIT_HEARTBEAT_INTERVAL=5s`; simulated payload with a
fixed 2 s apply delay. Node under test: `sat-1`.

For each cut duration (5 s, 15 s, 30 s, 60 s):

1. `./hack/scripts/link.sh cut sat-1` (`docker network disconnect` of the leaf from the
   "space link" network).
2. 2 s into the cut, publish a new version:
   `make desired NODE=sat-1 VERSION=<v>`.
3. At the end of the cut, record the time `t0` and run `./hack/scripts/link.sh restore sat-1`.
4. Poll the agent logs for the first `"applying <v>"` transition and record its timestamp.
   **Delay = applying timestamp − `t0`.**
5. Record the agent container restart count and the leafnode lines of the leaf logs.
6. Wait 15 s before the next run.

The run was automated with a throwaway script (not committed). A first run, at
09:28–09:37 UTC, gave the same qualitative result but its timing collection was broken.
The figures below come from the second run, at 09:45–09:49 UTC.

## Measurements

| Cut | Restore → `applying` | Leaf detects broken connection | Leaf reconnected | Agent restarts |
|---|---|---|---|---|
| 5 s  | **11.9 s** | +1.9 s (`Write Error`) | +2.9 s | 0 |
| 15 s | **9.8 s**  | +0.4 s (`Read Error`)  | +1.4 s | 0 |
| 30 s | **14.8 s** | +4.8 s (`Read Error`)  | +5.8 s | 0 |
| 60 s | **14.8 s** | +4.8 s (`Read Error`)  | +5.8 s | 0 |

The leaf times are relative to the restore. Every version published during a cut was
eventually applied and verified healthy. No agent restart was observed, and no
`"shutting down"` log line appeared.

A link-up baseline (`kv put` → `applying` with no cut) was not timed.

## Breakdown of the delay

The delay is made of three consecutive steps:

1. **Leaf node detects that its hub connection is dead: 0.4–5 s after the restore.**
   Counter-intuitively, the leaf notices nothing *during* the cut. The TCP socket stays
   open and silent, and it fails (`Read Error` / `Write Error`) only once traffic can flow
   again after the restore.
2. **Leaf node reconnects to the hub: about 1 s.** This is the default reconnect
   interval of a leafnode remote.
3. **The KV watch notices it missed an update: up to about 10 s.** The message pushed
   during the cut is lost on the broken connection. `kv.Watch` uses an ordered consumer
   (nats.go `js.go`): it expects an idle heartbeat every 5 s and checks for activity every
   2 × 5 s = 10 s (`orderedHeartbeatsInterval`, `hbcThresh`). Those values are hard-coded
   in nats.go v1.41.0. On a gap or a missed heartbeat, it recreates the consumer from the
   last sequence it saw and receives the latest value.

Step 3 dominates. Because the agent's own connection never drops, it gets no
"reconnected" event to react to: it can only wait for the ordered consumer's timer.

### Hypothesis disproved

Reading the nats.go code first suggested a worse outcome. During a long cut, the ordered
consumer's attempt to recreate itself would fail with a non-retryable error and close the
watch. `WatchDesired` would then exit, the agent would stop with exit code 0 and depend
on the container restart policy. On Kubernetes, that would mean `CrashLoopBackOff` delays
of up to 5 minutes.

The measurements disprove this: 0 restarts across all runs, including cuts of 30 s and
60 s. The theory is kept here as a reminder that reading the code was not enough: this
behaviour had to be measured.

## Conclusion

- **Functionally correct**: no update is lost, and the delay is bounded (≤ 15 s here)
  and does not grow with the cut duration. That is the ADR-0002 promise.
- **Slower than necessary**: the leaf is back on the hub within 1.4–5.8 s, yet the
  agent waits up to about 10 s more, because a hard-coded client timer dominates the
  delay rather than the network. That costs about 2–3 % of a LEO contact window per
  node, repeated at every rollout wave.
- Tracked as a bug in #16, with a target of ≤ 3 s. Candidate directions, not yet
  evaluated:
  - an explicit `kv.Get` catch-up, periodic every few seconds (cheap) or triggered when
    hub traffic resumes, on top of the watch;
  - a shorter leafnode `ping_interval`, so the leaf detects a dead connection sooner.

## Limitations

- **Simulated link**: `docker network disconnect` is not a radio blackout. With a real
  link, or with 100 % packet loss (`tc … loss 100%`), the TCP connection may survive the
  cut or be detected only after the leafnode `ping_interval` (2 min by default). The
  leaf-detection step could then be much longer, or absent. Not measured.
- **Degraded link** (`link.sh degrade`: latency, jitter, loss) not measured.
- **Small sample**: one node, one run per cut duration (plus one run without usable
  timings). No statistical spread; figures are indicative.
- **Timing resolution**: logs polled every 200 ms. `t0` is taken just before
  `link.sh restore`, whose first command is the network reconnect.
- **Single version**: agent built with nats.go v1.41.0 and a NATS 2.11 server. The
  ordered-consumer timers may differ in other releases.
- **Simulated payload**: the fixed 2 s apply delay comes after the measured interval, so
  it is excluded.
