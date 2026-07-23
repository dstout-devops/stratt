# ADR 0109 — The ADR knowledge graph: a generated subsystem map for discovery

- **Status:** **Proposed** (2026-07-23, steward)
- **Date:** 2026-07-23
- **Deciders:** Project steward (dstout)
- **Charter sections:** §1.3 (the ADR corpus is the durable, public decision memory — discovery must
  scale with it) · §1.8 (never hide diagnosis — a new design must surface, not bury, the prior
  decisions it touches). **Builds on ADR-0108** (this is its booked Follow-up #1 — `adr-scout` + the
  prior-art scan; ADR-0108 Follow-up #1 is closed by this ADR) and **ADR-0001** (charter encoded into
  the `.claude/` control plane; the on-demand-context / token-budget discipline this ADR must honor).

## Context

`adr-scout` (ADR-0108) makes the prior-art scan a required step, but it still relies on the author
choosing good search terms. ADR-0108 booked the structural complement: a generated topic index +
freshness lint so discovery doesn't depend on the keyword. This ADR delivers it — and, dogfooding
ADR-0108, the `adr-scout` scan for *this* decision surfaced eight things it must reconcile with (below).
That is the whole thesis working: an index/graph that makes *adjacency* explicit, so a new design sees
the neighbouring subsystems whose decisions it must reconcile with — the exact failure (ADR-0107 vs the
already-shipped ADR-0058 provisioning reach-path) that motivated ADR-0108.

