# Investigation: recovery after a silent link loss

- **Date**: 2026-11-23
- **Issue**: #31 (step D)
- **Status**: Measured. A tuning proposal follows in an ADR (step E).
- **Related**:
  - [2026-11-link-restore-delay](2026-11-link-restore-delay.md): clean cut, #16
  - [ADR-0002](../adr/0002-nats-jetstream-leaf-nodes.md)

## Context

The first investigation measured a **clean** cut: `docker network disconnect` makes both
ends see the connection drop. It explicitly left out the case that matters in orbit, a
**silent** loss. Packets vanish, no FIN or RST is ever received, sockets stay open, and
only application-level pings or TCP's own retransmission limits eventually reveal the
loss.

The question: with the default NATS settings, how long does a node take to notice it lost
the hub, and how long does the agent take to converge once the link is back?

```
operator → KV orbit-desired (hub) ══ space link ══ leaf-sat-N ── agent-sat-N
                                   ▲
                                   └─ silent loss injected here (both directions)
```

## Protocol

Environment:

- local fleet (`make dev-up`) built from `main` at `5c30d22`: 1 hub and 3 edge nodes;
- Docker 29.1.3, `nats:2.11-alpine` (server 2.11.17), agent on Go 1.26.10 with nats.go
  v1.41.0;
- `ORBIT_HEARTBEAT_INTERVAL=5s`; node under test: `sat-1`.

NATS settings, all defaults, read from the hub's `/varz`:

- `ping_interval` = 120 s, `ping_max` = 2, `write_deadline` = 10 s;
- the leaf uses the same image with no ping setting in `leaf.conf`, so the same defaults.

TCP, in the leaf's network namespace: `net.ipv4.tcp_retries2` = 15 (Linux default), base
RTO on the Docker bridge ≈ 200 ms.

