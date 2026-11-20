---
name: verify
description: Run the repository quality gates (format, lint, tests, terraform/gitops/shell validation) on the areas touched by the current change. Use before every commit and before opening or updating a pull request.
---

# Verify

Goal: catch regressions locally, exactly as CI would, before anything is committed.

## Steps

1. Find what changed (committed on the branch + working tree):
   ```bash
   git diff --name-only origin/main...HEAD; git diff --name-only HEAD; git ls-files --others --exclude-standard
   ```
2. Run the matching targets (run `make verify` if several areas or unsure):
   | Changed paths | Command |
   |---|---|
   | `agent/**`, `operator/**` (Go) | `make verify-go` |
   | `infra/terraform/**` | `make verify-tf` |
   | `gitops/**` | `make verify-gitops` |
   | `hack/**/*.sh` | `make verify-shell` |
   | docs only (`*.md`) | nothing to run; check links and file names |
3. If a check fails: read the error, fix the **cause**, re-run. Never:
   - delete, skip (`t.Skip`), or weaken a test or an assertion to get green;
   - add `//nolint`, `# tflint-ignore`, `# shellcheck disable` without a one-line justification;
   - commit with failing checks "to fix later".
4. If a tool is missing locally (e.g. `tflint`), say so explicitly and list what was
   not verified — do not claim success.

## Report

End with a short table: check → ✅ / ❌ / ⚠️ not run (reason). Mention new or changed tests.
