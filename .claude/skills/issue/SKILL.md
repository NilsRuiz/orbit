---
name: issue
description: Create a GitHub issue for Orbit (bug, feature or chore) with the repository's labels, milestone and template sections. Use when a bug or suspicious behaviour is observed, a feature or task is identified, or follow-up work must not be forgotten (e.g. "/issue bug: desired state is slow after link restore").
---

# Issue

Request: $ARGUMENTS

Issues record *why* work exists. Every change in this repository starts from one
(`CONTRIBUTING.md`, section 2).

## 1. Check the prerequisites
- `gh auth status` must succeed. If not, stop and ask me to run `gh auth login`.
- `gh label list` must contain the repository labels (`type/*`, `area/*`, `priority/*`).
  If any is missing, propose running `./hack/scripts/sync-labels.sh` and wait for my OK.

## 2. Classify
| Type | Use when | Label |
|---|---|---|
| bug | something behaves unexpectedly or suspiciously | `type/bug` |
| feature | new capability or improvement | `type/feature` |
| chore | maintenance, refactor, docs, tooling, investigation follow-up | `type/chore` |

Add exactly one `area/*` label: `agent`, `operator`, `infra`, `gitops`, `hack`, `ci`.
Add `priority/high` or `priority/low` only if I said so or it is obvious (blocks the
current phase / explicitly deferred).

## 3. Gather the content
Ask me for anything missing; never invent observations, numbers or logs.

**Bug** — sections of `.github/ISSUE_TEMPLATE/bug.yml`:
- `## What happened` — the observation as it was first seen, not the conclusion.
- `## Expected behaviour`
- `## Steps to reproduce` — numbered, ideally with the local fleet (`make dev-up`, `make link-cut` …).
- `## Area`

**Feature** — sections of `.github/ISSUE_TEMPLATE/feature.yml`:
- `## Problem` — what problem, for whom (fleet operator, on-call SRE…).
- `## Acceptance criteria` — observable, testable, "Given … when … then …".
- `## Design notes` — options, whether an ADR is needed.
- `## Area`

**Chore**: `## Context`, `## Task`, `## Done when`.

Write the issue in English. Title: short, specific, no trailing period, describes the
symptom for a bug (`Desired state takes 10–15 s to reach the agent after a link restore`),
the outcome for a feature (`Agent exposes Prometheus metrics`). No "Fix"/"Bug:" prefix:
the label carries the type.

## 4. Avoid duplicates and attach to a milestone
- Search open issues: `gh issue list --state open --search "<keywords>"`. If one looks like
  the same thing, show it and ask whether to comment there instead.
- List milestones: `gh api repos/{owner}/{repo}/milestones --jq '.[].title'`. Use the
  current phase milestone if it exists and the issue belongs to it; otherwise none.

## 5. Create ⏸
Show the full command and wait for my approval:
```bash
gh issue create --title "<title>" --label "type/<t>,area/<a>" [--milestone "<m>"] \
  --body "$(cat <<'EOF'
## What happened
…
EOF
)"
```
Use `--title`/`--body` (the issue forms in `.github/ISSUE_TEMPLATE/*.yml` cannot be filled
from the CLI); keep the same section headings so the issue looks like a form submission.

## 6. Report
Give the issue number and URL, and remind me to reference it in commits/PRs
(`Refs: #n`, or `Closes #n` in the PR that resolves it). Do not edit, close or comment on
other issues unless I ask.