**Silent loss**: `link.sh blackhole sat-1` (#33) adds `iptables -j DROP` rules in the
leaf's network namespace, `INPUT` from and `OUTPUT` to the hub's IP. Both directions are
dropped, and no socket is closed.

For each loss duration (30 s, 2 min, 5 min, 8 min):

1. `link.sh blackhole sat-1`, time `tb`.
2. 2 s later, publish a new version: `make desired NODE=sat-1 VERSION=<v>`.
3. At `tb + loss`, `link.sh restore sat-1`, time `tr`.
4. From the container logs (`docker logs -t`), record:
   - the first `Leafnode connection closed` on the leaf and on the hub (**detection**,
     relative to `tb`);
   - the first `Leafnode connection created` on the leaf after `tr` (**reconnect**);
   - the first `"applying <v>"` on the agent (**restore → applying**, relative to `tr`).
5. Record the agent restart count and its `WARN`/`ERROR` lines, including the
   `nats async error` events added in #32.
6. Wait 20 s before the next duration.

The runs took place from 15:10:05 to 15:29:44 UTC, automated with a throwaway script (not
committed). An earlier attempt at 15:08 was aborted after its first duration: the script
formatted numbers with a locale using a decimal comma. Its 30 s result, 24.2 s, is
consistent with the run below.

A separate 93 s loss at about 15:31 UTC sampled the leaf→hub socket with `ss -ti` every
15 s, to check the TCP retransmission backoff.

## Measurements

| Silent loss | Leaf detects | Hub detects | Reconnect after restore | Restore → `applying` |
|---|---|---|---|---|
| 30 s  | — | — | none, same TCP connection | **25.9 s** |
| 2 min | — | — | none | **97.3 s** |
| 5 min | — | — | none | **40.0 s** |
| 8 min | **345.5 s** (`Stale Connection`) | **345.5 s** (`Stale Connection`) | **0.6 s** | **4.3 s** |

- Across all four runs: no agent restart, no `nats async error`, and no version applied
  during a loss. Every published version was eventually applied.
- **8 min run**: after declaring the connection stale, the leaf retried every ≈ 2.5 s
  (54 `dial tcp … i/o timeout` attempts in 135 s), then reconnected 0.6 s after the
  restore.

TCP state of the leaf→hub socket during the 93 s loss (`ss -ti`):

| Time in the loss | `backoff` | Effective retransmission wait (≈ 0.2 s × 2^backoff) |
|---|---|---|
| +15 s | — | — |
| +31 s | 6 | ≈ 13 s |
| +46 s | 7 | ≈ 26 s |
| +77 s | 8 | ≈ 52 s |
| 5 s after restore | cleared | — |

## Analysis

There are two regimes, separated by the moment the loss is detected.

### Before detection (loss shorter than ≈ 4–6 min)

- **The connection survives the loss.** With `ping_interval` 120 s and `ping_max` 2, a
  peer is declared stale only after two unanswered pings, 4 to 6 min into the loss
  depending on the phase of the ping timer. 345.5 s was measured.
- **TCP doubles its retransmission wait on every unanswered attempt, up to 120 s.** While
  the link is down, the leaf's unacknowledged data keeps the retransmission timer running.
  The `ss` samples show the backoff climbing to ≈ 52 s after 77 s of loss.
- **At restore, nothing flows until the next scheduled retransmission.** The delay is
  therefore anything between 0 and the current backoff (up to ~2 min), essentially random
  from the operator's point of view. Hence 25.9 s, 97.3 s, then 40.0 s, in no monotonic
  order.
- **The ordered consumer's 2 × 5 s activity check (#16)** comes on top, once traffic
  flows again.

### After detection (loss longer than ≈ 6 min)

- Both ends drop the connection as stale at the same time.
- The leaf then **dials a fresh connection every ≈ 2.5 s**, so a new TCP connection with no
  backoff is up 0.6 s after the restore.
- The agent converged 4.3 s after the restore: the leaf reconnect, then the ordered
  consumer recreating itself.

### Consequence

**Short losses, the most frequent ones (masking, antenna handover), are the slowest to
recover from**: up to ≈ 97 s measured, and up to ≈ 2 min in theory, against ≈ 4 s once a
loss has been detected. On a ≈ 10 min LEO contact window, that is up to ~15 % of the
window lost before the node even hears from the hub.

The lever is **detection speed**. Pinging more often would move short losses into the
fast regime: the connection is closed during the loss, and a fresh one is dialled as soon
as the link returns. For example, `ping_interval: 10s` and `ping_max: 3` would detect a
loss in ≈ 30–40 s. Trade-offs, to be settled in an ADR (#31, step E):

- **Bandwidth**: one small PING/PONG per interval per connection. Negligible on its own,
  but the link is the scarcest resource of the system.
- **False positives on a degraded link**: high latency, jitter and loss (`link.sh
  degrade`) could make healthy-but-slow links flap. To be measured before choosing values.
- **Scope**: `ping_interval` is a **server-wide** setting in nats-server 2.11. Setting it
  in `leaf.conf` also changes how often the leaf pings the local agent (harmless on
  localhost).
- **Alternative**: shorten TCP's own give-up time (`TCP_USER_TIMEOUT`, `tcp_retries2`).
  NATS does not expose it, and with the defaults TCP would only give up after ≈ 15 min
  (`tcp_retries2` = 15). Ping tuning is the direct lever.

## Limitations

- **One run per duration.** The pre-detection delay is random by nature (backoff phase),
  so the 25.9 / 97.3 / 40.0 s figures are samples, not a distribution.
- **Local network.** The base RTO is ≈ 200 ms on the Docker bridge. On a satellite link
  (RTT of 0.5 s or more on GEO, tens of ms on LEO), the base RTO and every backoff step
  would be larger.
- **Light traffic.** Only agent heartbeats (every 5 s) and JetStream flow cross the link.
  With heavier traffic, socket buffers could fill and `write_deadline` (10 s) could close
  the connection earlier. Not observed here.
- **iptables DROP is a perfect blackhole.** A real fade often degrades before dropping
  entirely; `link.sh degrade` was not combined with the blackhole.
- **Leaf ping settings inferred, not read.** The leaf exposes no monitoring port; its
  defaults are inferred from the shared image and the absence of ping settings in
  `leaf.conf`. This is consistent with both ends detecting at the same moment.
