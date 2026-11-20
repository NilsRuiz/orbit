---
name: pr
description: Open or update a GitHub pull request for the current branch with a Conventional Commits title and the repository template, then follow CI until green. Use when the work on a branch is ready for review.
---

# Pull request

## Preconditions
- `/verify` passed and `/self-review` findings are fixed.
- All work is committed; branch is rebased on the latest `main` if `main` moved
  (`git fetch origin && git rebase origin/main` — only before the PR is opened).

## Steps
1. Push the branch: `git push -u origin HEAD` (never force-push; never push `main`).
2. Create the PR with `gh pr create`:
   - **Title**: Conventional Commits, it becomes the squash commit on `main`
     (e.g. `feat(agent): buffer status events during link outages`).
   - **Body**: fill every section of `.github/pull_request_template.md`:
     summary (what + why), linked issue (`Closes #n`), how it was tested (commands and
     results), risk & rollback, checklist.
   - Use `--draft` if anything is still open.
3. Follow CI: `gh pr checks --watch`. If a job fails:
   `gh run view <run-id> --log-failed`, reproduce locally, fix in a **new commit**, push.
4. Address review comments with new commits; reply to each comment with what changed.
5. Do not merge yourself. Tell me when the PR is green and ready, with the PR URL.
