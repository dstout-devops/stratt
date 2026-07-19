# ADR 0082 — Relation liveness: cross-source edge GC, the edge analog of entity presence

- **Status:** Accepted
- **Date:** 2026-07-19
- **Deciders:** steward (dstout)
- **Reviews:** charter-guardian PASS-WITH-CHANGES — shape approved as the correct edge analog of ADR-0042; three must-fixes folded: **MF-1** presence gated by a dedicated write-path trigger, **MF-2** the sweep + `RelationTypesBySource` scope by `relation_presence.source_id` (not the edge's last-writer `prov_source_id` — a real bug it caught), **MF-3** two-statement retract-then-delete (not a CTE) + the hard-delete self-heal semantics. vocabulary-linter deferred to slice merge (consistent with `entity_presence`).
- **Charter sections:** §1.2 (projections, never a second truth; liveness is a union over Sources), §2.1 (single write-owner; no implicit precedence), §2.4 (additive claims, per-element provenance), §1.8 (never hide a stale edge)
- **Closes:** the ADR-0059 "no relation tombstone/GC — edges to soft-deleted entities dangle" gap
- **Unblocks:** ADR-0081 `depends-on` (multi-source: a mesh AND declared deps)
- **Mirrors:** ADR-0042 (entity presence / union liveness), ADR-0060 (multi-source facet grain)

## Context

`graph.relation` is `UNIQUE(type, from_id, to_id)` — **one row per edge**, with `prov_source_id` as a last-writer attribute. That is fine for a **single-source** edge: ADR-0081 slice 1b's per-source full-sync delete-and-replace (`RetractSourceRelationsExcept`, scoped to `prov_source_id`) collects the reparent case for `provides`, which one collector fully enumerates.

It breaks for a **multi-source** edge. If a mesh *and* a declared source both assert `serviceA depends-on serviceB`, that is **one row** whose `prov_source_id` is whoever wrote last. A per-source sweep scoped to the mesh would delete an edge the declared source still asserts — a false retraction. This is exactly the problem ADR-0042 solved for **entities** (a host co-observed by Chef and Puppet stays live until *both* stop) and ADR-0060 for **facets** (each source keeps its own row). Relations lack the equivalent, so `depends-on` (inherently multi-source) cannot ship safely — the ADR-0081 guardian flagged this.

## Decision

**Add relation liveness as a union over Sources, via a `relation_presence` table — the edge analog of `entity_presence` (ADR-0042).** An observed edge is live while **any** Source asserts it, and is deleted only when its **last** Source presence is retracted.

### 1. `graph.relation_presence (relation_id, source_id)`
One row per (edge, asserting Source), PK `(relation_id, source_id)`, FK to `graph.relation(id)` and `graph.source(id)` both `ON DELETE CASCADE`. `graph.relation` stays the canonical single edge row (unchanged uniqueness), so existing readers and the FK cascade are untouched; presence is the per-source liveness ledger beside it. **Gated in the data layer (MF-1):** a dedicated `relation_presence_write_path` trigger enforces the §1.2 write path (only Normalizer/Run), WITHOUT the prov-kind check the shared `enforce_write_path` applies (presence carries no provenance columns — exactly the `entity_presence` exemption pattern). So the liveness ledger that co-decides edge deletion is itself write-path-gated — no code path can forge presence.

### 2. Write path (Syncer/observed edges)
When a Normalizer projects an edge from a Source, it upserts `graph.relation` (the edge, provenance = this write) **and** upserts `relation_presence` for `(edge, source_id)`. Additive per §2.4 — two Sources asserting one edge is not a conflict (unlike a facet *namespace*, whose value each source keeps separately; an edge is identity-only, so co-assertion is union presence, not a value fight).

### 3. Per-source full-sync GC (union liveness)
On a Source's full-sync boundary, in **two statements** (MF-3 — not a data-modifying CTE, whose `DELETE` is invisible to a same-statement `NOT EXISTS` under Postgres snapshot rules, per ADR-0042): (1) retract **that Source's** `relation_presence` rows for edges of the type it no longer emits; (2) delete any observed edge (`prov_writer_kind='syncer'`) of that type whose **last** presence row is now gone. A co-asserted edge survives one Source dropping it — ADR-0042's union rule for edges.

**The sweep is scoped by `relation_presence.source_id`, NEVER the edge's `prov_source_id` (MF-2 — a real bug the guardian caught).** `graph.relation.prov_source_id` is a *last-writer* field; a multi-source edge co-asserted by A but last-written by B has `prov_source_id=B`, so scoping A's sweep by it would never see the edge, never retract A's presence, and leave A's phantom presence keeping the edge alive forever after every real asserter drops it. Both the sweep and `RelationTypesBySource` (which picks the types to sweep) read the presence ledger. This replaces ADR-0081's direct edge delete, so the single-source case (`provides`) still collects cleanly and the multi-source case (`depends-on`) becomes correct.

**Concurrency & self-heal (MF-3):** the retract and the last-presence-gone delete serialize on the edge's `(type, from, to)` unique key against any concurrent `UpsertRelation` (which upserts that key), which narrows the race. And it is self-healing regardless: if a delete ever races an assert and collects an edge a Source still means, that Source's **next full sync re-upserts the edge + its presence** — identical to a wrongly-tombstoned Entity re-appearing on re-observe (ADR-0042). The hard-delete vs. entity soft-`deleted_at` asymmetry is thus consciously acceptable: convergence is one cycle, never permanent loss.

### 4. Run-provenance edges are out of scope
A build's `placed-in` edge (run provenance, no Source, single-owning-build per ADR-0059) keeps its existing lifecycle (`RetractRunRelationsFrom` + the endpoint-tombstone cascade). `relation_presence` tracks **observed** edges from Sources; a run edge writes no presence, and the last-presence-gone delete is scoped `prov_writer_kind='syncer'`, so it never collects a run edge. **Overlap ruling (matching ADR-0042's entity ruling):** if one canonical edge is both run-written and later observed by a Syncer, the Syncer write records presence and the edge becomes subject to presence liveness; the run lifecycle (`RetractRunRelationsFrom`) still governs the run-written placement half. For `provides`/`depends-on` (Syncer-only) this does not arise.

