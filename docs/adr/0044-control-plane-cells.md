# ADR 0044 — Control-plane Cells / multi-region (partitioned single-writer, one logical estate)

- **Status:** Accepted (design authority for the multi-slice Cells workstream; this ADR pins the COMPLETE
  architecture — later slices implement it without cutting corners). **Slice 1 (this commit):** the Cell as
  a first-class modeled concept + identity + provenance + collision-safe naming, backward-compatible as one
  Cell `local`.
- **Date:** 2026-07-15
- **Deciders:** Project steward (dstout)
- **Charter sections:** §0 (one typed estate graph), §1.2 (projection, not a second truth), §1.3 (rug-pull-
  proof — no gated tier), §1.4 (boring spine), §1.6 (one Principal/authz/audit/cost model), §1.8 (never hide
  failure), §2.1/§2.4 (exactly one answer / no implicit precedence / anti-GPO), §2.3 (Sites; and the new
  **Cell** Named Kind added to §2 alongside Site); realizes ADR-0040 §4 ("live/live/live in aggregate — cells
  + Sites", designed-there-built-here) and mirrors the ADR-0032 Site machinery (`mgmt.site` residency Facet,
  `ResolveTargetsBySite` fan-out, `run.sites`) one level up.

## §2 vocabulary addition (Cell — a new Named Kind)

Cell is admitted as a **new, adjacent** Named Kind. **Site's §2.3 definition is untouched** — Site remains an
execution locus; Cell is the control-plane shard that *contains* Sites. The frozen one-line §2.3 addition
(applied to `stratt-charter.md` by the steward at the highest review bar):

> **Cell** — a region-local, single-writer control-plane shard (its own boring-spine substrate). The fleet is
> many Cells presenting one logical estate, active/active across Cells with no datum multi-master; each datum
> has exactly one home Cell (§2.1). A Cell contains Sites; the built-in default is one Cell (`local`).

## Context

Stratt is one control plane today (single Postgres/NATS/Temporal/OpenFGA/object-store; no region/cell
identity). ADR-0040 rejected multi-master (Temporal is active-in-one-cluster; multi-master Postgres trades
silent LWW for write availability — a correctness regression that violates §2.1/§2.4) and deferred control-
plane cell-awareness. As the successor platform to fleet-scale estate tools (Intune/Jamf/SCCM/AWX), Stratt
needs true multi-region — done as the *most complete correct* form, not the most-available-at-any-cost form.

## Slice-2 refinements (accepted 2026-07-15 — supersede the `mgmt.cell` Facet references below)

Implementing slice 2 surfaced two refinements to the pinned design; the steward approved both:

1. **Residency is a set-once `home_cell` COLUMN on `graph.entity`, NOT a `mgmt.cell` Facet.** A Facet is
   last-writer (`ON CONFLICT DO UPDATE`), so a stray cross-Cell write would *silently overwrite* residency to
   match the writer — defeating the §2.4 placement-mismatch Finding this ADR also requires (the mismatch could
   never be observed). `home_cell` is stamped once at Entity creation (= the creating daemon's Cell), never
   touched on the correlate-UPDATE path, and mutated only by the slice-7 fenced re-home — mirroring the
   `run.cell`/`prov_cell` column precedent (not the soft, re-pointable `mgmt.site` routing hint). The slice-3
   router reads the column directly. **Everywhere below that says "`mgmt.cell` residency Facet", read
   "`home_cell` column".** `mgmt.site` (execution routing) is untouched.

2. **`Source.cell` = the registering daemon's Cell (Sources are env-registered, not CaC).** There is no CaC
   Source declaration; a Source homes to the Cell of the daemon whose `Register()` created it. Entity-inherits-
   Source-cell then holds by construction (the same daemon projects the Entity, stamping `home_cell`). The §2.4
   authority check compares an Entity's `home_cell` against the Cells of the Sources observing it (via the
   ADR-0042 `entity_presence` set) — a divergence is a cross-Cell identity collision (the multi-master
   condition) and raises a `framework='placement'`, `severity='critical'` Finding; it resolves when the
   collision clears (`placement-reconciled`) or the Entity is tombstoned (`entity-tombstoned`, ADR-0043).

