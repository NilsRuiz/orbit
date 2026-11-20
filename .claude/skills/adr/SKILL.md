---
name: adr
description: Write a new Architecture Decision Record in docs/adr when a design decision is made or changed (new component, dependency, protocol, cloud resource, or trade-off). Use when a plan changes the architecture or I ask "why X rather than Y".
---

# ADR

Decision to record: $ARGUMENTS

1. List existing ADRs (`ls docs/adr`) and take the next number (4 digits).
2. Copy `docs/adr/0000-template.md` to `docs/adr/NNNN-<kebab-title>.md`.
3. Fill it in English, one page maximum:
   - **Context**: constraints that matter (intermittent links, edge resources, cost, skills).
   - **Decision**: one or two sentences, affirmative.
   - **Alternatives considered**: at least two, each with the concrete reason it lost.
   - **Consequences**: ➕ and ➖, and how each ➖ is mitigated.
4. Status `Proposed` until I approve, then `Accepted`. If it replaces an ADR, set the old
   one to `Superseded by ADR-NNNN` — never rewrite an accepted ADR's decision.
5. Link the ADR from the README or component README if it changes how things work.
6. Commit separately: `docs(adr): add ADR-NNNN <title>`.
