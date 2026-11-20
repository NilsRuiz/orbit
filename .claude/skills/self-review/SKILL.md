---
name: self-review
description: Review the current branch diff like a senior SRE reviewer before opening a pull request — correctness, tests, concurrency, security, operability, docs. Use before /pr or when asked to review changes.
---

# Self-review

Review `git diff origin/main...HEAD` as if someone else wrote it. Be critical; report
findings, then fix them (ask me first if a fix changes the design).

## Checklist

**Correctness**
- Does it meet the acceptance criteria? Edge cases: empty input, duplicates, restarts,
  link cut mid-operation, out-of-order or replayed messages, clock skew.
- Is every state transition persisted before it is reported (agent)? Idempotent reconcile (operator)?

**Tests**
- Would the tests fail if the change were reverted? Is the bug fix covered by a regression test?
- No sleeps-as-synchronisation where an event can be awaited; no test depends on ordering of maps.

**Concurrency & resources (Go)**
- Each goroutine stops on context cancellation; channels closed by their owner only.
- Shared state protected; would `-race` catch anything? Timers/tickers stopped; no leaks.

**Security**
- No secrets in code, logs, or Terraform outputs (mark `sensitive = true`).
- Least privilege (IAM roles, RBAC, NATS permissions); containers non-root.
- New dependency: maintained, licensed, pinned?

**Operability (SRE)**
- Failures are observable: structured logs with context, metrics where relevant.
- Safe to roll out and to roll back? Backward compatible with nodes still on the old version
  (edge nodes update late)? Config defaults safe?
- Cost impact for cloud changes (ADR-0005).

**Maintainability**
- Names, small functions, no dead code, comments explain *why*.
- Docs/ADR/runbook updated.

## Output
A list of findings grouped as 🔴 must fix / 🟡 should fix / 🟢 nit, each with file:line
and a suggested fix. Then fix 🔴 and 🟡.
