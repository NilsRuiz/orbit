# Contributing to Orbit

This document describes the software development lifecycle used in this repository.
It follows common industry practice: trunk-based development, Conventional Commits,
mandatory review and CI before merge, automated semantic releases.

## 1. Lifecycle overview

```
 Issue ──► Branch ──► Plan ──► Test + Code ──► Verify ──► Commit(s) ──► PR ──► CI + Review ──► Squash merge ──► Release PR ──► Tag + Changelog
  (why)   (short-    (ADR if   (TDD for       (make     (Conventional)  (template)   (required)     (main stays      (release-please)
           lived)     design)   fixes)         verify)                                              releasable)
```

## 2. Issues first

Every change starts from a GitHub issue (even a one-liner): it records *why*. Labels are
defined as code in `hack/scripts/sync-labels.sh` (run it once per repository):
`type/bug|feature|chore`, `area/agent|operator|infra|gitops|hack|ci`, `priority/high|low`.
With Claude Code, use `/issue`.

## 3. Branches — trunk-based development

- `main` is protected and always releasable.
- Branches are short-lived (ideally < 2 days), created from an up-to-date `main`:
  `<type>/<short-description>` — e.g. `feat/agent-metrics`, `fix/kv-watch-reconnect`.
- Rebase on `main` before opening the PR if it moved (`git pull --rebase origin main`);
  once the PR is under review, add commits instead of force-pushing.

## 4. Commits — Conventional Commits

```
<type>(<scope>): <imperative summary, ≤ 72 chars, no final period>

<body: what and WHY, wrapped at 72 chars>

Refs: #123
BREAKING CHANGE: <description>   (only if applicable)
```

| Type | Use for | Release impact |
|---|---|---|
| `feat` | new behaviour | minor |
| `fix` | bug fix | patch |
| `perf` | performance | patch |
| `refactor` | no behaviour change | none |
| `test` | tests only | none |
| `docs` | docs/ADR only | none |
| `build` / `ci` | build system / pipelines | none |
| `chore` | maintenance, deps | none |

Scopes: `agent`, `operator`, `infra`, `gitops`, `hack`, `ci`, `docs`, `deps`.
`!` after the type/scope (`feat(agent)!:`) or a `BREAKING CHANGE:` footer → major.

One logical change per commit; each commit should build and pass tests.

## 5. Testing strategy (test pyramid)

| Level | Where | Run |
|---|---|---|
| Unit | pure logic (`internal/reconcile`, wave planning) with fakes | every commit |
| Integration | in-process NATS server, `envtest` for the operator | every commit |
| End-to-end | docker compose fleet / kind + chaos scenarios | CI on PR (phase 2+) |
| Infra | `fmt`, `validate`, `tflint`, `checkov`; `plan` commented on PR | every PR |

Rules: a bug fix comes with a regression test that fails without the fix; tests are never
deleted or weakened to get a green build; flaky tests are fixed or quarantined with an issue.

## 6. Pull requests

- Title in Conventional Commits format (it becomes the squash commit, enforced by CI).
- Fill in the template: what/why, how it was tested, risk & rollback.
- Small PRs (< ~400 changed lines). Split otherwise.
- Required to merge: green CI, 1 approval (self-review checklist when working solo),
  conversations resolved, branch up to date.

## 7. Releases — semantic versioning, automated

[release-please](https://github.com/googleapis/release-please) reads Conventional Commits on
`main`, opens a *release PR* with the version bump and `CHANGELOG.md`; merging it creates the
tag and GitHub release, which triggers the image build for that version.

## 8. Recommended GitHub settings (Settings → Rules → Rulesets on `main`)

- Require a pull request before merging (1 approval, dismiss stale approvals)
- Require status checks: `agent / test`, `terraform / static`, `gitops / validate`,
  `pr-title / lint`
- Require branches to be up to date; require conversation resolution
- Require linear history; allow **squash merge** only; block force pushes and deletions
- Enable secret scanning + push protection, Dependabot alerts and security updates

## 9. Local setup

```bash
pipx install pre-commit && pre-commit install --install-hooks \
  && pre-commit install --hook-type commit-msg
```

Tools: Go ≥ 1.24, Docker, Terraform, tflint, golangci-lint, shellcheck, jq, gh.
