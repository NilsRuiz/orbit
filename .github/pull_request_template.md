## Summary

<!-- What does this change and WHY? One paragraph. -->

Closes #

## How it was tested

<!-- Commands run and their results. New/changed tests. For bug fixes: the regression test
     that fails without the fix. -->

- [ ] `make verify` passes locally
- [ ] New/updated tests: …
- [ ] Manual check (if relevant): …

## Risk & rollback

<!-- What could break? Blast radius (hub / some nodes / whole fleet)? Backward compatible with
     edge nodes still running the previous version? How do we roll back? Cost impact? -->

## Checklist

- [ ] Title follows Conventional Commits (`type(scope): summary`)
- [ ] Docs updated (README / component README / runbook) where behaviour changed
- [ ] ADR added or updated if a design decision changed
- [ ] No secrets, credentials or state files in the diff
- [ ] Logs/metrics make new failure modes observable
