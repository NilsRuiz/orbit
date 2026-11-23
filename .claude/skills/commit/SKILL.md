---
name: commit
description: Create clean, atomic git commits following Conventional Commits, after verifying the change. Use whenever changes need to be committed.
---

# Commit

## Preconditions
- Current branch is not `main` (`git branch --show-current`). If it is, create a branch first.
- `/verify` passed for the areas being committed.

## Steps
1. Review what will be committed: `git status` and `git diff` (and `git diff --staged`).
2. Split into **atomic commits**: one logical change each (e.g. a refactor separate from
   the feature that needs it; tests may go with the code they test). Stage precisely with
   `git add <paths>` — never `git add -A` blindly. Check nothing unintended is staged
   (binaries, `.env`, `*.tfstate`, `*.tfvars`, editor files, debug logs).
3. Write the message:
   ```
   <type>(<scope>): <imperative summary, lower case, ≤ 72 chars, no period>

   <why this change is needed and what it does; wrap at 72 chars.
   Mention trade-offs or alternatives when relevant.>

   Refs: #<issue>
   ```
   - types: feat, fix, perf, refactor, test, docs, build, ci, chore
   - scopes: agent, operator, infra, gitops, hack, ci, docs, deps
     (`release` is reserved for release-please PRs)
   - breaking change: `feat(agent)!: …` + `BREAKING CHANGE: …` footer
   - good: `fix(agent): resume kv watch after leaf node reconnect`
   - bad: `fixed stuff`, `WIP`, `update files`, `feat: Added new feature.`
4. Commit with `git commit -m "<subject>" -m "<body>"` (or a heredoc). Never use
   `--no-verify`, never `--amend` a commit that is already pushed.
5. If a pre-commit hook fails: fix the issue, re-stage, create the commit again.
6. Show `git log --oneline -n 5` at the end.