**Slice-2 implementation-sequencing (deferred where the consumer/test lives, not corners cut — the design
above is unchanged):** the `run.cells` *touched-union* population lands with slice 5 (cross-Cell orchestration,
where a fan-out actually touches multiple Cells and descent consumes it); the `KindCell` CaC loader (declaring
`graph.cell` rows from Git) lands with slice 3 (its consumer is the federation router's peer-endpoint set).
Slice 2 ships the residency/homing data model + placement Findings + `Source.cell`/`run.cell` + `siteFile.cell`
+ the `SetRunCells`/`HomeCellsByEntities` plumbing those later slices consume.

## Decision (the complete architecture)

**Partitioned region-local single-writer Cells presenting ONE logical estate.** Not multi-master.

1. **Cell + homing.** Partition key = `cell`; every datum (Entity/Source/Site/Run/Intent/…) has exactly one
   **home Cell** — its sole writer, extending the existing single-writer invariants (`enforce_write_path`,
   `facet_owner` PK, single audit-sealer) from "one control plane" to "one per partition." Homing is
   **CaC-declared** (mirror of Site) with a per-Entity **`mgmt.cell` residency Facet** (exact mirror of
   `mgmt.site`: `{cell}`, unset⇒`local`, run/normalizer-written, read-only for routing). An Entity inherits
   its Source's Cell; a Run-created Entity inherits the Run's Cell.
   - **Authority rule (§2.4 anti-GPO):** CaC-declared Cell = *desired*, `mgmt.cell` = *observed*. Write-routing
     uses the CaC (desired) home. A CaC-vs-observed **mismatch raises a Finding** — never silently resolved
     (that would be implicit precedence). Placement, like Provenance, has exactly one answer.
2. **Re-homing is a FENCED two-phase move.** Moving a datum A→B uses a **fenced** lock (à la the Patroni
   fencing ADR-0040 relies on) so that during a partition the old-home and new-home Cells cannot *both*
   believe they hold write ownership. An advisory/unfenced lock is insufficient — it would reintroduce
   multi-master LWW at the worst moment. Single writer at every instant; the move is audited in both Cells.
3. **One logical estate — the `cellrouter`.** A stateless capability compiled into **every** strattd (not a
   new deployable; keeps §1.6 one-API for UI/CLI/CI/MCP). Reads scatter-gather across `graph.cell` peers with
   a **deterministic k-way merge** (reusing the replay-sort discipline already in `RoutedTargets`/`sites`),
   per-Cell `as-of` stamps, and **partial-result honesty** — an unreachable Cell is *named* in the response,
   never silently dropped (§1.8). Writes forward to the datum's **home** Cell; if the home is unreachable the
   write **fails loudly** (no failover-to-a-second-writer = no multi-master).
   - **Guardrail (§1.4):** the router is **scatter-gather + merge ONLY**. Cross-cell joins, distributed
     transactions, and query pushdown are **forbidden** — a distributed query engine would break the boring
     spine. Cross-cell Relations are **soft references** (by global Entity id, validated at the router), never
     a Postgres FK (different databases); in-cell Relations keep their FK.
4. **One Principal/authz/audit/cost (§1.6).**
   - **OIDC/Zitadel: global** (one issuer; Principals are global identities). strattd already treats OIDC as a
     stateless per-request verifier — a global issuer needs no per-Cell state.
   - **OpenFGA model + tuples: global** (one Git source, projected by the authz-home-Cell leader only, read-
     replicated per Cell; `HIGHER_CONSISTENCY` for must-be-fresh checks). Authz decisions are identical in
     every Cell.
   - **Audit: per-Cell hash-chain** (single sealer per chain — two sealers corrupt it), presented as **one
     logical stream** via federated read + one aggregated SIEM forwarder (ADR-0034). *Accepted (steward):*
     "one audit stream" = one logical/presented stream over N per-Cell tamper-evident chains; each chain is
     independently verifiable, cross-Cell order is not cryptographically linked (a single global sealer would
     put cross-region latency + shared-fate on every append — the wrong trade).
   - **Cost/usage: per-Cell attribution to the global Principal, aggregated at read.**
   - So: **identity + authz-model are globally shared** (a §1.6 requirement, with per-Cell read-replicas +
     ADR-0040 active-passive DR as the shared-fate mitigation); **graph/orchestration/execution/evidence are
     per-Cell**; **audit + cost are per-Cell-written, globally-read.**
5. **Orchestration.** Per-Cell Temporal (namespace `stratt-<cell>`, queue `stratt-runs-<cell>`) — respects
   active-in-one-cluster (a namespace never spans Cells). A Workflow spanning entities in multiple Cells runs
   a parent **`RunAcrossCells`** in the initiating Cell that partitions targets by home Cell (structural mirror
   of `ResolveTargetsBySite`, one level up), fans out **child Runs** to peer Cells' control APIs, awaits and
   merges (`RunOutcome`/`mergeResults`). `graph.run.sites` → add `graph.run.cells` (the union of Cells a Run
   touched). Cross-cell **descent** (Intent→…→Run→task-event) survives a Cell hop; an unreachable peer renders
   as a **named gap** (§1.8 — the ADR-0032 lossy-leaf disclosure, one level up).
6. **Execution.** Sites belong to a Cell (`graph.site.cell`); a Site's `sitegw` NATS work-queue lives on its
   Cell's NATS. The hop hierarchy is **cell-router/parent-workflow (cell→cell control) → cell-local orchestrate
   → sitegw (hub→Site NATS) → agent** — each layer keeps its single-writer/single-substrate assumption. Cross-
   cell is a control hop *above* Site dispatch, never a widening of the Site NATS work-queue across Cells.
7. **Schema skew (§1.5).** All Cells pin the same Facet/Contract registry version, or cross-Cell schema drift
   **blocks the merge** — schema drift is blocking, never silently absorbed into a federated union.
8. **Licensing (§1.3).** All Cell/multi-region/homing/routing code is in the **Apache-2.0 core, never `ee/`**.
   Multi-region affinity is the single most common capability commercial OSS gates behind an enterprise tier —
   the exact rug-pull §1.3 forbids. Cells are never a gated surface.
9. **Identity plumbing.** `STRATT_CELL_ID` (default `local`) is the daemon's own Cell id, stamped into write
   provenance (`Provenance.Cell`) and — for a named Cell — into the collision-prone shared-name control
   resources: leader lease (`strattd-leader-<cell>`), Temporal namespace/queue, and NATS stream/subject
   prefixes. The daemon **never self-registers** into `graph.cell`: that registry is CaC-declared (sole writer
   = the desired-state engine, mirroring Site) — a self-writing daemon would be a second writer to a projection
   (§1.2). All shared-name stamping is **gated on `cell != "local"`** so today's single-Cell deployment is
   byte-identical (namespace `default`, queue `stratt-runs`, lease `strattd-leader`, unprefixed subjects). The
   slice sequence below is the authority on *what lands when* (provenance + lease + Temporal namespace/queue in
   slice 1; NATS-subject scoping in slice 6 where a second Cell consumes it).
10. **Substrate HA/DR is deploy/runbook** (per-Cell in-region quorum HA + async cross-region DR replica /
    Temporal XDC / NATS mirror / object CRR — endpoints are already env strings). Cell failover promotes a
    Cell's DR replica set — a *within-Cell* DR event, bounded blast radius (the cell doctrine), human-authorized
    (no auto-flip on transient partition). The **code** is identity/homing/routing/cross-cell-orchestration.

## The correctness envelope (sequencing invariant, §1.8)

Fenced re-home + home-routed-loud-fail + per-Cell-audit-federated-read + partial-result honesty are a **single
atomic correctness envelope** that MUST land *before or with* the first slice where a second Cell owns real
data — never later polish. **No intermediate slice may permit two writers to one datum, a silent federation
drop, or a hidden audit gap.** Slice 1 (single Cell `local`) is safe to ship alone precisely because one Cell
cannot split-brain.

## Slice sequence (pinned — each a shippable increment; `local` keeps earlier slices no-ops)

1. **Cell as a modeled concept (this slice):** `STRATT_CELL_ID` + `graph.cell` registry (CaC-written; CRUD in
   place) + `Provenance.Cell` (`prov_cell` stamped) + homing columns (`site.cell`/`source.cell`/`run.cell`+
   `cells`, `audit.event.cell`) + collision-safe control naming (lease, Temporal namespace/queue), all gated on
   `cell != local`. NATS-subject scoping (slice 6) and the reusable-fan-out / `homeCell` seams (slices 2/3/5)
   are deferred to where a consumer + test exist — not shipped as unconsumed plumbing here.
2. **Homing semantics:** `mgmt.cell` Facet, Entity-inherits-Source-cell, CaC-vs-observed authority rule +
   **Finding-on-mismatch**, `graph.run.cell` home computation.
3. **`cellrouter` federation:** scatter-gather reads + deterministic merge + per-Cell `as-of` + partial-result
   honesty; home-Cell write forwarding; `graph.cell` globally replicated.
4. **Global identity/authz + federated audit/cost:** global OIDC/OpenFGA (authz-home-Cell leader sync); per-
   Cell audit chains + federated `ListAudit` merge + aggregated SIEM forwarder; per-identity cost aggregation.
5. **Cross-cell orchestration:** `RunAcrossCells` + `ResolveTargetsByCell` (the slice-1 seam) + child-Run fan-
   out to peer control APIs + `graph.run.cells`.
6. **Cross-cell execution wiring:** Cell-scoped NATS fully cut over; Site→Cell binding; the hop hierarchy e2e.
7. **Per-Cell DR + fenced re-home GA + failover drill** (mostly deploy/runbook); 99.99% multi-region evidence.

## Charter reconciliation

- **§2.1/§2.4** single-writer homing + no-multi-master + fenced re-home + CaC-authority-with-Finding-on-mismatch
  — "exactly one answer" preserved and *strengthened* per Cell; no LWW anywhere.
- **§1.6** one Principal (global OIDC), one authz model (global OpenFGA from one Git source), one *logical*
  audit stream (per-Cell chains, federated read) + one cost model, one API via the router — identical for
  UI/CLI/CI/MCP.
- **§0/§1.2** one *logical* graph, physically partitioned, globally queryable; each Cell's write-path
  invariants unchanged; rebuildable per Cell — not a second truth.
- **§1.4** zero new dependencies (same boring spine per Cell; a boring Go scatter-gather router; no distributed
  query engine). **§1.3** Apache-2.0 core, never gated.
- **§1.8** partial-result honesty + named-gap cross-Cell descent; nothing hidden.

## Residual tensions (steward-accepted)

1. **Federated cross-Cell reads are eventually consistent** (a global `/entities` unions per-Cell snapshots).
   A globally-linearizable estate read is not offered (CAP/PACELC — ADR-0040 already accepts this currency);
   single-Cell reads stay strongly consistent; per-Cell `as-of` + named-unreachable make it honest.
2. **Global OpenFGA + global OIDC are shared-fate** — the price of §1.6 "one model", mitigated by per-Cell
   read-replication + active-passive DR.
3. **No cross-Cell referential integrity in Postgres** — cross-Cell Relations are soft references (validated
   at the router), inherent to sharding.
4. **A brief fenced-re-home window and the ADR-0040 two-leader lease overlap** are the only moments single-
   writer leans on protocol rather than a DB constraint; the ADR-0034 expected-prev-hash CAS follow-up hardens
   the audit case.

## Reviews

- **charter-guardian:** direction **SOUND**; its must-fixes (fenced re-home, CaC-vs-observed authority +
  Finding-on-mismatch, correctness-envelope sequencing, no-distributed-query guardrail, blocking schema skew,
  core-not-`ee/`, per-Cell-audit acceptance) are captured above; **Cell** admitted as a §2 Named Kind (Site
  untouched). Re-reviewed against this text + the slice-1 diff.
- **vocabulary-linter:** Cell used consistently as a Named Kind; `graph.cell`/`prov_cell`/`mgmt.cell` clean;
  no banned term.
- **No dependency-scout** — zero new dependencies.

## Consequences

Cell exists as a first-class concept; the complete multi-region architecture is pinned so no later slice cuts a
corner. Slice 1 ships the modeled concept + identity + provenance + homing columns + collision-safe naming, a
no-op for the single `local` Cell (today's deployment byte-identical), with the seams later slices plug
federation, homing semantics, cross-Cell orchestration, and per-Cell DR into — building toward true multi-region
active/active-across-Cells with no datum multi-master.
