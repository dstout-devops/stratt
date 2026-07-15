# ADR 0042 — Cross-source Entity liveness (per-Source presence) + observedBy

- **Status:** Accepted
- **Date:** 2026-07-15
- **Deciders:** Project steward (dstout)
- **Charter sections:** §1.2 (projections, never a second truth; enforced in the data layer), §2.4 (no
  implicit precedence — the anti-GPO axiom), §2.1 (Entity + Provenance, exactly one answer), §2.2
  (Source/Syncer/Normalizer); resolves the **cross-source Entity liveness** deferral logged in
  **ADR-0038 §Honest deferrals** and re-noted in ADR-0041; completes the arc facets (ADR-0038) → labels
  (ADR-0041) → **liveness** started when the config-mgmt track (ADR-0037/0038/0039) first correlated one
  host across two Sources.

## Context

`graph.entity` liveness was a single `deleted_at` timestamp, and `TombstoneAbsent(prov, scheme, seen)`
soft-deleted the **whole** Entity for anything carrying an identity under `scheme` whose value was not in
`seen`. A host co-managed by Chef (`chef.node.name`) and Puppet (`puppet.certname`), correlated into
**one** Entity via a shared `dns.fqdn` identity, was therefore wrongly tombstoned by whichever Source's
cycle dropped it — hiding the *other* Source's facets — then resurrected (`deleted_at=NULL`) by the next
Source's upsert. Liveness was **last-writer-wins across Sources**: the §2.4 implicit-precedence pattern,
one level above the facet/label bags already fixed by ADR-0038/0041. It was the last un-owned
cross-source write surface on the Entity.

The Entity row also carried only a **last-writer** `prov_source_id` — a single-Source guess at "who
vouches for this Entity", not the true set.

## Decision

**Make liveness a UNION over Sources — an Entity is live while ≥1 Source still observes it — via a
per-(Source, Entity) presence table, all enforced in the data layer (§1.2).** Migration `00024`:

1. **`graph.entity_presence`** (`entity_id`, `source_id` → `graph.source`, `first_seen`, `last_seen`,
   `PK(entity_id, source_id)`; index on `source_id`). One row per Source that currently observes the
   Entity. Gated by the `enforce_write_path` trigger — presence is added to the same prov-check
   exemption as `entity_identity` (it carries no provenance columns), so only the Normalizer path may
   write it, but no writer-kind stamp is required.