### 5. Endpoint-tombstone cascade still applies
When an endpoint Entity is tombstoned, `retractRelationsFor` (ADR-0059 7b) sweeps its edges; the `relation_presence` rows go with them via `ON DELETE CASCADE` on the edge/entity FKs. Presence is the *source-drop* half; the cascade is the *endpoint-gone* half — together, no dangling edge.

## Charter alignment
- **§1.2:** relation liveness is a union over Sources (rebuildable; a Source dropping an edge never removes another Source's truth). Enforced in the data layer (the presence table + the write path), not convention.
- **§2.1/§2.4:** additive co-assertion, per-source presence rows — no last-writer-wins, no precedence. An edge is identity-only, so there is no value to fight over.
- **§1.8:** a stale edge is collected (presence retract or cascade), never left dangling and never silently.

## Consequences
- **Positive:** multi-source relations are correct — `depends-on` (mesh + declared) can ship; `provides` hardens (single-source is the one-presence case); the ADR-0059 relation-GC gap closes; the model mirrors entity/facet liveness, so it is familiar.
- **Negative / trade-offs:** a second write per observed edge (the presence row) and a migration with a backfill (existing edges get one presence row from their `prov_source_id`); edges with a NULL `prov_source_id` (legacy run-written) are left to the run/cascade lifecycle, not presence.

## Slice roadmap
1. **This slice:** the `relation_presence` migration + backfill; the projector presence write + `RetractSourceRelationPresenceExcept` (retract presence, delete last-presence-gone edges); rewire the pluginhost full-sync sweep from direct-delete to presence-aware; the cross-source union-liveness test (edge co-asserted by two Sources survives one dropping it).
2. **Next:** `depends-on` proper — an observed Source (a service-mesh reader) projecting service→service edges over this liveness model (ADR-0081 slice 2).

## Alternatives considered
- **Per-source relation rows** (add `source_id` to the edge PK) — rejected: it forks the canonical edge (readers, the placement drift check, the FK cascade all assume one edge row). A presence side-table mirrors the *proven* ADR-0042 entity model with less blast radius.
- **Keep the ADR-0081 direct per-source delete** — rejected: it is correct only for single-source edges; `depends-on` is multi-source and would false-retract.
- **Ship `depends-on` single-source (one collector) and defer multi-source** — rejected as the design: a mesh and declared deps are the real SoRs; building the liveness foundation now is the honest path the guardian required, and it also hardens `provides`.
