# ADR-0006: Tune leaf node pings to detect silent link losses quickly

- **Status**: Proposed
- **Date**: 2026-11-24

## Context

A real radio loss is **silent**: packets vanish, no FIN or RST is ever received, and both
ends keep the connection open. With the NATS defaults (`ping_interval` 120 s, `ping_max`
2), a leaf node notices such a loss only after 4–6 min.
[2026-11-silent-link-loss](../investigations/2026-11-silent-link-loss.md) measured what
that costs:

- **A shorter loss is never detected.** The TCP connection survives it, but TCP has doubled
  its retransmission wait during the loss (`backoff:8` ≈ 52 s after 77 s of loss).
- **After the link returns, nothing flows until the next retransmission.** Restore →
  agent applying took 25.9 s, 97.3 s and 40.0 s for losses of 30 s, 2 min and 5 min.
- **Once a loss is detected, the leaf redials every ≈ 2.5 s**, and recovery takes ≈ 4 s.

So the **short losses** (masking, antenna handover), the most frequent ones, are the
slowest to recover from: up to ≈ 2 min of a ≈ 10 min LEO contact window. Detection speed is
the lever.

## Decision

Set **`ping_interval: 10s`** and **`ping_max: 3`** on the edge leaf nodes
(`hack/compose/nats/leaf.conf`, and `config.merge` of the `nats-leaf` HelmRelease). A
silent loss is then detected in ≈ 30–40 s, and the leaf redials instead of waiting on TCP
backoff. The hub keeps the defaults.

This requires **nats.go ≥ v1.55** in the agent (#35, fixed in #36). Older versions could
leave the KV watch stuck forever when the stale detection fired just after a restore.

## Alternatives considered

- **Keep the defaults**: recovery from short losses stays at 25–97 s (≈ 2 min worst case),
  random from the operator's point of view.
- **Shorten TCP's own give-up time** (`TCP_USER_TIMEOUT`, `net.ipv4.tcp_retries2`): NATS
  does not expose a socket option for it, and the sysctl is per network namespace and per
  host, outside what the leaf chart and the compose fleet manage. Default give-up is
  ≈ 15 min, too slow anyway.
- **More aggressive pings** (e.g. 5 s × 2, ≈ 10–15 s detection): more traffic on the
  scarcest resource, and a higher false-positive risk on high-latency links (GEO RTT
  ≥ 0.5 s, plus TCP retransmissions under loss). With 10 s × 3, a connection is dropped
  only after ≈ 30 s without any PONG.
- **Also tune the hub**: it would only change when the hub drops its side, while the leaf
  is the one that must redial. Measured: with only the leaf tuned, both ends already close
  at the same moment, because the leaf sends `-ERR 'Stale Connection'` to the hub before
  closing.

## Consequences

Measured on the local fleet (`link.sh blackhole`, 30 s silent loss, nats.go v1.56.0):

- ➕ Restore → applying in **1.8–14.4 s (median ≈ 9 s, 10/10 runs)**, against 25.9–97.3 s
  with the defaults.
- ➕ Detection in ≈ 36 s on both ends: the dead connection is replaced instead of nursed
  back through TCP backoff.
- ➖ **Bandwidth**: one PING/PONG pair (a few bytes each, plus TCP/IP headers) every 10 s
  per leaf. Negligible on its own; it is the only standing cost on the link besides the
  agent heartbeats.
- ➖ **False positives on a degraded link**, a flapping leaf.
  - Measured: 5 min of `link.sh degrade` (800 ms ± 200 ms, 20 % loss) on two nodes gave
    0 stale disconnect, 0 reconnect, and versions applied within 2 s.
  - Caveat: `tc netem` degrades only the leaf's egress (ping RTT ≈ 0.8 s, one lossy leg).
    A symmetric profile and real GEO latencies remain to be measured.
  - Mitigation: revisit the values when edge VMs exist (phase 3) and in the phase 5 chaos
    work.
- ➖ **Scope**: `ping_interval` is server-wide in nats-server. On the leaf it also pings
  local clients (the agent) every 10 s; harmless on localhost.
- ➖ **A short loss can now end with a reconnect** right after the restore. That path
  exposed #35; it is covered by nats.go ≥ v1.55. A deterministic regression test comes with
  the silent proxy mode of the convergence test (#31, step C).