2. **Record presence in the Projector's `upsertEntityTx`** for Syncer writes only
   (`WriterKind==syncer && SourceID!=""`): `INSERT ... ON CONFLICT DO UPDATE SET last_seen=now()`. Run
   writes record **no** presence — a run-only Entity stays outside the presence system and is never
   tombstoned (preserving today's behavior).

3. **Rewrite `TombstoneAbsent`/`TombstoneByIdentity`** (same signatures — **zero connector changes**; all
   7 Syncers already pass `SourceID`) as a **two-statement reconcile** in the existing normalizer tx:
   (A) `DELETE` the calling Source's presence rows for the vanished identities `RETURNING entity_id`;
   (B) `UPDATE graph.entity SET deleted_at=now(), prov=<the retracting Syncer>` for exactly those
   Entities whose **last** presence row was just removed (`NOT EXISTS` any remaining presence). A
   co-managed host keeps the other Source's presence row → stays live; the tombstone restamps the
   retracting Syncer's provenance (as before), which also satisfies `enforce_write_path` on the
   normalizer path. **Two statements, not a data-modifying CTE:** a CTE's `DELETE` is invisible to a
   `NOT EXISTS` in the same statement's outer query under Postgres snapshot rules, so the tombstone
   must be a second command in the same transaction.

4. **Surface `observedBy`** on the Entity read: `types.Entity.ObservedBy []SourceObservation`, populated
   by `reader.GetObservedBy` (`entity_presence JOIN graph.source`) and stitched into `GET /entities/{id}`
   beside Facets. The MCP `get_entity` proxy carries it through; the UI adds an "Observed by" card. This
   is the **true** presence set — who currently vouches for the Entity — replacing the last-writer
   `prov_source_id` guess, and partially closing the ADR-0039 projection-time-vs-observation-time
   deferral (`last_seen` is exposed, though still projection-time in v1).

**`deleted_at` stays the read gate, computed in Projector SQL, NOT a derivation trigger.** The §1.2
"enforced in the data layer" guarantee is already met by `enforce_write_path` (nothing outside the
Normalizer/Run path may touch `deleted_at` or presence); the *derivation* living in the one Projector
path all Syncers funnel through is identical in trust to today's `TombstoneAbsent`. A derivation trigger
is in fact mechanically impossible: an entity `UPDATE` fired from a trigger inside a normalizer tx would
re-hit `enforce_write_path`'s prov-check on a **run-stamped** row and raise. Readers, `entity_kind_idx`,
`entity_labels_gin`, and the 50k View gate are untouched.

**The invariant, stated narrowly:** an Entity that has *ever* carried Syncer presence is live iff it
currently has ≥1 presence row; an Entity that never entered the presence system (run-only) is governed
solely by its last `deleted_at` write. There is deliberately **no** blanket "live iff presence exists"
rule — that would tombstone every run-only Entity.

**Run+Syncer overlap semantic (decided):** a run-created Entity a Syncer *later* observes acquires
presence and becomes mortal — tombstoned when that Syncer drops it (observation = projection, §1.2).
Runs structurally cannot hold presence (no Source row to key on). Not reachable today (no tofu
`stratt_entities` output declares an identity scheme a Syncer enumerates); locked by a test.

## Charter posture

- **§2.4** removes the last cross-source last-writer-wins surface on the Entity — presence is an additive
  union, no precedence field. Completes facets → labels → liveness.
- **§1.2** liveness is a rebuildable projection enforced in the write path; `deleted_at` stays the gate.
- **§2.1** the Entity is live iff ≥1 owning Source observes it; `observedBy` gives "exactly one answer"
  per Source presence claim.

## Alternatives considered

- **Add `source_id` to the tombstone predicate, no presence table.** Insufficient: `deleted_at` is
  whole-Entity, so without a presence *set* you cannot know whether another Source still vouches; you'd
  reintroduce the guess. The table is the honest model.
- **Derive `deleted_at` via an `entity_presence` trigger.** Rejected — mechanically impossible (the
  trigger's entity UPDATE re-hits `enforce_write_path`'s prov-check on run-stamped rows) and unnecessary
  (the write-path gate already provides the §1.2 guarantee).
- **A multi-owner precedence field on liveness.** Rejected as charter-hostile (§2.4).
- **Derive observedBy from distinct facet `prov_source_id`.** An approximation (only Sources that own a
  facet, and no `first_seen`); the presence table gives the true set for free.

## Reviews

- **charter-guardian:** _(recorded in the slice commit)_ — §1.2 data-layer union-liveness; §2.4 last
  cross-source LWW surface closed; narrow invariant; observation semantics for run+syncer overlap.
- **vocabulary-linter:** `entity_presence`/`observedBy`/`SourceObservation` are clean — no banned term
  (`inventory`/`resource`/`CMDB`), no Named-Kind misuse.
- **No dependency-scout** — zero new dependencies (a migration + Projector SQL + a reader query + wire).

## Honest deferrals

- **Observation-time liveness** (ADR-0039 `manage.up`, carrying the Source's own cache timestamp as
  `last_seen` rather than projection-time) — `observedBy.lastSeen` is projection-time in v1.
- **Run presence / `onRemove`-style retract** (tofu-destroy explicitly retracting a run Entity) —
  run-only Entities stay live until re-observed or hard-deleted, as today.
- **Relation liveness** — relations have no per-Source presence; Entities only this slice.
- **Backfill fidelity** — the migration reconstructs only the last-writer Source's presence (historical
  multi-Source presence was never stored), so a co-managed host holds one presence row until each Source
  re-syncs: a transient window identical to today's last-writer behavior, self-healing after one full
  cycle per Source.

## Consequences

A host co-managed by two Sources is now one Entity that survives either Source dropping it and is
tombstoned only when the last Source stops observing it — cross-source liveness done right, matching the
cross-source *facts* the config-mgmt track already delivered. The Entity read now answers "who vouches
for this?" with the true Source set, not a last-writer guess. No new engine, no new dependency, no
connector change, and no cost to the read path or the View query gate. The ADR-0038 cross-source-liveness
deferral is resolved.