Grounding in context-engineering practice (Anthropic, *Effective context engineering for AI agents*;
the knowledge-base-index / KG-for-agents literature): the useful form for an agent is a small **router**
that an agent reads like a table of contents and then fetches the few relevant docs — *not* a payload;
it must be **generated + persisted** (a pure render), because a hand-kept graph rots ("ontologies demand
maintenance"). Those three constraints — router-not-payload, generated-not-hand-kept, lint-gated — drive
the design.

## Decision

A single curated ontology, `docs/adr/topics.json`, is the source of truth: ~20 **subsystems**, each with
its ADR numbers and its `depends_on` subsystem edges. A stdlib-only Go generator (`tools/adrmap`) renders
it into **`docs/adr/MAP.md`** — a mermaid subsystem graph (nodes tagged with ADR numbers) + a by-subsystem
index + an ADR→subsystem reverse index. A CI gate **`task adr:index:check`** fails the build if MAP.md is
stale *or* any ADR is untagged.

### D1 — One ontology file, not per-ADR frontmatter (refines ADR-0108 Follow-up #1)

ADR-0108 sketched a `topics:` frontmatter tag per ADR. This ADR uses a **single `topics.json`** instead,
for two reasons: (a) the graph's *edges* (`subsystem → subsystem`) are a graph-level property that per-ADR
frontmatter cannot express; (b) it avoids editing 108 files and keeps the whole ontology reviewable in one
diff. The 108-ADR "backfill" ADR-0108 flagged is therefore done *in one file*. Trade-off: the ADR file
itself doesn't carry its topics — mitigated by the generated reverse index (ADR → subsystems) in MAP.md.

### D2 — Router, not payload; on-demand, never always-loaded (honors ADR-0001)

MAP.md is consulted **on demand** — by `adr-scout` (seed step) and `/new-adr` step 0 — and is **not**
folded into always-loaded `CLAUDE.md` context. This preserves the ADR-0001 token-budget discipline
(deep reference lives in on-demand skills/agents, not the static prompt). MAP.md points at the 3–5
relevant ADRs; it never inlines them.

### D3 — `adr-scout` seeds from the map but still greps live seams (refactors the agent)

`adr-scout`'s Method gains a step 0: read MAP.md, take the subsystem + its neighbours as the candidate
set. But the map indexes **ADRs**, and a shipped seam can exist in `estate/`/`core/`/`plugins/` with no
ADR — so the live shipped-seam grep (the step that caught ADR-0058's `builder:` in the estate) remains
mandatory. The map accelerates discovery; it never replaces the seam search.

### D4 — Three coexisting indexes, one consistency gate (reconciles README / roadmap / MAP)

The corpus now has three cross-reference axes, deliberately: **[README.md](README.md)** (chronological,
flat, hand-maintained, with supersede notes), **[roadmap.md](../roadmap.md)** (phase → deliverable → ADR
evidence), and **MAP.md** (subsystem graph, generated). They answer different questions (*when* / *which
phase* / *which subsystem + neighbours*). The consistency guarantee is the **coverage gate**: every ADR
file must appear in ≥1 subsystem, so MAP.md can never silently omit an ADR that README lists. MAP.md
links back to both other axes in its header.

### D5 — The subsystem edges are a curated navigation layer, NOT a second copy of the per-ADR lineage

~20 ADRs carry hand-authored `Builds on / Supersedes` lines — a fine-grained, per-ADR (ADR→ADR) lineage.
`topics.json`'s edges are **subsystem→subsystem** — a coarser, curated *navigation* graph at a different
granularity, deliberately hand-maintained (a subsystem grouping is a human judgement, not mechanically
derivable from per-ADR Builds-on). They are **complementary, not a second source of the same truth**: the
per-ADR lines remain the precise decision lineage (charter discipline #2's "no second truth" spirit is
respected — the subsystem graph is a *projection for navigation*, not an authoritative restatement of
lineage). They must not contradict; where a `Builds on` crosses subsystems, that is exactly the adjacency
edge the map should carry.

### D6 — MAP.md is a discovery router, distinct from the explanatory diagrams

`docs/architecture.md` and `docs/overview.md` carry hand-authored mermaid *flowcharts* that explain **how
the system works** (the projection loop, the plugin-port core). MAP.md is a generated graph that answers
**which decisions exist and where** — a different job. They coexist; MAP.md does not supersede them, and
the explanatory diagrams stay hand-authored (they encode understanding, not a mechanical index). Scoping
this explicitly avoids diagram-sprawl confusion across the three mermaid sources.

## Consequences

- **Positive.** Discovery is a cheap navigation step, not a keyword gamble: an agent (or human) reads one
  small router, sees the subsystem + its neighbours, and opens the few relevant ADRs — the ADR-0108
  scan's structural backstop. Can't rot (coverage + freshness gate in `ci`). Generated from one reviewable
  ontology. Proven: the generator validates 108 ADRs across 20 subsystems, and the coverage gate fires
  when an ADR is dropped.
- **Negative / cost.** The subsystem ontology is a human judgement that needs occasional curation as the
  architecture evolves (a new subsystem, a moved ADR) — the irreducible "ontology maintenance" cost, kept
  minimal by putting it in one file and gating only *coverage* (every ADR placed), not *correctness* of
  the placement. A new module (`tools/adrmap`) joins the workspace (stdlib-only, no deps).
- **Scope.** Ships the ontology + generator + MAP.md + the `ci` gate + the `adr-scout`/`new-adr` wiring.
  Does not regenerate README.md or the explanatory diagrams (D4/D6 — they coexist).

## Alternatives considered (rejected)

- **Per-ADR `topics:` frontmatter (ADR-0108's sketch).** Rejected (D1): can't express subsystem edges;
  108-file churn. One ontology file is more reviewable and carries the graph.
- **A hand-maintained MAP.md / TOC.** Rejected: rots silently and a stale map is worse than none. It must
  be generated + freshness-gated (the whole repo already disciplines generated artifacts this way —
  `generate:check` / `proto:check` / `migrate:lint`).
- **Derive subsystem edges from the per-ADR `Builds on` lines.** Rejected (D5): different granularity;
  subsystem grouping is a curation judgement, not mechanically derivable. Kept complementary.
- **Fold MAP.md into `CLAUDE.md` for always-on visibility.** Rejected (D2, ADR-0001): violates the
  token-budget/on-demand discipline. It is a router fetched on demand.

## Follow-ups

1. **Closes ADR-0108 Follow-up #1** (the generated topic index + freshness lint + `adr-scout` seeding).
2. Optional: a lint that flags a cross-subsystem `Builds on` whose subsystems are not connected by a
   `depends_on` edge — a soft consistency nudge between the two graphs (D5), not a hard gate.
3. Refine the subsystem taxonomy as the architecture evolves; the ontology is a living curation, the
   coverage gate keeps it complete.
