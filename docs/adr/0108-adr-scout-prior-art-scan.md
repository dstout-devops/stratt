# ADR 0108 — `adr-scout` + a mandatory prior-art scan before drafting an ADR

- **Status:** **Proposed** (2026-07-23, steward)
- **Date:** 2026-07-23
- **Deciders:** Project steward (dstout)
- **Charter sections:** §1.3 (public ADRs / triage from the first release — the ADR corpus is the
  durable decision memory) · §1.8 (never hide diagnosis — a new decision must surface, not bury, the
  prior seams it touches). **Extends ADR-0001** (charter encoded into the `.claude/` control plane:
  the review-subagent pattern + the `new-adr` skill; ADR-0001 already names the token/always-loaded-
  context trade-off "mitigated by … on-demand skills" — this ADR is the next step on that thread).

## Context

The corpus is 100+ ADRs plus a live estate and a large codebase. An agent (token-bound, stateless
across sessions) cannot hold all of it and cannot be trusted to *remember* to search for prior art —
so it drafts on the pattern in front of it. That produced a real miss: **ADR-0107 (EC2 provisioning)
was drafted as if provisioning were greenfield, while ADR-0058 already shipped a provisioning
reach-path** (`Intent/Compute`'s `builder:` field, live in `estate/intents/*.yaml`). The new ADR's
guardrail and "no consumer / zero-line swap" claims contradicted accepted, shipped design — caught
only by charter-guardian, *after* drafting, at review cost. The failure was **discovery, not ADR
quality**: `grep -il provision docs/adr/` finds ADR-0058 instantly; the author simply didn't scan.

This is inherent as a decision corpus grows (§1.3's durable memory has a discovery cost), but it is
mitigable — and the cheapest mitigation is to make the scan a **required step**, not a matter of agent
diligence.

## Decision

Add a **prior-art scan** as a mandatory, first step of authoring any ADR / non-trivial design, backed
by a dedicated subagent:

1. **`adr-scout`** (`.claude/agents/adr-scout.md`) — a read-only subagent (Read/Grep/Glob/Bash) whose
   single job is to **kill the greenfield illusion**: given a short decision description, it searches
   the ADR corpus **by body** (titles lie), the **live estate/code/contracts/proto for already-shipped
   seams** (the load-bearing step — a live declaration is the strongest "not greenfield" signal), and
   the cross-ref graph, and returns a ranked *Prior Art* list with, per hit, *what it already ships*
   and *the reconciliation owed* (supersede / refactor / extend / coexist). Verdict `GREENFIELD` (rare,
   with what was searched) or `RECONCILE` (the ADRs + seams the new ADR must explicitly address). It
   finds prior art; it does not judge design (charter-guardian) or naming (vocabulary-linter).
2. **`/new-adr` step 0** (`.claude/skills/new-adr/SKILL.md`) — before drafting, launch `adr-scout` (or
   self-scan for a small ADR), read the related ADRs, and reconcile with them in the new ADR; never
   claim greenfield when a coupled/overlapping seam already ships.
3. **`CLAUDE.md` Workflow** — the pre-design scan joins `charter-guardian` / `dependency-scout` /
   `vocabulary-linter` as a named step, so it is discoverable at the point of work.

This **extends ADR-0001**'s "encode the charter into the control plane" posture: `adr-scout` is the
fourth review subagent, and the scan is a new pre-design gate. It moves the catch that guardian
performed *after* drafting to *before* it — earlier and cheaper.

## Consequences

- **Positive.** The exact miss class (greenfield illusion → duplicating/coupling-to/contradicting a
  shipped seam) is caught before drafting, at grep cost. On-demand + scoped (ADR-0001's token thread):
  the scan loads only the 3–5 relevant ADRs, not 108. Dogfooded on authoring: the prior-art scan for
  *this* ADR surfaced ADR-0001 as the decision it extends.
- **Negative / cost.** One subagent invocation (or a self-scan) per new ADR — small against a
  re-drafted ADR. A scan is only as good as the term expansion; `adr-scout` is instructed to prefer
  `RECONCILE` and name candidates when unsure, so a false-greenfield (the failure mode) is disfavoured.
- **Scope.** Ships the process + subagent. Does **not** yet add the durable concept→ADR index — that
  is the booked follow-up (below), the structural complement so discovery does not depend on the
  author choosing the right search term.

## Alternatives considered (rejected)

- **Rely on agent diligence (status quo).** Rejected: it produced the ADR-0107 miss. A stateless,
  token-bound author will not reliably self-initiate the scan; making it a required step is the fix.
- **Consolidate / rewrite ADRs to shrink the corpus.** Rejected: ADRs are immutable records
  (supersede, never rewrite). The problem is a routing/discovery layer, not corpus size.
- **Have charter-guardian do prior-art discovery.** Rejected (separation of concerns): guardian judges
  a *drafted* design against the charter; prior-art discovery must happen *before* drafting and is a
  distinct, cheaper, read-only search. Overloading guardian keeps the catch late.

## Follow-ups

1. **The durable concept→ADR index** (the structural complement): a `topics:` frontmatter tag on ADRs
   + a **generated** by-topic view + a freshness lint (mirroring `generate:check` / `migrate:lint`, so
   it cannot silently rot), so discovery does not depend on the author guessing the right keyword.
   Continues ADR-0001's on-demand-context thread; its own ADR + a 108-ADR tag backfill.
2. Re-scope `adr-scout`'s search-term expansion as the corpus grows; consider seeding it from the
   `topics:` tags once (1) lands.
