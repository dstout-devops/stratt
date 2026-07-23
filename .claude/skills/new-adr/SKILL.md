---
name: new-adr
description: >-
  Create a new Architecture Decision Record under docs/adr/. The charter mandates public ADRs from
  the first tagged release (§1.3, §7.2); capture decisions of consequence as they are made.
disable-model-invocation: true
---

Create a new ADR for: **$ARGUMENTS**

0. **Prior-art scan (MANDATORY — do this BEFORE drafting).** The corpus is 100+ ADRs plus a live
   estate and codebase; a decision that *feels* greenfield often is not (a new ADR once designed a
   provisioning reach-path while ADR-0058 already shipped one — caught only in review). So, before
   writing anything: launch the **`adr-scout`** subagent with a one-to-three-sentence description of
   this decision, OR (for a small/obvious ADR) do the scan yourself — grep `docs/adr/*.md` **bodies**
   (not just titles) for the concept + its synonyms/mechanism words, and grep `estate/`, `core/`,
   `plugins/`, `contracts/`, `proto/` for an already-shipped seam. **Read the related ADRs.** In the
   ADR you write, cross-reference them and state the reconciliation each demands — supersede, refactor,
   extend, or coexist — and never claim greenfield when a coupled/overlapping seam already ships.
1. List `docs/adr/` and find the highest existing `NNNN-*.md` number. The next ADR number is that
   + 1, zero-padded to 4 digits (`0001`, `0002`, …).
2. Read `docs/adr/0000-template.md` for the structure.
3. Derive a short kebab-case slug from the decision title in `$ARGUMENTS`. New file:
   `docs/adr/NNNN-<slug>.md`.
4. Fill in the template:
   - **Status:** `Proposed` (the human accepts/supersedes later).
   - **Context:** the problem and forces. Tie to the relevant charter section(s) by § number.
   - **Decision:** what was decided, in active voice.
   - **Charter alignment:** which Founding Disciplines (§1) / non-goals this respects, and any it
     tensions against. If it touches §1 or §2, note that those require the highest review bar.
   - **Consequences:** positive, negative, and follow-ups (including any CI evergreen gate to add).
   - **Alternatives considered:** with why each was rejected.
5. Do not fabricate a decision — if the details in `$ARGUMENTS` are thin, ask the human for the
   missing context before writing, or leave clearly-marked `TODO` placeholders.
6. If the decision touches the data model, Contracts, vocabulary, or a dependency, recommend running
   the `charter-guardian` (and `dependency-scout`, if a dependency) subagent before the ADR is
   accepted.
7. Report the created path. Do not commit unless asked.
