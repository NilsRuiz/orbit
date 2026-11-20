---
name: feature
description: Full development cycle for a change in Orbit — issue, branch, plan, tests, implementation, verification, commits and pull request. Use when starting any new feature, bug fix or refactor (e.g. "/feature fix status lost during link cut").
---

# Feature workflow

Task: $ARGUMENTS

Follow these phases in order. Stop and wait for my approval where marked ⏸.

## 1. Understand
- Read the relevant code, the component README and the ADRs in `docs/adr/` that apply.
- If a GitHub issue exists (`gh issue view <n>`), use it; otherwise propose a title and
  body and create it with `gh issue create` after my OK.
- Restate the problem in 2–3 sentences, including acceptance criteria.

## 2. Plan ⏸
Present, in French:
- approach and alternatives considered (one line each);
- files to create/modify;
- **test plan**: which tests prove it works, which test reproduces the bug (for fixes);
- risks (concurrency, reconnection, cost, security) and how they are handled;
- whether an ADR is needed (design decision, new dependency, new component) → `/adr`.
Wait for my approval before writing code.

## 3. Branch
```bash
git switch main && git pull --ff-only
git switch -c <type>/<short-kebab-description>
```
Do this as a separate command before any commit (hooks block commits on `main`).

## 4. Test first, then implement
- Bug fix: write the regression test, run it, **show it fails**, then fix.
- Feature: write tests for the acceptance criteria alongside the code.
- Keep business logic behind interfaces so it is testable without NATS/Kubernetes.
- Small steps: after each coherent step, run `/verify` and `/commit`.

## 5. Self-review
Run `/self-review` on the full branch diff and fix findings.

## 6. Documentation
Update README / component README / runbooks / ADR status as needed. If behaviour visible
in the demo changed, update the demo scenario.

## 7. Pull request
Run `/pr`, then follow CI to green.

## 8. Wrap-up
Summarise in French: what changed, how it was tested, what is left (as issues).
